package postgres

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pablofornell/helios-audio-transcription-service/internal/domain"
)

// priorityIndexName is the partial index migration 00003 creates for Claim.
const priorityIndexName = "transcriptions_pending_priority_claim_idx"

// createPending inserts n pending rows one after the other, so their
// next_attempt_at and created_at both follow the order of the returned ids.
func createPending(t *testing.T, store *Store, n int) []string {
	t.Helper()
	ids := make([]string, n)
	for i := range ids {
		ids[i] = newID()
		require.NoError(t, store.Create(context.Background(), newTranscription(ids[i], time.Now().UTC())))
		time.Sleep(2 * time.Millisecond)
	}
	return ids
}

// drainClaims claims until the queue is idle and returns the ids in the
// order Claim returned them.
func drainClaims(t *testing.T, store *Store) []string {
	t.Helper()
	var order []string
	for {
		claimed, err := store.Claim(context.Background(), "w", time.Minute)
		require.NoError(t, err)
		if claimed == nil {
			return order
		}
		order = append(order, claimed.ID)
	}
}

// failOnly creates one row in an empty table, claims it and fails it.
func failOnly(t *testing.T, store *Store, code domain.ErrorCode) string {
	t.Helper()
	ctx := context.Background()
	id := newID()
	require.NoError(t, store.Create(ctx, newTranscription(id, time.Now().UTC())))
	claimed, err := store.Claim(ctx, "w", time.Minute)
	require.NoError(t, err)
	require.NotNil(t, claimed)
	require.Equal(t, id, claimed.ID)
	require.NoError(t, store.Finish(ctx, domain.FinishRequest{
		ID: id, LeaseToken: *claimed.LeaseToken, Status: domain.StatusFailed,
		Err: domain.New(code, "boom"),
	}))
	return id
}

func TestStore_Prioritize_PriorityBeatsOlderItems(t *testing.T) {
	requireDocker(t)
	truncateAll(t)
	store := New(testPool)
	ctx := context.Background()

	ids := createPending(t, store, 4)
	got, err := store.Prioritize(ctx, ids[3])
	require.NoError(t, err)
	require.NotNil(t, got.PriorityAt)
	assert.Equal(t, domain.StatusPending, got.Status)

	assert.Equal(t, []string{ids[3], ids[0], ids[1], ids[2]}, drainClaims(t, store),
		"the priority row goes first, then the others in FIFO order")
}

func TestStore_Prioritize_NewestClickGoesFirst(t *testing.T) {
	requireDocker(t)
	truncateAll(t)
	store := New(testPool)
	ctx := context.Background()

	ids := createPending(t, store, 4)
	for _, i := range []int{2, 3, 1} {
		_, err := store.Prioritize(ctx, ids[i])
		require.NoError(t, err)
		time.Sleep(2 * time.Millisecond)
	}

	assert.Equal(t, []string{ids[1], ids[3], ids[2], ids[0]}, drainClaims(t, store),
		"the newest click goes first; the row with no priority goes last")
}

func TestStore_Prioritize_DoesNotInheritBackoffOfAnotherRow(t *testing.T) {
	requireDocker(t)
	truncateAll(t)
	store := New(testPool)
	ctx := context.Background()

	ids := createPending(t, store, 2)
	// ids[0] waits for a long backoff of its own.
	_, err := testPool.Exec(ctx, `UPDATE transcriptions SET next_attempt_at = now() + interval '1 hour', attempts = 2, error = '{"code":"timeout","message":"x"}' WHERE id = $1`, ids[0])
	require.NoError(t, err)

	// ids[1] has priority and is due: the backoff of ids[0] does not hold it.
	_, err = store.Prioritize(ctx, ids[1])
	require.NoError(t, err)
	assert.Equal(t, []string{ids[1]}, drainClaims(t, store))

	// A priority row in backoff is not due until Prioritize clears it.
	got, err := store.Prioritize(ctx, ids[0])
	require.NoError(t, err)
	assert.Equal(t, 0, got.Attempts)
	assert.Equal(t, []string{ids[0]}, drainClaims(t, store), "Prioritize drops the backoff of its own row")
}

