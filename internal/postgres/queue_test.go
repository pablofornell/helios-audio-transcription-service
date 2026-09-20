package postgres

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pablofornell/helios-audio-transcription-service/internal/domain"
)

// TestStore_Claim_ConcurrentClaimersNeverOverlap seeds 50 pending rows and
// hammers Claim from 16 goroutines, one row at a time each: the union of
// everything claimed must be exactly those 50 rows, each claimed exactly
// once. This is the property FOR UPDATE SKIP LOCKED exists to guarantee,
// and per CONTRACT.md the single most important correctness point in this
// package.
func TestStore_Claim_ConcurrentClaimersNeverOverlap(t *testing.T) {
	requireDocker(t)
	truncateAll(t)
	store := New(testPool)
	ctx := context.Background()
	now := time.Now().UTC()

	const total = 50
	want := make(map[string]bool, total)
	for i := 0; i < total; i++ {
		id := newID()
		require.NoError(t, store.Create(ctx, newTranscription(id, now)))
		want[id] = true
	}

	const workers = 16
	var (
		mu   sync.Mutex
		seen = make(map[string]int, total)
		errs = make([]error, workers)
		wg   sync.WaitGroup
	)
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			owner := fmt.Sprintf("worker-%d", idx)
			for {
				claimed, err := store.Claim(ctx, owner, time.Minute)
				if err != nil {
					errs[idx] = err
					return
				}
				if claimed == nil {
					return
				}
				mu.Lock()
				seen[claimed.ID]++
				mu.Unlock()
			}
		}(w)
	}
	wg.Wait()

	for _, err := range errs {
		require.NoError(t, err)
	}

	assert.Len(t, seen, total)
	for id := range want {
		assert.Equal(t, 1, seen[id], "transcription %s must be claimed exactly once", id)
	}
}

// TestStore_Claim_SkipsRowLockedByAnotherTransaction proves FOR UPDATE SKIP
// LOCKED is actually in effect, not merely FOR UPDATE: a plain FOR UPDATE
// would make Claim block on the still-open transaction below until it
// commits or rolls back, and this test would time out rather than pass.
func TestStore_Claim_SkipsRowLockedByAnotherTransaction(t *testing.T) {
	requireDocker(t)
	truncateAll(t)
	store := New(testPool)
	ctx := context.Background()
	now := time.Now().UTC()

	locked := newID()
	require.NoError(t, store.Create(ctx, newTranscription(locked, now)))
	free := newID()
	require.NoError(t, store.Create(ctx, newTranscription(free, now.Add(time.Millisecond))))

	tx, err := testPool.Begin(ctx)
	require.NoError(t, err)
	defer tx.Rollback(ctx)

	_, err = tx.Exec(ctx, `SELECT 1 FROM transcriptions WHERE id = $1 FOR UPDATE`, locked)
	require.NoError(t, err)

	type result struct {
		claimed *domain.Transcription
		err     error
	}
	done := make(chan result, 1)
	go func() {
		claimed, err := store.Claim(ctx, "w1", time.Minute)
		done <- result{claimed, err}
	}()

	var res result
	select {
	case res = <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Claim blocked on a row locked by another, still-open transaction; FOR UPDATE SKIP LOCKED is not in effect")
	}

	require.NoError(t, res.err)
	require.NotNil(t, res.claimed, "Claim must skip the locked row and hand back the free one")
	assert.Equal(t, free, res.claimed.ID)

	require.NoError(t, tx.Rollback(ctx))
}

// TestStore_Claim_IdleQueueReturnsNilNil pins the contract's explicit
// "(nil, nil), not an error" idle-queue behaviour.
func TestStore_Claim_IdleQueueReturnsNilNil(t *testing.T) {
	requireDocker(t)
	truncateAll(t)
	store := New(testPool)

	claimed, err := store.Claim(context.Background(), "w1", time.Minute)
	require.NoError(t, err)
	assert.Nil(t, claimed)
}

// TestStore_Claim_SkipsNotYetDueRows pins next_attempt_at's role in the
// claim predicate: a row scheduled in the future must not be claimable yet.
func TestStore_Claim_SkipsNotYetDueRows(t *testing.T) {
	requireDocker(t)
	truncateAll(t)
	store := New(testPool)
	ctx := context.Background()
	now := time.Now().UTC()

	future := now.Add(time.Hour)
	tr := newTranscription(newID(), now)
	tr.NextAttemptAt = &future
	require.NoError(t, store.Create(ctx, tr))

	claimed, err := store.Claim(ctx, "w1", time.Minute)
	require.NoError(t, err)
	assert.Nil(t, claimed)
}

