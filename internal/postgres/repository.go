package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/pablofornell/helios-audio-transcription-service/internal/domain"
)

// Store implements internal/app's Repository and Queue ports (see
// internal/app/ports.go) on top of a single pgxpool.Pool. One type
// implements both: Get, List, Claim and PurgeTerminalBefore all scan the
// same transcriptions row shape, so splitting that scanning code across two
// types would just duplicate it.
type Store struct {
	pool *pgxpool.Pool
}

// New wraps pool in a Store. It does not take ownership of pool: callers
// open and close it themselves (see cmd/helios-transcription-api/wire).
func New(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
}

// ListFilter narrows and pages Store.List. It is field-for-field identical
// to app.ListFilter -- this package cannot import internal/app (see
// CONTRACT.md section 2's one-way import rule: implementations depend on
// domain only), so the two are separate named types that convert into each
// other with a plain Go type conversion at whatever call site needs both
// (the wire/DI container that hands a *Store to app.NewService).

// ListFilter and Page are aliases for the domain types so that *Store
// satisfies app.Repository exactly, without this package importing
// internal/app. See internal/domain/listing.go for why they live there.
type ListFilter = domain.ListFilter

// Page is one page of a Store.List call. See ListFilter.
type Page = domain.Page

// listDefaultLimit and listMaxLimit bound Store.List's page size, per
// CONTRACT.md section 5: clamped to [1,100], default 20.
const (
	listDefaultLimit = 20
	listMaxLimit     = 100
)