func TestStore_Prioritize_PriorityRowInBackoffDoesNotBlockOthers(t *testing.T) {
	requireDocker(t)
	truncateAll(t)
	store := New(testPool)
	ctx := context.Background()

	ids := createPending(t, store, 2)
	_, err := store.Prioritize(ctx, ids[1])
	require.NoError(t, err)
	// The priority row now fails once and waits for an automatic retry.
	_, err = testPool.Exec(ctx, `UPDATE transcriptions SET next_attempt_at = now() + interval '1 hour' WHERE id = $1`, ids[1])
	require.NoError(t, err)

	assert.Equal(t, []string{ids[0]}, drainClaims(t, store),
		"a priority row that is not due must not hold back a due row")
}

func TestStore_Prioritize_ResetsRetryState(t *testing.T) {
	requireDocker(t)
	truncateAll(t)
	store := New(testPool)
	ctx := context.Background()

	id := newID()
	require.NoError(t, store.Create(ctx, newTranscription(id, time.Now().UTC())))
	claimed, err := store.Claim(ctx, "w1", time.Minute)
	require.NoError(t, err)
	next := time.Now().UTC().Add(time.Hour)
	require.NoError(t, store.Finish(ctx, domain.FinishRequest{
		ID: id, LeaseToken: *claimed.LeaseToken, Status: domain.StatusPending,
		Err: domain.New(domain.ErrCodeTimeout, "slow"), NextAttemptAt: &next,
	}))
	before, err := store.Get(ctx, id)
	require.NoError(t, err)
	require.Equal(t, 1, before.Attempts)
	require.NotNil(t, before.Error)
	require.NotNil(t, before.StartedAt)

	got, err := store.Prioritize(ctx, id)
	require.NoError(t, err)
	assert.Equal(t, domain.StatusPending, got.Status)
	assert.Equal(t, 0, got.Attempts)
	assert.Nil(t, got.Error)
	assert.Nil(t, got.LeaseOwner)
	assert.Nil(t, got.LeaseToken)
	assert.Nil(t, got.LeaseExpiresAt)
	assert.Nil(t, got.StartedAt)
	assert.Nil(t, got.CompletedAt)
	require.NotNil(t, got.NextAttemptAt)
	assert.WithinDuration(t, time.Now(), *got.NextAttemptAt, 5*time.Second, "the backoff is gone")
	require.NotNil(t, got.PriorityAt)

	again, err := store.Claim(ctx, "w2", time.Minute)
	require.NoError(t, err)
	require.NotNil(t, again, "the row is due at once")
	assert.Equal(t, id, again.ID)
	assert.Equal(t, 1, again.Attempts, "a new attempt starts from zero")
}

// TestStore_Prioritize_FailedRowKeepsStoredSourceAndIsClaimable compares
// Prioritize with Requeue on a failed row: same cleanup. The source path and
// the probed audio metadata stay, so the new attempt reads the stored file.
func TestStore_Prioritize_FailedRowKeepsStoredSourceAndIsClaimable(t *testing.T) {
	requireDocker(t)
	truncateAll(t)
	store := New(testPool)
	ctx := context.Background()

	id := failOnly(t, store, domain.ErrCodeToolFailure)
	_, err := testPool.Exec(ctx, `UPDATE transcriptions SET cancel_requested = true, audio = jsonb_set(audio, '{codec}', '"mp3"') WHERE id = $1`, id)
	require.NoError(t, err)
	failed, err := store.Get(ctx, id)
	require.NoError(t, err)
	require.NotNil(t, failed.CompletedAt)

	got, err := store.Prioritize(ctx, id)
	require.NoError(t, err)
	assert.Equal(t, domain.StatusPending, got.Status)
	assert.Nil(t, got.Error)
	assert.Nil(t, got.StartedAt)
	assert.Nil(t, got.CompletedAt)
	assert.Nil(t, got.Result)
	assert.Equal(t, failed.SourcePath, got.SourcePath, "the stored source stays: the new attempt reads it")
	assert.Equal(t, "mp3", got.Audio.Codec, "probed metadata stays")

	var cancelRequested bool
	require.NoError(t, testPool.QueryRow(ctx, `SELECT cancel_requested FROM transcriptions WHERE id = $1`, id).Scan(&cancelRequested))
	assert.False(t, cancelRequested, "as Requeue does, the flag is cleared; else Claim would skip the row")

	claimed, err := store.Claim(ctx, "w2", time.Minute)
	require.NoError(t, err)
	require.NotNil(t, claimed)
	assert.Equal(t, id, claimed.ID)
}