// TestStore_Heartbeat_ExtendsLeaseAndReportsCancelRequested proves
// Heartbeat both extends lease_expires_at and reports cancel_requested as
// its canceled result.
func TestStore_Heartbeat_ExtendsLeaseAndReportsCancelRequested(t *testing.T) {
	requireDocker(t)
	truncateAll(t)
	store := New(testPool)
	ctx := context.Background()
	now := time.Now().UTC()

	id := newID()
	require.NoError(t, store.Create(ctx, newTranscription(id, now)))

	claimed, err := store.Claim(ctx, "w1", 200*time.Millisecond)
	require.NoError(t, err)
	require.NotNil(t, claimed)
	require.NotNil(t, claimed.LeaseExpiresAt)
	require.NotNil(t, claimed.LeaseToken)
	shortLease := *claimed.LeaseExpiresAt

	canceled, err := store.Heartbeat(ctx, id, *claimed.LeaseToken, time.Hour)
	require.NoError(t, err)
	assert.False(t, canceled)

	got, err := store.Get(ctx, id)
	require.NoError(t, err)
	require.NotNil(t, got.LeaseExpiresAt)
	assert.Greater(t, got.LeaseExpiresAt.Sub(shortLease), 55*time.Minute,
		"Heartbeat must extend lease_expires_at out by ~1h, not leave the original short lease in place")

	// Sleep past the original 200ms lease. If Heartbeat did not really
	// extend it, ReleaseExpired will reclaim this row right about now.
	time.Sleep(400 * time.Millisecond)
	n, err := store.ReleaseExpired(ctx)
	require.NoError(t, err)
	assert.Equal(t, 0, n, "a lease genuinely extended to 1h out must not be reclaimed as expired")

	// RequestCancel while running must surface through the very next
	// Heartbeat as canceled=true, without erroring.
	_, err = store.RequestCancel(ctx, id)
	require.NoError(t, err)
	canceled, err = store.Heartbeat(ctx, id, *claimed.LeaseToken, time.Hour)
	require.NoError(t, err)
	assert.True(t, canceled, "Heartbeat must report cancel_requested once RequestCancel has run")
}

// TestStore_Heartbeat_WrongLeaseTokenIsConflict pins the lease guard: a
// caller presenting a well-formed but non-matching token -- one that never
// held (or no longer holds) the lease -- gets ErrCodeConflict. This is
// deliberately a token from a DIFFERENT claim, not the row's owner label:
// per the lease-token fix (CONTRACT.md), identity is per-claim, and owner
// is no longer part of the guard at all.
func TestStore_Heartbeat_WrongLeaseTokenIsConflict(t *testing.T) {
	requireDocker(t)
	truncateAll(t)
	store := New(testPool)
	ctx := context.Background()
	now := time.Now().UTC()

	id := newID()
	require.NoError(t, store.Create(ctx, newTranscription(id, now)))
	_, err := store.Claim(ctx, "w1", time.Minute)
	require.NoError(t, err)

	_, err = store.Heartbeat(ctx, id, newID(), time.Minute)
	require.Error(t, err)
	assert.Equal(t, domain.ErrCodeConflict, domain.CodeOf(err))
}

// TestStore_Heartbeat_NoSuchLease covers a lease guard failing because the
// id was never claimed at all (still pending) -- also ErrCodeConflict, per
// CONTRACT.md's "zero rows match" wording, which does not distinguish "no
// such id" from "not running".
func TestStore_Heartbeat_NoSuchLease(t *testing.T) {
	requireDocker(t)
	truncateAll(t)
	store := New(testPool)
	_, err := store.Heartbeat(context.Background(), newID(), newID(), time.Minute)
	assert.Equal(t, domain.ErrCodeConflict, domain.CodeOf(err))
}

// TestStore_Finish_Succeeded pins the succeeded terminal transition:
// completed_at set, result persisted, lease cleared.
func TestStore_Finish_Succeeded(t *testing.T) {
	requireDocker(t)
	truncateAll(t)
	store := New(testPool)
	ctx := context.Background()
	now := time.Now().UTC()

	id := newID()
	require.NoError(t, store.Create(ctx, newTranscription(id, now)))
	claimed, err := store.Claim(ctx, "w1", time.Minute)
	require.NoError(t, err)
	require.NotNil(t, claimed)
	require.NotNil(t, claimed.LeaseToken)

	result := &domain.Result{Text: "ok", Language: "en", Model: "base", Task: domain.TaskTranscribe}
	require.NoError(t, store.Finish(ctx, domain.FinishRequest{
		ID: id, LeaseToken: *claimed.LeaseToken, Status: domain.StatusSucceeded, Result: result,
	}))

	got, err := store.Get(ctx, id)
	require.NoError(t, err)
	assert.Equal(t, domain.StatusSucceeded, got.Status)
	require.NotNil(t, got.Result)
	assert.Equal(t, *result, *got.Result)
	assert.Nil(t, got.Error)
	assert.Nil(t, got.LeaseOwner)
	assert.Nil(t, got.LeaseToken)
	assert.Nil(t, got.LeaseExpiresAt)
	require.NotNil(t, got.CompletedAt)
}