// Create inserts t in domain.StatusPending. Every column is taken verbatim
// from t, including CreatedAt and UpdatedAt: internal/app's Service
// controls those via app.Clock, and overriding them with the database's
// own now() would break its deterministic tests. NextAttemptAt is the one
// exception: when the caller leaves it nil (the ordinary "due immediately"
// case -- see CONTRACT.md's Create/Claim clock-skew fix), it is written as
// the database's own now() (`COALESCE($7, now())`), not t.CreatedAt taken
// from the app clock. Claim's predicate compares next_attempt_at against
// database now() too; stamping a freshly submitted row with the app
// clock's idea of "now" made it silently unclaimable under any container
// clock skew that put the app ahead of the database. A caller that DOES
// set NextAttemptAt explicitly (a deliberately scheduled future claim) is
// still honored verbatim.
func (s *Store) Create(ctx context.Context, t *domain.Transcription) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO transcriptions (
			id, status, options, audio, source_path,
			attempts, max_attempts, next_attempt_at,
			created_at, updated_at
		) VALUES (
			$1, 'pending', $2, $3, $4,
			$5, $6, COALESCE($7, now()),
			$8, $9
		)`,
		t.ID, t.Options, t.Audio, t.SourcePath,
		t.Attempts, t.MaxAttempts, t.NextAttemptAt,
		t.CreatedAt, t.UpdatedAt,
	)
	if err != nil {
		return mapErr(err)
	}
	t.Status = domain.StatusPending
	return nil
}

// Get returns the Transcription with the given id, or ErrCodeNotFound.
func (s *Store) Get(ctx context.Context, id string) (*domain.Transcription, error) {
	row := s.pool.QueryRow(ctx, `SELECT `+transcriptionColumns+` FROM transcriptions WHERE id = $1`, id)
	t, err := scanTranscription(row)
	if err != nil {
		return nil, mapErr(err)
	}
	return t, nil
}

// List defaults to newest-created keyset pagination. Library mode returns
// only finished items: status succeeded, with a stored result and stored
// audio. It orders them by completion time, newest first, using a separate
// rank/time/id cursor with full timestamp precision.
func (s *Store) List(ctx context.Context, f ListFilter) (Page, error) {
	limit := f.Limit
	switch {
	case limit <= 0:
		limit = listDefaultLimit
	case limit > listMaxLimit:
		limit = listMaxLimit
	}

	var (
		cursorCreatedAt time.Time
		cursorID        string
		cursorRank      int
		hasCursor       bool
	)
	if f.Cursor != "" {
		var err error
		if f.Library {
			cursorRank, cursorCreatedAt, cursorID, err = decodeLibraryCursor(f.Cursor)
		} else {
			cursorCreatedAt, cursorID, err = decodeCursor(f.Cursor)
		}
		if err != nil {
			return Page{}, err
		}
		hasCursor = true
	}

	query := `SELECT ` + transcriptionColumns + ` FROM transcriptions WHERE true`
	var args []any
	if f.Status != "" {
		args = append(args, string(f.Status))
		query += fmt.Sprintf(" AND status = $%d", len(args))
	}
	// Library rows are all finished, so they all have rank 2. The cursor keeps
	// its rank field so that cursors from earlier versions still decode.
	const libraryTimeSQL = "COALESCE(completed_at, created_at)"
	if f.Library {
		// Test for SQL NULL only. A comparison with JSON null reads every stored
		// transcript. The write path never stores JSON null (see Finish), and
		// the client also drops an item without a result.
		query += " AND status = 'succeeded' AND result IS NOT NULL"
		query += " AND btrim(source_path) <> '' AND btrim(audio->>'filename') <> '' AND (audio->>'sizeBytes')::bigint > 0"
	}
	// A cursor with a rank below 2 points into the old active section. That
	// section no longer exists, so such a cursor restarts at the first row.
	if hasCursor && f.Library && cursorRank == 2 {
		args = append(args, cursorCreatedAt, cursorID)
		at, id := len(args)-1, len(args)
		query += fmt.Sprintf(" AND (%s < $%d OR (%s = $%d AND id > $%d::uuid))", libraryTimeSQL, at, libraryTimeSQL, at, id)
	} else if hasCursor && !f.Library {
		args = append(args, cursorCreatedAt, cursorID)
		query += fmt.Sprintf(" AND (created_at, id) < ($%d, $%d)", len(args)-1, len(args))
	}
	// Fetch one extra row so we know whether a next page exists without an
	// extra round trip, and never hand back a NextCursor that leads to an
	// empty final page.
	args = append(args, limit+1)
	if f.Library {
		query += fmt.Sprintf(" ORDER BY %s DESC, id ASC LIMIT $%d", libraryTimeSQL, len(args))
	} else {
		query += fmt.Sprintf(" ORDER BY created_at DESC, id DESC LIMIT $%d", len(args))
	}

	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return Page{}, mapErr(err)
	}
	defer rows.Close()

	var items []*domain.Transcription
	for rows.Next() {
		t, err := scanTranscription(rows)
		if err != nil {
			return Page{}, mapErr(err)
		}
		items = append(items, t)
	}
	if err := rows.Err(); err != nil {
		return Page{}, mapErr(err)
	}

	page := Page{Items: items}
	if len(items) > limit {
		last := items[limit-1]
		page.Items = items[:limit]
		if f.Library {
			page.NextCursor = encodeLibraryCursor(last)
		} else {
			page.NextCursor = encodeCursor(last.CreatedAt, last.ID)
		}
	}
	return page, nil
}

// Delete removes the Transcription row with the given id, reporting
// ErrCodeNotFound when there was no such row.
func (s *Store) Delete(ctx context.Context, id string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM transcriptions WHERE id = $1`, id)
	if err != nil {
		return mapErr(err)
	}
	// A delete that matched nothing is ErrCodeNotFound, not success. Without
	// this the API would answer 204 for an id that never existed, and
	// app.Service.Delete -- which relies on this method to detect a missing
	// row rather than pre-checking -- would go on to remove a storage
	// directory for it. DELETE is idempotent at the SQL level; the HTTP
	// resource it backs is not.
	if tag.RowsAffected() == 0 {
		return domain.New(domain.ErrCodeNotFound, "transcription not found").
			WithDetails("id", id)
	}
	return nil
}

// RequestCancel sets cancel_requested and, when the job is still pending,
// moves it straight to domain.StatusCanceled -- no worker will ever claim a
// pending row again once this commits, so there is nothing left to notify.
// That move is terminal, so it clears priority_at. A running job keeps its
// priority_at until its worker calls Queue.Finish, which clears it.
// A running job is left running: its worker discovers the request at its
// next Queue.Heartbeat call and finishes the transition itself. A job that
// is already terminal returns ErrCodeConflict.
func (s *Store) RequestCancel(ctx context.Context, id string) (*domain.Transcription, error) {
	row := s.pool.QueryRow(ctx, `
		UPDATE transcriptions
		SET cancel_requested = true,
		    status = CASE WHEN status = 'pending' THEN 'canceled' ELSE status END,
		    lease_owner = CASE WHEN status = 'pending' THEN NULL ELSE lease_owner END,
		    lease_expires_at = CASE WHEN status = 'pending' THEN NULL ELSE lease_expires_at END,
		    completed_at = CASE WHEN status = 'pending' THEN now() ELSE completed_at END,
		    priority_at = CASE WHEN status = 'pending' THEN NULL ELSE priority_at END,
		    updated_at = now()
		WHERE id = $1 AND status IN ('pending', 'running')
		RETURNING `+transcriptionColumns,
		id,
	)
	t, err := scanTranscription(row)
	if err == nil {
		return t, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, mapErr(err)
	}
	return nil, s.notFoundOrConflict(ctx, id, "transcription %s is already terminal")
}