func TestStore_Prioritize_MatchesRequeueCleanupOnFailedRow(t *testing.T) {
	requireDocker(t)
	truncateAll(t)
	store := New(testPool)
	ctx := context.Background()

	a := failOnly(t, store, domain.ErrCodeToolFailure)
	b := failOnly(t, store, domain.ErrCodeToolFailure)
	viaRequeue, err := store.Requeue(ctx, a)
	require.NoError(t, err)
	viaPrioritize, err := store.Prioritize(ctx, b)
	require.NoError(t, err)

	assert.Equal(t, viaRequeue.Status, viaPrioritize.Status)
	assert.Equal(t, viaRequeue.Attempts, viaPrioritize.Attempts)
	assert.Equal(t, viaRequeue.Error, viaPrioritize.Error)
	assert.Equal(t, viaRequeue.Result, viaPrioritize.Result)
	assert.Equal(t, viaRequeue.Audio, viaPrioritize.Audio)
	assert.Equal(t, viaRequeue.StartedAt, viaPrioritize.StartedAt)
	assert.Equal(t, viaRequeue.CompletedAt, viaPrioritize.CompletedAt)
	assert.Equal(t, viaRequeue.LeaseToken, viaPrioritize.LeaseToken)
	assert.Nil(t, viaRequeue.PriorityAt)
	assert.NotNil(t, viaPrioritize.PriorityAt, "this is the only difference")
}

func TestStore_Prioritize_RejectsInvalidStates(t *testing.T) {
	requireDocker(t)
	truncateAll(t)
	store := New(testPool)
	ctx := context.Background()

	// running
	runningID := newID()
	require.NoError(t, store.Create(ctx, newTranscription(runningID, time.Now().UTC())))
	_, err := store.Claim(ctx, "w", time.Minute)
	require.NoError(t, err)

	// succeeded
	succeededID := newID()
	require.NoError(t, store.Create(ctx, newTranscription(succeededID, time.Now().UTC())))
	claimed, err := store.Claim(ctx, "w", time.Minute)
	require.NoError(t, err)
	require.Equal(t, succeededID, claimed.ID)
	require.NoError(t, store.Finish(ctx, domain.FinishRequest{
		ID: succeededID, LeaseToken: *claimed.LeaseToken, Status: domain.StatusSucceeded,
		Result: &domain.Result{Text: "hello"},
	}))

	// canceled
	canceledID := newID()
	require.NoError(t, store.Create(ctx, newTranscription(canceledID, time.Now().UTC())))
	_, err = store.RequestCancel(ctx, canceledID)
	require.NoError(t, err)

	// pending, with a cancel request that the next sweep will act on
	cancelPendingID := newID()
	require.NoError(t, store.Create(ctx, newTranscription(cancelPendingID, time.Now().UTC())))
	_, err = testPool.Exec(ctx, `UPDATE transcriptions SET cancel_requested = true WHERE id = $1`, cancelPendingID)
	require.NoError(t, err)

	for name, id := range map[string]string{
		"running": runningID, "succeeded": succeededID, "canceled": canceledID, "cancel requested": cancelPendingID,
	} {
		before, err := store.Get(ctx, id)
		require.NoError(t, err)
		_, err = store.Prioritize(ctx, id)
		require.Error(t, err, name)
		assert.Equal(t, domain.ErrCodeConflict, domain.CodeOf(err), name)
		after, err := store.Get(ctx, id)
		require.NoError(t, err)
		assert.Equal(t, before, after, "%s: a rejected call changes nothing", name)
		assert.Nil(t, after.PriorityAt, name)
	}

	_, err = store.Prioritize(ctx, newID())
	assert.Equal(t, domain.ErrCodeNotFound, domain.CodeOf(err))
}

func TestStore_Prioritize_IsIdempotent(t *testing.T) {
	requireDocker(t)
	truncateAll(t)
	store := New(testPool)
	ctx := context.Background()

	ids := createPending(t, store, 2)
	first, err := store.Prioritize(ctx, ids[1])
	require.NoError(t, err)
	time.Sleep(2 * time.Millisecond)
	second, err := store.Prioritize(ctx, ids[1])
	require.NoError(t, err)

	assert.Equal(t, domain.StatusPending, second.Status)
	assert.Equal(t, 0, second.Attempts)
	require.NotNil(t, second.PriorityAt)
	assert.False(t, second.PriorityAt.Before(*first.PriorityAt), "priority_at moves to the newest click")
	assert.Equal(t, []string{ids[1], ids[0]}, drainClaims(t, store))
}