// TestStore_Finish_Failed pins the failed terminal transition: error
// persisted, completed_at set.
func TestStore_Finish_Failed(t *testing.T) {
	requireDocker(t)
	truncateAll(t)
	store := New(testPool)
	ctx := context.Background()
	now := time.Now().UTC()

	id := newID()
	require.NoError(t, store.Create(ctx, newTranscription(id, now)))
	claimed, err := store.Claim(ctx, "w1", time.Minute)
	require.NoError(t, err)
	require.NotNil(t, claimed.LeaseToken)

	jobErr := domain.New(domain.ErrCodeToolFailure, "whisper-cli exited 1").WithDetails("stderr", "boom")
	require.NoError(t, store.Finish(ctx, domain.FinishRequest{
		ID: id, LeaseToken: *claimed.LeaseToken, Status: domain.StatusFailed, Err: jobErr,
	}))

	got, err := store.Get(ctx, id)
	require.NoError(t, err)
	assert.Equal(t, domain.StatusFailed, got.Status)
	require.NotNil(t, got.Error)
	assert.Equal(t, jobErr.Code, got.Error.Code)
	assert.Equal(t, jobErr.Message, got.Error.Message)
	assert.Equal(t, jobErr.Details["stderr"], got.Error.Details["stderr"])
	require.NotNil(t, got.CompletedAt)
}

// TestStore_Finish_PendingRetry pins the scheduled-retry transition: status
// returns to pending, next_attempt_at is written, the lease is cleared, and
// completed_at is NOT set (the job is not terminal).
func TestStore_Finish_PendingRetry(t *testing.T) {
	requireDocker(t)
	truncateAll(t)
	store := New(testPool)
	ctx := context.Background()
	now := time.Now().UTC()

	id := newID()
	require.NoError(t, store.Create(ctx, newTranscription(id, now)))
	claimed, err := store.Claim(ctx, "w1", time.Minute)
	require.NoError(t, err)
	require.NotNil(t, claimed.LeaseToken)
	require.NotNil(t, claimed.StartedAt)

	jobErr := domain.New(domain.ErrCodeTimeout, "probe timed out")
	next := now.Add(10 * time.Second)
	require.NoError(t, store.Finish(ctx, domain.FinishRequest{
		ID: id, LeaseToken: *claimed.LeaseToken, Status: domain.StatusPending,
		Err: jobErr, NextAttemptAt: &next,
	}))

	got, err := store.Get(ctx, id)
	require.NoError(t, err)
	assert.Equal(t, domain.StatusPending, got.Status)
	require.NotNil(t, got.Error)
	assert.Equal(t, jobErr.Code, got.Error.Code)
	assert.Nil(t, got.LeaseOwner)
	assert.Nil(t, got.LeaseToken)
	assert.Nil(t, got.LeaseExpiresAt)
	assert.Nil(t, got.CompletedAt)
	assert.NotNil(t, got.StartedAt, "a retry's StartedAt must survive Finish's pending branch: the job did start")
	require.NotNil(t, got.NextAttemptAt)
	assert.WithinDuration(t, next, *got.NextAttemptAt, time.Microsecond)

	// Not due yet: must not be claimable.
	claimed, err = store.Claim(ctx, "w2", time.Minute)
	require.NoError(t, err)
	assert.Nil(t, claimed)
}

// TestStore_Finish_StaleLeaseIsConflict pins the lease guard on Finish: a
// caller that lost its lease (here, because the job was already reclaimed
// by a second worker) must not be able to write any outcome.
func TestStore_Finish_StaleLeaseIsConflict(t *testing.T) {
	requireDocker(t)
	truncateAll(t)
	store := New(testPool)
	ctx := context.Background()
	now := time.Now().UTC()

	id := newID()
	require.NoError(t, store.Create(ctx, newTranscription(id, now)))
	staleClaim, err := store.Claim(ctx, "w1", time.Millisecond)
	require.NoError(t, err)
	require.NotNil(t, staleClaim.LeaseToken)
	staleToken := *staleClaim.LeaseToken

	waitForLeaseExpiry(t)
	n, err := store.ReleaseExpired(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, n)

	claimed, err := store.Claim(ctx, "w2", time.Minute)
	require.NoError(t, err)
	require.NotNil(t, claimed)

	// w1's execution presents the token from its ORIGINAL claim -- the one
	// ReleaseExpired invalidated -- not the fresh token w2's Claim minted.
	err = store.Finish(ctx, domain.FinishRequest{
		ID: id, LeaseToken: staleToken, Status: domain.StatusSucceeded,
		Result: &domain.Result{Model: "base", Task: domain.TaskTranscribe},
	})
	require.Error(t, err)
	assert.Equal(t, domain.ErrCodeConflict, domain.CodeOf(err))

	got, err := store.Get(ctx, id)
	require.NoError(t, err)
	assert.Equal(t, domain.StatusRunning, got.Status, "w1's stale Finish must not have touched the row w2 now owns")
}