// Requeue moves a failed or canceled Transcription back to
// domain.StatusPending, ready to be claimed again: it resets Attempts to
// 0, clears the previous Error, lease and cancel_requested flag, and sets
// NextAttemptAt to now. It also clears CompletedAt and StartedAt: without
// that, a requeued job kept reporting the previous attempt's completion
// (and start) time even while StatusPending, which is a stale answer for a
// job that has not run yet this time around (CONTRACT.md's
// retry-visibility fix). Any other status returns ErrCodeConflict.
func (s *Store) Requeue(ctx context.Context, id string) (*domain.Transcription, error) {
	row := s.pool.QueryRow(ctx, `
		UPDATE transcriptions
		SET status = 'pending',
		    attempts = 0,
		    error = NULL,
		    lease_owner = NULL,
		    lease_token = NULL,
		    lease_expires_at = NULL,
		    cancel_requested = false,
		    next_attempt_at = now(),
		    started_at = NULL,
		    completed_at = NULL,
		    updated_at = now()
		WHERE id = $1 AND status IN ('failed', 'canceled')
		RETURNING `+transcriptionColumns,
		id,
	)
	t, err := scanTranscription(row)
	if err == nil {
		return t, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, mapErr(err)
	}
	return nil, s.notFoundOrConflict(ctx, id, "transcription %s is not failed or canceled")
}

// Prioritize moves a pending or failed transcription to the head of the
// queue and starts it from a clean retry state. It rejects a running,
// succeeded or canceled row with ErrCodeConflict, and an unknown id with
// ErrCodeNotFound. It also rejects a pending row that has cancel_requested
// set: the next ReleaseExpired sweep cancels that row.
//
// One UPDATE does all of this on the row of id. The cleanup is the same as
// in Requeue:
//
//   - attempts, error, lease, cancel_requested, started_at and completed_at
//     go back to their values for a new run.
//   - result, audio and source_path do not change. A pending or failed row
//     has no result. The stored source file stays on disk, so the new
//     attempt reads it. The retention sweep purges terminal rows only, and
//     this row is pending after the call.
//
// The UPDATE also does three things that Requeue does not:
//
//   - next_attempt_at becomes now(). This drops the backoff of this row.
//   - priority_at becomes now(). Claim takes the row with the newest
//     priority_at first.
//   - status becomes pending, also for a row that was pending already.
//
// Calling it again is safe. The row stays pending in a clean retry state,
// and priority_at moves to the time of the newest call.
//
// It does not touch any other row. The backoff of one row is not shared
// with another row, and a running row keeps its worker slot: the new
// attempt starts when a slot is free.
//
// priority_at stays set while the row is pending or running. An automatic
// retry keeps it, and max_attempts bounds those retries. Every terminal
// transition clears it (see Finish, RequestCancel and ReleaseExpired).
func (s *Store) Prioritize(ctx context.Context, id string) (*domain.Transcription, error) {
	row := s.pool.QueryRow(ctx, `
		UPDATE transcriptions
		SET status = 'pending',
		    attempts = 0,
		    error = NULL,
		    lease_owner = NULL,
		    lease_token = NULL,
		    lease_expires_at = NULL,
		    cancel_requested = false,
		    next_attempt_at = now(),
		    priority_at = now(),
		    started_at = NULL,
		    completed_at = NULL,
		    updated_at = now()
		WHERE id = $1 AND (status = 'failed' OR (status = 'pending' AND NOT cancel_requested))
		RETURNING `+transcriptionColumns,
		id,
	)
	t, err := scanTranscription(row)
	if err == nil {
		return t, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, mapErr(err)
	}
	return nil, s.notFoundOrConflict(ctx, id, "transcription %s is not pending or failed")
}

// notFoundOrConflict disambiguates a 0-rows-affected guarded UPDATE:
// RequestCancel, Requeue and Prioritize all need to tell "no such transcription" apart
// from "exists, but its status forbids this transition".
func (s *Store) notFoundOrConflict(ctx context.Context, id string, conflictFormat string) error {
	var exists bool
	if err := s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM transcriptions WHERE id = $1)`, id).Scan(&exists); err != nil {
		return mapErr(err)
	}
	if !exists {
		return domain.New(domain.ErrCodeNotFound, fmt.Sprintf("transcription %s not found", id))
	}
	return domain.New(domain.ErrCodeConflict, fmt.Sprintf(conflictFormat, id))
}