func priorityAtOf(t *testing.T, id string) *time.Time {
	t.Helper()
	var at *time.Time
	require.NoError(t, testPool.QueryRow(context.Background(), `SELECT priority_at FROM transcriptions WHERE id = $1`, id).Scan(&at))
	return at
}

// TestStore_Prioritize_ClearedOnEveryTerminalTransition proves each way a
// row leaves the queue clears priority_at.
func TestStore_Prioritize_ClearedOnEveryTerminalTransition(t *testing.T) {
	requireDocker(t)
	ctx := context.Background()

	// claimPriority makes a priority row and claims it. maxAttempts sets how
	// many attempts it has.
	claimPriority := func(t *testing.T, store *Store, maxAttempts int, lease time.Duration) (string, string) {
		t.Helper()
		truncateAll(t)
		id := newID()
		require.NoError(t, store.Create(ctx, newTranscriptionWithMaxAttempts(id, time.Now().UTC(), maxAttempts)))
		_, err := store.Prioritize(ctx, id)
		require.NoError(t, err)
		claimed, err := store.Claim(ctx, "w", lease)
		require.NoError(t, err)
		require.NotNil(t, claimed)
		require.NotNil(t, claimed.PriorityAt, "a claim keeps priority_at while the row runs")
		return id, *claimed.LeaseToken
	}

	store := New(testPool)

	for _, status := range []domain.Status{domain.StatusSucceeded, domain.StatusFailed, domain.StatusCanceled} {
		t.Run("finish "+string(status), func(t *testing.T) {
			id, token := claimPriority(t, store, 3, time.Minute)
			req := domain.FinishRequest{ID: id, LeaseToken: token, Status: status}
			if status == domain.StatusFailed {
				req.Err = domain.New(domain.ErrCodeInternal, "boom")
			}
			require.NoError(t, store.Finish(ctx, req))
			assert.Nil(t, priorityAtOf(t, id))
		})
	}

	t.Run("finish succeeded on a cancel-requested row", func(t *testing.T) {
		id, token := claimPriority(t, store, 3, time.Minute)
		_, err := store.RequestCancel(ctx, id)
		require.NoError(t, err)
		require.NoError(t, store.Finish(ctx, domain.FinishRequest{ID: id, LeaseToken: token, Status: domain.StatusSucceeded}))
		got, err := store.Get(ctx, id)
		require.NoError(t, err)
		assert.Equal(t, domain.StatusCanceled, got.Status)
		assert.Nil(t, priorityAtOf(t, id))
	})

	t.Run("cancel a pending row", func(t *testing.T) {
		truncateAll(t)
		id := newID()
		require.NoError(t, store.Create(ctx, newTranscription(id, time.Now().UTC())))
		_, err := store.Prioritize(ctx, id)
		require.NoError(t, err)
		require.NotNil(t, priorityAtOf(t, id))
		_, err = store.RequestCancel(ctx, id)
		require.NoError(t, err)
		assert.Nil(t, priorityAtOf(t, id))
	})

	t.Run("lease expiry with no attempt left", func(t *testing.T) {
		id, _ := claimPriority(t, store, 1, time.Millisecond)
		waitForLeaseExpiry(t)
		_, err := store.ReleaseExpired(ctx)
		require.NoError(t, err)
		got, err := store.Get(ctx, id)
		require.NoError(t, err)
		assert.Equal(t, domain.StatusFailed, got.Status)
		assert.Nil(t, priorityAtOf(t, id))
	})

	t.Run("sweep of a pending row with a cancel request", func(t *testing.T) {
		truncateAll(t)
		id := newID()
		require.NoError(t, store.Create(ctx, newTranscription(id, time.Now().UTC())))
		_, err := store.Prioritize(ctx, id)
		require.NoError(t, err)
		_, err = testPool.Exec(ctx, `UPDATE transcriptions SET cancel_requested = true WHERE id = $1`, id)
		require.NoError(t, err)
		_, err = store.ReleaseExpired(ctx)
		require.NoError(t, err)
		got, err := store.Get(ctx, id)
		require.NoError(t, err)
		assert.Equal(t, domain.StatusCanceled, got.Status)
		assert.Nil(t, priorityAtOf(t, id))
	})
}