// TestStore_Heartbeat_And_Finish_RejectStaleExecutionAfterSameOwnerReclaim
// is the exact scenario the lease-token fix exists for (review finding 1):
// a lease expires WHILE ITS WORKER IS STILL RUNNING (it never stopped
// executing, it just missed a heartbeat past the lease), the row is
// reclaimed -- here, by the very same owner string, which is the case a
// per-process owner guard cannot distinguish from the live execution at
// all -- and the ORIGINAL, still-running execution's Heartbeat AND Finish
// must both still be rejected as ErrCodeConflict, because they carry the
// first claim's token, not the second one. Before the fix, a per-owner
// guard would let both the stale execution and the new claim finish the
// same job.
func TestStore_Heartbeat_And_Finish_RejectStaleExecutionAfterSameOwnerReclaim(t *testing.T) {
	requireDocker(t)
	truncateAll(t)
	store := New(testPool)
	ctx := context.Background()
	now := time.Now().UTC()
	const owner = "worker-1" // deliberately the SAME owner both times.

	id := newID()
	require.NoError(t, store.Create(ctx, newTranscription(id, now)))

	// The first execution claims the job and is still "running" from its
	// own point of view -- nothing about it has crashed or given up -- but
	// its lease is short enough to lapse before it ever heartbeats again.
	firstClaim, err := store.Claim(ctx, owner, time.Millisecond)
	require.NoError(t, err)
	require.NotNil(t, firstClaim.LeaseToken)
	staleToken := *firstClaim.LeaseToken

	// The lease lapses, ReleaseExpired recovers the row to pending, and the
	// SAME owner's poll loop re-claims its own job -- a normal, expected
	// sequence after a database blip, not a bug in itself.
	waitForLeaseExpiry(t)
	n, err := store.ReleaseExpired(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, n)

	secondClaim, err := store.Claim(ctx, owner, time.Minute)
	require.NoError(t, err)
	require.NotNil(t, secondClaim)
	require.NotNil(t, secondClaim.LeaseToken)
	assert.NotEqual(t, staleToken, *secondClaim.LeaseToken,
		"a re-claim must mint a fresh token even under the same owner string")

	// The original execution, unaware any of this happened, is still
	// running and calls Heartbeat with the token from its now-invalidated
	// claim. A per-owner guard would have accepted this -- owner matches --
	// which is exactly the defect under test.
	_, err = store.Heartbeat(ctx, id, staleToken, time.Minute)
	require.Error(t, err)
	assert.Equal(t, domain.ErrCodeConflict, domain.CodeOf(err),
		"a stale execution's Heartbeat must be rejected even when its owner string is identical to the new claim's")

	// And even if the original execution goes on to "successfully" finish
	// its work and calls Finish with that same stale token, it must not be
	// allowed to record any outcome over the row the second claim now owns.
	err = store.Finish(ctx, domain.FinishRequest{
		ID: id, LeaseToken: staleToken, Status: domain.StatusSucceeded,
		Result: &domain.Result{Model: "base", Task: domain.TaskTranscribe},
	})
	require.Error(t, err)
	assert.Equal(t, domain.ErrCodeConflict, domain.CodeOf(err),
		"a stale execution's Finish must be rejected even when its owner string is identical to the new claim's")

	// The live (second) claim must be completely unaffected: it can still
	// heartbeat and finish normally.
	canceled, err := store.Heartbeat(ctx, id, *secondClaim.LeaseToken, time.Minute)
	require.NoError(t, err)
	assert.False(t, canceled)

	require.NoError(t, store.Finish(ctx, domain.FinishRequest{
		ID: id, LeaseToken: *secondClaim.LeaseToken, Status: domain.StatusSucceeded,
		Result: &domain.Result{Model: "base", Task: domain.TaskTranscribe},
	}))

	got, err := store.Get(ctx, id)
	require.NoError(t, err)
	assert.Equal(t, domain.StatusSucceeded, got.Status, "the live claim's Finish must have landed")
}

// TestStore_ReleaseExpired_ReturnsExpiredRunningRowToPending is the exact
// scenario named by CONTRACT.md's test list.
func TestStore_ReleaseExpired_ReturnsExpiredRunningRowToPending(t *testing.T) {
	requireDocker(t)
	truncateAll(t)
	store := New(testPool)
	ctx := context.Background()
	now := time.Now().UTC()

	id := newID()
	require.NoError(t, store.Create(ctx, newTranscription(id, now)))
	_, err := store.Claim(ctx, "w1", time.Millisecond)
	require.NoError(t, err)

	waitForLeaseExpiry(t)
	n, err := store.ReleaseExpired(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, n)

	got, err := store.Get(ctx, id)
	require.NoError(t, err)
	assert.Equal(t, domain.StatusPending, got.Status)
	assert.Nil(t, got.LeaseOwner)
	assert.Nil(t, got.LeaseExpiresAt)

	// Immediately claimable again.
	claimed, err := store.Claim(ctx, "w2", time.Minute)
	require.NoError(t, err)
	require.NotNil(t, claimed)
	assert.Equal(t, id, claimed.ID)
}

