package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/pablofornell/helios-audio-transcription-service/internal/domain"
)

// microsecondsOf turns a time.Duration into a Postgres interval expression
// fragment. Passed as a float64 microsecond count rather than relying on
// any Go<->pgtype.Interval mapping, which time.Duration does not have:
// `$n::float8 * interval '1 microsecond'` is precise down to the
// microsecond, which matters because tests use leases as short as 1ms.
func microsecondsOf(d time.Duration) float64 {
	return float64(d.Microseconds())
}

// purgeBatchLimit bounds how many rows a single PurgeTerminalBefore call
// deletes. Without it, a long-overdue sweep (retention disabled for a
// while, then re-enabled) could hold one DELETE's row locks and WAL volume
// open for an unbounded transaction; internal/retention's sweeper simply
// picks up whatever is left on its next tick instead.
const purgeBatchLimit = 1000

// Claim atomically selects the first due pending transcription and moves it
// to running: the WITH candidate AS (... FOR UPDATE SKIP LOCKED)
// picks the row under a row lock that a concurrent Claim will skip, and the
// UPDATE ... RETURNING commits status/attempts/lease/started_at in the same
// round trip -- so no two callers can ever be handed the same row. This is
// the single most important correctness property in this package; see
// TestStore_Claim_ConcurrentClaimersNeverOverlap.
//
// Claim order: rows with priority_at set come first, newest first
// (priority_at DESC NULLS LAST, see Prioritize). Then the normal order:
// next_attempt_at, created_at, id. The ORDER BY matches the partial index
// transcriptions_pending_priority_claim_idx (migration 00003), so LIMIT 1
// stops at the first row that passes the filters. Without that index the
// query sorts every due pending row on each claim.
//
// candidate's own WHERE clause repeats `status = 'pending' AND
// next_attempt_at <= now() AND NOT cancel_requested` directly rather than
// via a join against some other precomputed result, because under READ
// COMMITTED, when FOR UPDATE finds a row concurrently updated since this
// statement's snapshot was taken, Postgres re-fetches the latest committed
// version and re-checks it against the scan node's OWN quals
// (EvalPlanQual) before deciding whether to still return it. Keeping the
// predicate inline is what lets that recheck catch a row a losing
// concurrent Claim's stale snapshot still saw as pending. `AND NOT
// cancel_requested` is what stops a job canceled between a retryable
// failure and the next Finish/ReleaseExpired reap (see ReleaseExpired) from
// ever being handed to a worker again.
//
// Every claim mints a fresh lease_token (a UUID, independent of owner):
// this is THE guard Heartbeat and Finish check, not owner. owner alone
// cannot distinguish a stale execution from the same process's live one --
// see domain.Transcription.LeaseToken for why that distinction is the
// point of this column.
//
// Claim returns (nil, nil) -- not an error -- when nothing is due: an idle
// queue is the normal case, and callers (internal/jobs' poll loop) must not
// treat it as a failure worth logging or backing off from.
func (s *Store) Claim(ctx context.Context, owner string, lease time.Duration) (*domain.Transcription, error) {
	leaseToken := uuid.NewString()
	row := s.pool.QueryRow(ctx, `
		WITH candidate AS (
			SELECT id FROM transcriptions
			WHERE status = 'pending' AND next_attempt_at <= now() AND NOT cancel_requested
			ORDER BY priority_at DESC NULLS LAST, next_attempt_at, created_at, id
			FOR UPDATE SKIP LOCKED
			LIMIT 1
		)
		UPDATE transcriptions AS t
		SET status = 'running',
		    attempts = t.attempts + 1,
		    lease_owner = $1,
		    lease_token = $3::uuid,
		    lease_expires_at = now() + $2::float8 * interval '1 microsecond',
		    started_at = COALESCE(t.started_at, now()),
		    updated_at = now()
		FROM candidate AS c
		WHERE t.id = c.id
		RETURNING `+claimReturningColumns,
		owner, microsecondsOf(lease), leaseToken,
	)

	t, err := scanTranscription(row)
	if err == nil {
		return t, nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return nil, mapErr(err)
}

// Heartbeat extends the current claim's lease on id by lease and reports
// whether cancellation has been requested in the meantime -- this is the
// only channel by which Store.RequestCancel reaches a worker that is
// already running a job. The lease is extended only while the row is still
// running under leaseToken -- the token Claim minted for THIS attempt
// (`id=$1 AND lease_token=$2 AND status='running'`, per CONTRACT.md
// section 7 as amended by the lease-token fix) -- not merely under the
// caller's owner label. That distinction is what rejects a stale execution
// whose own process re-claimed the row after a lease expiry: the new claim
// minted a different token, so the old execution's Heartbeat now matches
// zero rows even though its owner string is identical to the new claim's.
// Zero rows matching means the lease was lost -- reclaimed by
// ReleaseExpired, finished by a stale duplicate, or the job no longer
// exists -- and is reported as ErrCodeConflict so the caller stops work it
// no longer owns rather than finishing it anyway.
func (s *Store) Heartbeat(ctx context.Context, id, leaseToken string, lease time.Duration) (bool, error) {
	var canceled bool
	err := s.pool.QueryRow(ctx, `
		UPDATE transcriptions
		SET lease_expires_at = now() + $3::float8 * interval '1 microsecond',
		    updated_at = now()
		WHERE id = $1 AND lease_token = $2::uuid AND status = 'running'
		RETURNING cancel_requested`,
		id, leaseToken, microsecondsOf(lease),
	).Scan(&canceled)
	if err == nil {
		return canceled, nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return false, domain.New(domain.ErrCodeConflict, fmt.Sprintf("heartbeat: lease token for %s is stale or lost", id))
	}
	return false, mapErr(err)
}

// Finish applies req's outcome to the job req.LeaseToken claimed, guarded
// the same way as Heartbeat: the write only lands where `id=$1 AND
// lease_token=$2 AND status='running'`, so a worker whose claim's token no
// longer matches -- whether because its lease expired and was reclaimed, or
// because a stale duplicate execution is calling in with a previous claim's
// token -- can never write a terminal state (or a retry) over whatever
// holds the row now, and zero rows affected is ErrCodeConflict.
//
// req.Status.Terminal() (succeeded, failed or canceled) sets completed_at,
// clears priority_at and clears the lease outright. The status actually written is not always
// req.Status, though: if cancel_requested was set on this row (an API
// caller canceled it after the worker's last heartbeat but before this
// Finish call), a reported "succeeded" is downgraded to "canceled" --
// otherwise a job the API already answered 200 to a cancel for could still
// be recorded successful. failed/canceled outcomes are unaffected: they are
// not the case this guards against.
//
// req.Status == domain.StatusPending is a scheduled retry instead: it
// requires req.NextAttemptAt, writes it, clears completed_at (a retried job
// is not completed, even if a previous attempt's failure left a stale
// completed_at behind -- see CONTRACT.md's retry-visibility fix) and
// likewise clears the lease so the row becomes claimable again once it is
// due. priority_at is left untouched here: a retry keeps the priority the
// user asked for, and max_attempts bounds how often that can happen.
// started_at is deliberately left untouched here: the job did start,
// just not successfully yet. Any other req.Status is rejected as invalid
// input -- Running is not a legal outcome, and neither is an unrecognised
// value.
//
// req.Result and req.Err are written verbatim in both branches (nil clears
// the column): a succeeded outcome's req.Err is nil, clearing whatever a
// previous failed attempt left behind, and req.Result is nil on every
// outcome but succeeded. req.Audio is written only when non-nil, via
// COALESCE, so a Finish call that does not carry freshly probed metadata
// never clobbers what an earlier attempt already recorded.
//
// The succeeded->canceled downgrade above does not merely relabel status: a
// row left with status='canceled' but result set to the discarded success
// would answer GET /{id} with a populated "result" field while GET
// /{id}/transcript still 409s it (that handler only serves succeeded rows),
// which is a lie by omission about what the row actually holds. When the
// downgrade fires, result is nulled and error is overwritten with a
// canceled-after-completion marker instead of the (nil) error a genuine
// success would have written, so the persisted row is indistinguishable
// from any other canceled job.
func (s *Store) Finish(ctx context.Context, req domain.FinishRequest) error {
	var (
		tag pgconn.CommandTag
		err error
	)

	switch {
	case req.Status.Terminal():
		var cancelDowngradeErr []byte
		if req.Status == domain.StatusSucceeded {
			cancelDowngradeErr, err = json.Marshal(domain.New(
				domain.ErrCodeCanceled,
				"canceled after this attempt had already completed",
			))
			if err != nil {
				return domain.Wrap(domain.ErrCodeInternal, "encode cancel-downgrade error", err)
			}
		}
		tag, err = s.pool.Exec(ctx, `
			UPDATE transcriptions
			SET status = CASE WHEN cancel_requested AND $3 = 'succeeded' THEN 'canceled' ELSE $3 END,
			    result = CASE WHEN cancel_requested AND $3 = 'succeeded' THEN NULL ELSE $4::jsonb END,
			    error = CASE WHEN cancel_requested AND $3 = 'succeeded' THEN $7::jsonb ELSE $5::jsonb END,
			    audio = COALESCE($6, audio),
			    lease_owner = NULL,
			    lease_token = NULL,
			    lease_expires_at = NULL,
			    completed_at = now(),
			    priority_at = NULL,
			    updated_at = now()
			WHERE id = $1 AND lease_token = $2::uuid AND status = 'running'`,
			req.ID, req.LeaseToken, string(req.Status), req.Result, req.Err, req.Audio, cancelDowngradeErr,
		)

	case req.Status == domain.StatusPending:
		if req.NextAttemptAt == nil {
			return domain.New(domain.ErrCodeInvalidInput, "finish: pending retry requires NextAttemptAt")
		}
		tag, err = s.pool.Exec(ctx, `
			UPDATE transcriptions
			SET status = 'pending',
			    error = $4,
			    audio = COALESCE($5, audio),
			    next_attempt_at = $3,
			    lease_owner = NULL,
			    lease_token = NULL,
			    lease_expires_at = NULL,
			    completed_at = NULL,
			    updated_at = now()
			WHERE id = $1 AND lease_token = $2::uuid AND status = 'running'`,
			req.ID, req.LeaseToken, *req.NextAttemptAt, req.Err, req.Audio,
		)

	default:
		return domain.New(domain.ErrCodeInvalidInput, fmt.Sprintf("finish: status %q is neither terminal nor pending", req.Status))
	}

	if err != nil {
		return mapErr(err)
	}
	if tag.RowsAffected() == 0 {
		return domain.New(domain.ErrCodeConflict, fmt.Sprintf("finish: lease token for %s is stale or lost", req.ID))
	}
	return nil
}

// ReleaseExpired recovers every running transcription whose lease has
// lapsed -- its worker died, or was killed, before it could Heartbeat or
// Finish -- and separately reaps every pending transcription still flagged
// cancel_requested straight to domain.StatusCanceled. Both are handled here
// because both are the same kind of problem: a row RequestCancel could not
// finish resolving on the spot (a running job's worker has to notice via
// Heartbeat; a job mid-retry-scheduling can race RequestCancel and land
// back on pending with the flag still set) and that this ticker-driven
// sweep is what eventually resolves. Without the second half, a
// cancel-requested row that lost that race would sit unclaimable forever
// (Claim's `AND NOT cancel_requested` predicate) but never actually
// terminal either.
//
// A running job whose lease expired has already burned an attempt: Claim
// incremented attempts when the now-dead worker took it. So recovery has
// to respect max_attempts the same way Finish does, or a worker that
// crashes on one particular input becomes a permanent loop -- claim, die,
// expire, requeue, claim -- with attempts climbing forever and nothing ever
// marking the job failed. Recovering the row and failing it are the same
// operation, decided per row.
//
// priority_at follows the same decision as completed_at. A row that moves to
// a terminal status (canceled or failed) loses its priority. A row that
// returns to pending keeps it, because it still has attempts left.
//
// It reports how many rows it recovered in total, counting the lease
// recoveries and the cancellation reaps together: both are "rows this sweep
// moved out of a state a worker can no longer act on", which is what the
// return value documents to callers (internal/jobs' sweep loop logs it).
func (s *Store) ReleaseExpired(ctx context.Context) (int, error) {
	failure, err := json.Marshal(domain.New(
		domain.ErrCodeTimeout,
		"worker lease expired without finishing the job, and no attempts remain",
	))
	if err != nil {
		return 0, domain.Wrap(domain.ErrCodeInternal, "encode lease-expiry error", err)
	}

	tag, err := s.pool.Exec(ctx, `
		UPDATE transcriptions
		SET status = CASE
		        WHEN status = 'pending' THEN 'canceled'
		        WHEN attempts >= max_attempts THEN 'failed'
		        ELSE 'pending'
		    END,
		    error = CASE WHEN status = 'running' AND attempts >= max_attempts THEN $1::jsonb ELSE error END,
		    completed_at = CASE WHEN status = 'pending' OR attempts >= max_attempts THEN now() ELSE completed_at END,
		    priority_at = CASE WHEN status = 'pending' OR attempts >= max_attempts THEN NULL ELSE priority_at END,
		    next_attempt_at = now(),
		    lease_owner = NULL,
		    lease_token = NULL,
		    lease_expires_at = NULL,
		    updated_at = now()
		WHERE (status = 'running' AND lease_expires_at < now())
		   OR (status = 'pending' AND cancel_requested)`,
		failure,
	)
	if err != nil {
		return 0, mapErr(err)
	}
	return int(tag.RowsAffected()), nil
}

// ReleaseForShutdown returns id to pending WITHOUT consuming the attempt it
// was claimed under -- unlike ReleaseExpired, which treats a lapsed lease as
// evidence the attempt genuinely failed to complete, a process stopping for
// an ordinary deploy is not a failed attempt at all. Without this, a job
// claimed on its last attempt (attempts == max_attempts) that happens to be
// running when SIGTERM arrives would sit `running` until its lease expired,
// at which point ReleaseExpired -- seeing no attempts left -- would record
// it permanently failed for a routine restart, not a real defect in the job.
// attempts is decremented (floored at 0, in case of a concurrent update
// racing this one) to give the attempt back rather than merely leaving it
// unclaimed with the same count. Guarded by leaseToken exactly like
// Heartbeat and Finish: zero rows affected means the lease was already lost
// to a re-claim, reported as ErrCodeConflict so the caller knows this write
// did not land and the row belongs to whoever holds it now.
func (s *Store) ReleaseForShutdown(ctx context.Context, id, leaseToken string) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE transcriptions
		SET status = 'pending',
		    attempts = GREATEST(attempts - 1, 0),
		    next_attempt_at = now(),
		    lease_owner = NULL,
		    lease_token = NULL,
		    lease_expires_at = NULL,
		    updated_at = now()
		WHERE id = $1 AND lease_token = $2::uuid AND status = 'running'`,
		id, leaseToken,
	)
	if err != nil {
		return mapErr(err)
	}
	if tag.RowsAffected() == 0 {
		return domain.New(domain.ErrCodeConflict, fmt.Sprintf("release for shutdown: lease token for %s is stale or lost", id))
	}
	return nil
}

// PurgeTerminalBefore deletes up to purgeBatchLimit terminal transcriptions
// whose completed_at is before cutoff and returns a light-weight record of
// the deleted rows -- ID and SourcePath only, per domain.PurgedTranscription
// -- so internal/retention's sweeper can remove their stored files. The
// database row is not the only thing a purge has to reclaim, but the
// sweeper only ever needs the id (to find the transcription's storage
// directory); scanning and returning every column, including the
// potentially large Result and Error JSONB, for rows about to be thrown
// away was pure waste. The candidate CTE both bounds the batch (LIMIT
// purgeBatchLimit, oldest first) and is what makes the transcriptions_retention_idx
// partial index (see migration 00002) usable instead of a sequential scan.
//
// The DELETE repeats the status and completed_at tests on t, not only
// t.id = c.id. The candidate CTE has no row lock, so another transaction can
// change a candidate row (Prioritize or Requeue make it pending again) while
// the DELETE waits for that row. Under READ COMMITTED, Postgres then
// rechecks the quals of t on the new version of the row. With the repeated
// tests, the recheck fails and the DELETE skips the row. Without them, it
// would delete a row that is pending again, and the caller would remove its
// files. The sweeper removes files only for the rows that RETURNING gives
// back, so a skipped row keeps its files.
func (s *Store) PurgeTerminalBefore(ctx context.Context, cutoff time.Time) ([]domain.PurgedTranscription, error) {
	rows, err := s.pool.Query(ctx, `
		WITH candidate AS (
			SELECT id FROM transcriptions
			WHERE status IN ('succeeded', 'failed', 'canceled') AND completed_at < $1
			ORDER BY completed_at
			LIMIT `+fmt.Sprint(purgeBatchLimit)+`
		)
		DELETE FROM transcriptions AS t
		USING candidate AS c
		WHERE t.id = c.id
		  AND t.status IN ('succeeded', 'failed', 'canceled') AND t.completed_at < $1
		RETURNING t.id::text, t.source_path`,
		cutoff,
	)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()

	var purged []domain.PurgedTranscription
	for rows.Next() {
		var p domain.PurgedTranscription
		if err := rows.Scan(&p.ID, &p.SourcePath); err != nil {
			return nil, mapErr(err)
		}
		purged = append(purged, p)
	}
	if err := rows.Err(); err != nil {
		return nil, mapErr(err)
	}
	return purged, nil
}