// TestStore_Prioritize_KeptWhileAttemptsRemain proves that a retry keeps the
// priority. Each path that returns a row to pending is listed.
func TestStore_Prioritize_KeptWhileAttemptsRemain(t *testing.T) {
	requireDocker(t)
	ctx := context.Background()
	store := New(testPool)

	setup := func(t *testing.T, lease time.Duration) (string, string) {
		t.Helper()
		truncateAll(t)
		id := newID()
		require.NoError(t, store.Create(ctx, newTranscriptionWithMaxAttempts(id, time.Now().UTC(), 3)))
		_, err := store.Prioritize(ctx, id)
		require.NoError(t, err)
		claimed, err := store.Claim(ctx, "w", lease)
		require.NoError(t, err)
		return id, *claimed.LeaseToken
	}

	t.Run("finish with a scheduled retry", func(t *testing.T) {
		id, token := setup(t, time.Minute)
		next := time.Now().UTC().Add(time.Minute)
		require.NoError(t, store.Finish(ctx, domain.FinishRequest{
			ID: id, LeaseToken: token, Status: domain.StatusPending,
			Err: domain.New(domain.ErrCodeTimeout, "slow"), NextAttemptAt: &next,
		}))
		assert.NotNil(t, priorityAtOf(t, id))
	})

	t.Run("lease expiry with an attempt left", func(t *testing.T) {
		id, _ := setup(t, time.Millisecond)
		waitForLeaseExpiry(t)
		_, err := store.ReleaseExpired(ctx)
		require.NoError(t, err)
		assert.NotNil(t, priorityAtOf(t, id))
	})

	t.Run("release for shutdown", func(t *testing.T) {
		id, token := setup(t, time.Minute)
		require.NoError(t, store.ReleaseForShutdown(ctx, id, token))
		assert.NotNil(t, priorityAtOf(t, id))
	})
}

// TestStore_Prioritize_CannotStarveOthers walks a priority row that always
// fails with a retryable error through all its attempts. Another row waits
// the whole time. The priority ends at max_attempts, and the other row then
// runs.
func TestStore_Prioritize_CannotStarveOthers(t *testing.T) {
	requireDocker(t)
	truncateAll(t)
	store := New(testPool)
	ctx := context.Background()

	const maxAttempts = 3
	prio := newID()
	require.NoError(t, store.Create(ctx, newTranscriptionWithMaxAttempts(prio, time.Now().UTC(), maxAttempts)))
	time.Sleep(2 * time.Millisecond)
	other := newID()
	require.NoError(t, store.Create(ctx, newTranscriptionWithMaxAttempts(other, time.Now().UTC(), maxAttempts)))
	_, err := store.Prioritize(ctx, prio)
	require.NoError(t, err)

	for attempt := 1; attempt <= maxAttempts; attempt++ {
		claimed, err := store.Claim(ctx, "w", time.Minute)
		require.NoError(t, err)
		require.NotNil(t, claimed)
		require.Equal(t, prio, claimed.ID, "attempt %d: the priority row is first while it has attempts", attempt)
		require.Equal(t, attempt, claimed.Attempts)

		req := domain.FinishRequest{ID: prio, LeaseToken: *claimed.LeaseToken, Err: domain.New(domain.ErrCodeTimeout, "slow")}
		if attempt < maxAttempts {
			next := time.Now().UTC().Add(time.Hour)
			req.Status, req.NextAttemptAt = domain.StatusPending, &next
		} else {
			req.Status = domain.StatusFailed
		}
		require.NoError(t, store.Finish(ctx, req))

		if attempt < maxAttempts {
			// While the priority row waits for its backoff, the other row runs.
			assert.NotNil(t, priorityAtOf(t, prio))
			_, err = testPool.Exec(ctx, `UPDATE transcriptions SET next_attempt_at = now() WHERE id = $1`, prio)
			require.NoError(t, err)
		}
	}
	assert.Nil(t, priorityAtOf(t, prio), "the priority ends with the last attempt")

	claimed, err := store.Claim(ctx, "w", time.Minute)
	require.NoError(t, err)
	require.NotNil(t, claimed)
	assert.Equal(t, other, claimed.ID, "the other row runs after the priority row is out of attempts")
}