// TestStore_ReleaseExpired_ReapsCancelRequestedPendingRow pins A3's second
// bullet: a pending row still flagged cancel_requested -- the state left
// behind when a retryable failure races RequestCancel and lands the job
// back on pending before its worker's next Heartbeat could see the flag --
// is claimed by nobody (Claim's `AND NOT cancel_requested` predicate) and
// is reaped straight to StatusCanceled by the next ReleaseExpired sweep,
// counted in its returned total alongside ordinary lease recoveries.
// Without this reap such a row would sit forever: never claimable, but
// never actually resolved either.
func TestStore_ReleaseExpired_ReapsCancelRequestedPendingRow(t *testing.T) {
	requireDocker(t)
	truncateAll(t)
	store := New(testPool)
	ctx := context.Background()
	now := time.Now().UTC()

	id := newID()
	require.NoError(t, store.Create(ctx, newTranscription(id, now)))
	claimed, err := store.Claim(ctx, "w1", time.Minute)
	require.NoError(t, err)
	require.NotNil(t, claimed.LeaseToken)

	// RequestCancel while running only flags the row; the worker then loses
	// the race and schedules a retry instead of ever noticing the flag via
	// Heartbeat.
	_, err = store.RequestCancel(ctx, id)
	require.NoError(t, err)

	next := now.Add(-time.Minute) // already due
	require.NoError(t, store.Finish(ctx, domain.FinishRequest{
		ID: id, LeaseToken: *claimed.LeaseToken, Status: domain.StatusPending,
		Err: domain.New(domain.ErrCodeToolFailure, "transient"), NextAttemptAt: &next,
	}))

	got, err := store.Get(ctx, id)
	require.NoError(t, err)
	require.Equal(t, domain.StatusPending, got.Status, "precondition: back on pending with cancel_requested still set")

	// Due, but must never be claimed while cancel_requested is set.
	stillPending, err := store.Claim(ctx, "w2", time.Minute)
	require.NoError(t, err)
	assert.Nil(t, stillPending, "a cancel-requested pending row must never be claimed")

	n, err := store.ReleaseExpired(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, n, "ReleaseExpired must count the cancellation reap in its total")

	got, err = store.Get(ctx, id)
	require.NoError(t, err)
	assert.Equal(t, domain.StatusCanceled, got.Status)
	require.NotNil(t, got.CompletedAt)
	assert.Nil(t, got.LeaseOwner)
	assert.Nil(t, got.LeaseToken)

	claimedAgain, err := store.Claim(ctx, "w3", time.Minute)
	require.NoError(t, err)
	assert.Nil(t, claimedAgain, "a canceled job must never be claimable")
}

// TestStore_Finish_SucceededOnCancelRequestedRowRecordsCanceled pins A3's
// third bullet: a job the API already answered 200 to a cancel for must
// never be recorded succeeded, even when the worker's own outcome -- computed
// before it noticed the cancellation -- was a genuine success.
func TestStore_Finish_SucceededOnCancelRequestedRowRecordsCanceled(t *testing.T) {
	requireDocker(t)
	truncateAll(t)
	store := New(testPool)
	ctx := context.Background()
	now := time.Now().UTC()

	id := newID()
	require.NoError(t, store.Create(ctx, newTranscription(id, now)))
	claimed, err := store.Claim(ctx, "w1", time.Minute)
	require.NoError(t, err)
	require.NotNil(t, claimed.LeaseToken)

	_, err = store.RequestCancel(ctx, id)
	require.NoError(t, err)

	require.NoError(t, store.Finish(ctx, domain.FinishRequest{
		ID: id, LeaseToken: *claimed.LeaseToken, Status: domain.StatusSucceeded,
		Result: &domain.Result{Model: "base", Task: domain.TaskTranscribe},
	}))

	got, err := store.Get(ctx, id)
	require.NoError(t, err)
	assert.Equal(t, domain.StatusCanceled, got.Status, "a cancel-requested row must never be recorded succeeded")
	require.NotNil(t, got.CompletedAt)
}

// TestStore_RequestCancel_PendingMovesStraightToCanceled pins the
// no-worker-will-ever-pick-it-up shortcut for a still-pending job.
func TestStore_RequestCancel_PendingMovesStraightToCanceled(t *testing.T) {
	requireDocker(t)
	truncateAll(t)
	store := New(testPool)
	ctx := context.Background()
	now := time.Now().UTC()

	id := newID()
	require.NoError(t, store.Create(ctx, newTranscription(id, now)))

	got, err := store.RequestCancel(ctx, id)
	require.NoError(t, err)
	assert.Equal(t, domain.StatusCanceled, got.Status)
	require.NotNil(t, got.CompletedAt)

	claimed, err := store.Claim(ctx, "w1", time.Minute)
	require.NoError(t, err)
	assert.Nil(t, claimed, "a canceled job must never be claimable")
}

// TestStore_RequestCancel_TerminalIsConflict pins that a second
// RequestCancel (or one against any other terminal job) is ErrCodeConflict,
// not a silent no-op.
func TestStore_RequestCancel_TerminalIsConflict(t *testing.T) {
	requireDocker(t)
	truncateAll(t)
	store := New(testPool)
	ctx := context.Background()
	now := time.Now().UTC()

	id := newID()
	require.NoError(t, store.Create(ctx, newTranscription(id, now)))
	_, err := store.RequestCancel(ctx, id)
	require.NoError(t, err)

	_, err = store.RequestCancel(ctx, id)
	require.Error(t, err)
	assert.Equal(t, domain.ErrCodeConflict, domain.CodeOf(err))
}

// TestStore_RequestCancel_NotFound pins ErrCodeNotFound for an id that does
// not exist at all.
func TestStore_RequestCancel_NotFound(t *testing.T) {
	requireDocker(t)
	truncateAll(t)
	store := New(testPool)
	_, err := store.RequestCancel(context.Background(), newID())
	assert.Equal(t, domain.ErrCodeNotFound, domain.CodeOf(err))
}

// TestStore_RequestCancel_RunningLeavesWorkerToNoticeViaHeartbeat pins that
// a running job is left running by RequestCancel itself.
func TestStore_RequestCancel_RunningLeavesWorkerToNoticeViaHeartbeat(t *testing.T) {
	requireDocker(t)
	truncateAll(t)
	store := New(testPool)
	ctx := context.Background()
	now := time.Now().UTC()

	id := newID()
	require.NoError(t, store.Create(ctx, newTranscription(id, now)))
	claimed, err := store.Claim(ctx, "w1", time.Minute)
	require.NoError(t, err)
	require.NotNil(t, claimed.LeaseToken)

	got, err := store.RequestCancel(ctx, id)
	require.NoError(t, err)
	assert.Equal(t, domain.StatusRunning, got.Status, "RequestCancel must leave a running job running")

	canceled, err := store.Heartbeat(ctx, id, *claimed.LeaseToken, time.Minute)
	require.NoError(t, err)
	assert.True(t, canceled)
}

// TestStore_Requeue_FromFailedToPending pins the manual-retry transition.
func TestStore_Requeue_FromFailedToPending(t *testing.T) {
	requireDocker(t)
	truncateAll(t)
	store := New(testPool)
	ctx := context.Background()
	now := time.Now().UTC()

	id := newID()
	require.NoError(t, store.Create(ctx, newTranscription(id, now)))
	claimed, err := store.Claim(ctx, "w1", time.Minute)
	require.NoError(t, err)
	require.NotNil(t, claimed.LeaseToken)
	require.NoError(t, store.Finish(ctx, domain.FinishRequest{
		ID: id, LeaseToken: *claimed.LeaseToken, Status: domain.StatusFailed,
		Err: domain.New(domain.ErrCodeToolFailure, "boom"),
	}))

	got, err := store.Requeue(ctx, id)
	require.NoError(t, err)
	assert.Equal(t, domain.StatusPending, got.Status)
	assert.Equal(t, 0, got.Attempts)
	assert.Nil(t, got.Error)
	assert.Nil(t, got.LeaseOwner)
	assert.Nil(t, got.LeaseToken)
	assert.Nil(t, got.StartedAt, "Requeue must clear StartedAt: the job has not started again yet")
	assert.Nil(t, got.CompletedAt, "Requeue must clear CompletedAt: the job is pending, not terminal")

	claimed, err = store.Claim(ctx, "w2", time.Minute)
	require.NoError(t, err)
	require.NotNil(t, claimed)
	assert.Equal(t, id, claimed.ID)
}

// TestStore_Requeue_OnlyFromFailedOrCanceled pins ErrCodeConflict from
// every other status.
func TestStore_Requeue_OnlyFromFailedOrCanceled(t *testing.T) {
	requireDocker(t)
	truncateAll(t)
	store := New(testPool)
	ctx := context.Background()
	now := time.Now().UTC()

	pendingID := newID()
	require.NoError(t, store.Create(ctx, newTranscription(pendingID, now)))
	_, err := store.Requeue(ctx, pendingID)
	require.Error(t, err)
	assert.Equal(t, domain.ErrCodeConflict, domain.CodeOf(err))

	runningID := newID()
	require.NoError(t, store.Create(ctx, newTranscription(runningID, now)))
	_, err = store.Claim(ctx, "w1", time.Minute)
	require.NoError(t, err)
	_, err = store.Requeue(ctx, runningID)
	require.Error(t, err)
	assert.Equal(t, domain.ErrCodeConflict, domain.CodeOf(err))

	canceledID := newID()
	require.NoError(t, store.Create(ctx, newTranscription(canceledID, now)))
	_, err = store.RequestCancel(ctx, canceledID)
	require.NoError(t, err)
	got, err := store.Requeue(ctx, canceledID)
	require.NoError(t, err)
	assert.Equal(t, domain.StatusPending, got.Status)
}