// TestStore_Prioritize_ConcurrentWithClaim runs Claim and Prioritize
// against the same rows. No row is claimed twice, no call fails with
// anything but a conflict (a row that Claim took first), and every row ends
// up claimed once.
func TestStore_Prioritize_ConcurrentWithClaim(t *testing.T) {
	requireDocker(t)
	truncateAll(t)
	store := New(testPool)
	ctx := context.Background()

	ids := createPending(t, store, 40)

	var (
		mu      sync.Mutex
		claimed = map[string]int{}
		errs    []error
		wg      sync.WaitGroup
	)
	for w := 0; w < 6; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				c, err := store.Claim(ctx, "w", time.Minute)
				if err != nil {
					mu.Lock()
					errs = append(errs, err)
					mu.Unlock()
					return
				}
				if c == nil {
					return
				}
				mu.Lock()
				claimed[c.ID]++
				mu.Unlock()
			}
		}()
	}
	for p := 0; p < 6; p++ {
		wg.Add(1)
		go func(p int) {
			defer wg.Done()
			for i := p; i < len(ids); i += 3 {
				_, err := store.Prioritize(ctx, ids[i])
				if err != nil && domain.CodeOf(err) != domain.ErrCodeConflict {
					mu.Lock()
					errs = append(errs, err)
					mu.Unlock()
				}
			}
		}(p)
	}
	wg.Wait()

	require.Empty(t, errs)
	// Drain what a worker left, in case it saw an idle queue before the last click.
	for _, id := range drainClaims(t, store) {
		claimed[id]++
	}
	assert.Len(t, claimed, len(ids))
	for _, id := range ids {
		assert.Equal(t, 1, claimed[id], "row %s is claimed exactly once", id)
	}
}

func TestStore_PriorityClaimIndexExists(t *testing.T) {
	requireDocker(t)
	var def string
	require.NoError(t, testPool.QueryRow(context.Background(),
		`SELECT indexdef FROM pg_indexes WHERE indexname = $1`, priorityIndexName).Scan(&def))
	assert.Contains(t, def, "priority_at DESC NULLS LAST, next_attempt_at, created_at, id")
	assert.Contains(t, def, "status = 'pending'")
}

// seedPending inserts n due pending rows in one statement and refreshes the
// planner statistics.
func seedPending(t *testing.T, n int) {
	t.Helper()
	truncateAll(t)
	_, err := testPool.Exec(context.Background(), `
		INSERT INTO transcriptions (id, status, options, audio, source_path, max_attempts, next_attempt_at, created_at, updated_at)
		SELECT gen_random_uuid(), 'pending', '{}', '{}', 'transcriptions/x/source.mp3', 3,
		       now() - interval '1 day', now() - (g || ' seconds')::interval, now()
		FROM generate_series(1, $1) g`, n)
	require.NoError(t, err)
	_, err = testPool.Exec(context.Background(), `ANALYZE transcriptions`)
	require.NoError(t, err)
}

// TestStore_Claim_LargeBacklogUsesPriorityIndex guards the cost of the
// priority order. Without the index of migration 00003, every claim sorts
// all the due pending rows. With it, a claim reads the first index entry.
// The time bound is loose, so a slow machine does not fail it. The plan
// check is the exact guard.
func TestStore_Claim_LargeBacklogUsesPriorityIndex(t *testing.T) {
	requireDocker(t)
	ctx := context.Background()
	store := New(testPool)
	const rows = 5000
	const bound = 1500 * time.Millisecond

	seedPending(t, rows)
	_, err := testPool.Exec(ctx, `UPDATE transcriptions SET priority_at = now() WHERE id IN (SELECT id FROM transcriptions LIMIT 3)`)
	require.NoError(t, err)

	// The plan of the candidate query, as Claim writes it.
	planRows, err := testPool.Query(ctx, `
		EXPLAIN SELECT id FROM transcriptions
		WHERE status = 'pending' AND next_attempt_at <= now() AND NOT cancel_requested
		ORDER BY priority_at DESC NULLS LAST, next_attempt_at, created_at, id
		FOR UPDATE SKIP LOCKED
		LIMIT 1`)
	require.NoError(t, err)
	var plan []string
	for planRows.Next() {
		var line string
		require.NoError(t, planRows.Scan(&line))
		plan = append(plan, line)
	}
	planRows.Close()
	require.NoError(t, planRows.Err())
	joined := strings.Join(plan, "\n")
	assert.Contains(t, joined, priorityIndexName)
	assert.NotContains(t, joined, "Sort", "the index gives the order: no sort of the backlog")

	start := time.Now()
	claimed, err := store.Claim(ctx, "w1", time.Minute)
	elapsed := time.Since(start)
	require.NoError(t, err)
	require.NotNil(t, claimed)
	require.NotNil(t, claimed.PriorityAt, "a priority row goes first")
	assert.Less(t, elapsed, bound, "a claim over %d pending rows must not scan them all", rows)
}

// TestMigration00003_DownThenUp runs the Down step of migration 00003 and
// applies it again. DownTo a fixed version keeps this test valid when a
// later migration exists.
func TestMigration00003_DownThenUp(t *testing.T) {
	requireDocker(t)
	ctx := context.Background()
	truncateAll(t)
	db := stdlib.OpenDB(*testPool.Config().ConnConfig)
	defer db.Close()
	goose.SetBaseFS(migrationsFS)
	require.NoError(t, goose.SetDialect("postgres"))
	t.Cleanup(func() { require.NoError(t, Migrate(ctx, testPool)) })

	count := func(query string) int {
		var n int
		require.NoError(t, testPool.QueryRow(ctx, query).Scan(&n))
		return n
	}
	const columns = `SELECT count(*) FROM information_schema.columns WHERE table_name = 'transcriptions' AND column_name = 'priority_at'`
	const indexes = `SELECT count(*) FROM pg_indexes WHERE indexname = '` + priorityIndexName + `'`

	require.NoError(t, goose.DownToContext(ctx, db, "migrations", 2))
	assert.Equal(t, 0, count(columns), "Down must drop priority_at")
	assert.Equal(t, 0, count(indexes), "Down must drop the index")

	require.NoError(t, Migrate(ctx, testPool))
	assert.Equal(t, 1, count(columns))
	assert.Equal(t, 1, count(indexes))
}

// TestStore_PurgeTerminalBefore_SkipsRowPrioritizedWhileDeleteWaits
// reproduces a race between the retention sweep and Prioritize on two
// connections. Connection A updates a failed row to pending, as Prioritize
// does, and holds the row lock. The purge starts on a second connection: its
// candidate query still sees the old version, so the DELETE waits for the
// lock. When A commits, the DELETE rechecks the new version and must skip the
// row. If it deletes the row, the user loses a job after a 202 answer.
func TestStore_PurgeTerminalBefore_SkipsRowPrioritizedWhileDeleteWaits(t *testing.T) {
	requireDocker(t)
	truncateAll(t)
	store := New(testPool)
	ctx := context.Background()

	id := failOnly(t, store, domain.ErrCodeToolFailure)

	tx, err := testPool.Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback(ctx) }()
	_, err = tx.Exec(ctx, `
		UPDATE transcriptions
		SET status = 'pending', attempts = 0, error = NULL, next_attempt_at = now(),
		    priority_at = now(), started_at = NULL, completed_at = NULL, updated_at = now()
		WHERE id = $1`, id)
	require.NoError(t, err)

	type result struct {
		purged []domain.PurgedTranscription
		err    error
	}
	done := make(chan result, 1)
	go func() {
		purged, err := store.PurgeTerminalBefore(ctx, time.Now().UTC().Add(time.Hour))
		done <- result{purged, err}
	}()

	// Wait until the DELETE waits for the row lock held by the transaction.
	require.Eventually(t, func() bool {
		var n int
		require.NoError(t, testPool.QueryRow(ctx, `
			SELECT count(*) FROM pg_stat_activity
			WHERE wait_event_type = 'Lock' AND query LIKE '%DELETE FROM transcriptions%'`).Scan(&n))
		return n > 0
	}, 10*time.Second, 10*time.Millisecond, "the purge must wait for the row lock")
	select {
	case r := <-done:
		t.Fatalf("the purge finished before the lock was released: %+v", r)
	default:
	}

	require.NoError(t, tx.Commit(ctx))
	r := <-done
	require.NoError(t, r.err)
	assert.Empty(t, r.purged, "a row that is pending again must not be purged")

	got, err := store.Get(ctx, id)
	require.NoError(t, err, "the row must still exist")
	assert.Equal(t, domain.StatusPending, got.Status)
	assert.NotNil(t, got.PriorityAt)
}