// TestStore_Requeue_NotFound pins ErrCodeNotFound for an id that does not
// exist at all.
func TestStore_Requeue_NotFound(t *testing.T) {
	requireDocker(t)
	truncateAll(t)
	store := New(testPool)
	_, err := store.Requeue(context.Background(), newID())
	assert.Equal(t, domain.ErrCodeNotFound, domain.CodeOf(err))
}

// TestStore_Finish_SucceededOnCancelRequestedRowNullsResultAndSetsCanceledError
// pins the other half of the succeeded->canceled downgrade (FIX 5): the row
// must not merely change status while still carrying the discarded
// success's result. Before this fix, GET /{id} could answer
// {"status":"canceled","result":{...}} for a row GET /{id}/transcript
// still 409s (that handler only serves StatusSucceeded) -- a persisted row
// that lies about what happened by omission. The discarded success's (nil)
// error is also replaced with something that actually explains the
// canceled status, rather than leaving the column empty.
func TestStore_Finish_SucceededOnCancelRequestedRowNullsResultAndSetsCanceledError(t *testing.T) {
	requireDocker(t)
	truncateAll(t)
	store := New(testPool)
	ctx := context.Background()
	now := time.Now().UTC()

	id := newID()
	require.NoError(t, store.Create(ctx, newTranscription(id, now)))
	claimed, err := store.Claim(ctx, "w1", time.Minute)
	require.NoError(t, err)
	require.NotNil(t, claimed.LeaseToken)

	_, err = store.RequestCancel(ctx, id)
	require.NoError(t, err)

	result := &domain.Result{Text: "must never be served", Language: "en", Model: "base", Task: domain.TaskTranscribe}
	require.NoError(t, store.Finish(ctx, domain.FinishRequest{
		ID: id, LeaseToken: *claimed.LeaseToken, Status: domain.StatusSucceeded, Result: result,
	}))

	got, err := store.Get(ctx, id)
	require.NoError(t, err)
	assert.Equal(t, domain.StatusCanceled, got.Status)
	assert.Nil(t, got.Result, "a canceled row must never carry the discarded success's result")
	require.NotNil(t, got.Error, "the discarded success's (nil) error must be replaced with one that explains the canceled status")
	assert.Equal(t, domain.ErrCodeCanceled, got.Error.Code)
	require.NotNil(t, got.CompletedAt)
}

// TestStore_ReleaseForShutdown_GivesBackClaimWithoutConsumingAttempt pins
// FIX 1's whole point: a job on its LAST allowed attempt that happens to be
// running when a process stops for an ordinary reason (not a genuine
// failure) must not come out of that stop permanently failed. Giving the
// claim back via ReleaseForShutdown must leave Attempts exactly as if this
// claim had never happened, not merely unclaim the row with the attempt
// already burned.
func TestStore_ReleaseForShutdown_GivesBackClaimWithoutConsumingAttempt(t *testing.T) {
	requireDocker(t)
	truncateAll(t)
	store := New(testPool)
	ctx := context.Background()
	now := time.Now().UTC()

	id := newID()
	require.NoError(t, store.Create(ctx, newTranscriptionWithMaxAttempts(id, now, 1)))
	claimed, err := store.Claim(ctx, "w1", time.Minute)
	require.NoError(t, err)
	require.NotNil(t, claimed.LeaseToken)
	require.Equal(t, 1, claimed.Attempts, "the single allowed attempt was just consumed by Claim")

	require.NoError(t, store.ReleaseForShutdown(ctx, id, *claimed.LeaseToken))

	got, err := store.Get(ctx, id)
	require.NoError(t, err)
	assert.Equal(t, domain.StatusPending, got.Status)
	assert.Equal(t, 0, got.Attempts, "ReleaseForShutdown must give the attempt back, not merely leave the row unclaimed")
	assert.Nil(t, got.LeaseOwner)
	assert.Nil(t, got.LeaseToken)
	assert.Nil(t, got.LeaseExpiresAt)

	// Immediately claimable again, exactly as if this were the very first
	// claim -- and, once THIS attempt is genuinely spent (left running past
	// its lease, rather than released for shutdown), lease expiry must
	// still fail it. That is what proves the attempt was truly given back
	// rather than merely relabelled.
	reclaimed, err := store.Claim(ctx, "w2", time.Millisecond)
	require.NoError(t, err)
	require.NotNil(t, reclaimed)
	assert.Equal(t, 1, reclaimed.Attempts, "the attempt count must be exactly as if this were the first claim")

	waitForLeaseExpiry(t)
	n, err := store.ReleaseExpired(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, n)

	got, err = store.Get(ctx, id)
	require.NoError(t, err)
	assert.Equal(t, domain.StatusFailed, got.Status, "once the (real) attempt is genuinely spent, lease expiry must still fail it")
}

// TestStore_ReleaseForShutdown_StaleLeaseIsConflict pins the lease guard:
// exactly like Heartbeat and Finish, a caller presenting a token that no
// longer matches the row's current claim must not be able to touch it --
// including a token that WAS valid until a second claim (e.g. this same
// worker's own re-claim after a database blip) replaced it.
func TestStore_ReleaseForShutdown_StaleLeaseIsConflict(t *testing.T) {
	requireDocker(t)
	truncateAll(t)
	store := New(testPool)
	ctx := context.Background()
	now := time.Now().UTC()

	id := newID()
	require.NoError(t, store.Create(ctx, newTranscription(id, now)))
	staleClaim, err := store.Claim(ctx, "w1", time.Millisecond)
	require.NoError(t, err)
	require.NotNil(t, staleClaim.LeaseToken)
	staleToken := *staleClaim.LeaseToken

	waitForLeaseExpiry(t)
	n, err := store.ReleaseExpired(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, n)

	claimed, err := store.Claim(ctx, "w2", time.Minute)
	require.NoError(t, err)
	require.NotNil(t, claimed)

	err = store.ReleaseForShutdown(ctx, id, staleToken)
	require.Error(t, err)
	assert.Equal(t, domain.ErrCodeConflict, domain.CodeOf(err))

	got, err := store.Get(ctx, id)
	require.NoError(t, err)
	assert.Equal(t, domain.StatusRunning, got.Status, "a stale ReleaseForShutdown must not touch the row a second claim now owns")
}

// waitForLeaseExpiry sleeps long enough for a lease acquired with
// time.Millisecond to have lapsed, with margin for scheduling jitter.
func waitForLeaseExpiry(t *testing.T) {
	t.Helper()
	time.Sleep(50 * time.Millisecond)
}

// TestStore_ReleaseExpired_FailsJobWithNoAttemptsLeft pins the other half of
// lease recovery. A worker that dies mid-job has already burned an attempt,
// because Claim incremented it on the way in. If ReleaseExpired returned
// such a row to pending unconditionally, a worker that crashes on one
// specific input would loop forever -- claim, die, expire, requeue -- with
// attempts climbing and nothing ever retiring the job. Once the attempts are
// spent, recovery must mean "fail it", not "try again".
func TestStore_ReleaseExpired_FailsJobWithNoAttemptsLeft(t *testing.T) {
	requireDocker(t)
	truncateAll(t)
	store := New(testPool)
	ctx := context.Background()
	now := time.Now().UTC()

	// One attempt allowed, and the single claim below consumes it.
	id := newID()
	require.NoError(t, store.Create(ctx, newTranscriptionWithMaxAttempts(id, now, 1)))

	claimed, err := store.Claim(ctx, "doomed-worker", time.Millisecond)
	require.NoError(t, err)
	require.NotNil(t, claimed)
	require.Equal(t, 1, claimed.Attempts)

	waitForLeaseExpiry(t)
	n, err := store.ReleaseExpired(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, n, "the expired row must still be counted as recovered")

	got, err := store.Get(ctx, id)
	require.NoError(t, err)
	assert.Equal(t, domain.StatusFailed, got.Status,
		"a lease expiry with no attempts left must fail the job, not requeue it")
	require.NotNil(t, got.CompletedAt, "a failed job must carry completed_at")
	require.NotNil(t, got.Error, "a job failed by lease expiry must say why")
	assert.Equal(t, domain.ErrCodeTimeout, got.Error.Code)
	assert.Nil(t, got.LeaseOwner, "the dead worker's lease must be cleared")

	// And it must be genuinely off the queue, not merely relabelled.
	next, err := store.Claim(ctx, "w2", time.Minute)
	require.NoError(t, err)
	assert.Nil(t, next, "a failed job must not be claimable again")
}

// TestStore_ReleaseExpired_RequeuesJobWithAttemptsLeft is the companion
// case: the same expiry, but with attempts to spare, still goes back to
// pending so another worker picks it up.
func TestStore_ReleaseExpired_RequeuesJobWithAttemptsLeft(t *testing.T) {
	requireDocker(t)
	truncateAll(t)
	store := New(testPool)
	ctx := context.Background()
	now := time.Now().UTC()

	id := newID()
	require.NoError(t, store.Create(ctx, newTranscriptionWithMaxAttempts(id, now, 3)))

	_, err := store.Claim(ctx, "doomed-worker", time.Millisecond)
	require.NoError(t, err)

	waitForLeaseExpiry(t)
	n, err := store.ReleaseExpired(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, n)

	got, err := store.Get(ctx, id)
	require.NoError(t, err)
	assert.Equal(t, domain.StatusPending, got.Status)
	assert.Nil(t, got.Error, "a requeued job must not be given a failure error")

	next, err := store.Claim(ctx, "w2", time.Minute)
	require.NoError(t, err)
	require.NotNil(t, next, "a requeued job must be claimable again")
	assert.Equal(t, 2, next.Attempts, "the second claim consumes the second attempt")
}
