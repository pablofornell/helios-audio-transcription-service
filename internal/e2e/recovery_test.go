package e2e

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pablofornell/helios-audio-transcription-service/internal/domain"
)

// TestCrashedWorkerRecovery simulates a worker that claimed a job and then
// died before ever heartbeating or finishing it: this test claims the row
// itself, directly through app.Queue (see stack.repo, which satisfies both
// app.Repository and app.Queue), under an owner identity distinct from the
// real pool's and with a lease so short it is already expired by the time
// the real pool starts. It then starts that pool and asserts its own
// periodic Queue.ReleaseExpired sweep (see jobs.Pool.sweepLoop) recovers
// the abandoned row to pending and it runs to completion -- proving lease
// recovery end to end, not just that ReleaseExpired's SQL is correct in
// isolation (internal/postgres already covers that).
func TestCrashedWorkerRecovery(t *testing.T) {
	requireDocker(t)
	requireFFmpeg(t)

	cfg := newTestConfig(t)
	st := newRawStack(t, cfg, nil) // pool not started yet -- see newRawStack.s doc.
	base := serveStack(t, st)

	created := submitFile(t, base, fixtureGoodMP3, "audio/mpeg", nil)

	// Simulate a different worker claiming the row and then crashing: claim
	// it directly with a lease so short it is already expired by the time
	// this call returns.
	ctx := context.Background()
	claimed, err := repoOf(st).Claim(ctx, "crashed-worker-owner", time.Nanosecond)
	require.NoError(t, err)
	require.NotNil(t, claimed, "expected the freshly submitted row to be claimable")
	require.Equal(t, created.ID, claimed.ID)
	require.Equal(t, domain.StatusRunning, claimed.Status)
	require.Equal(t, 1, claimed.Attempts, "Claim always increments Attempts")

	// Confirm over the API that it is genuinely stuck running under a lease
	// nobody will ever heartbeat -- this is the exact state a worker
	// crashing mid-job leaves behind.
	stuck := getTranscription(t, base, created.ID)
	assert.Equal(t, domain.StatusRunning, stuck.Status)

	// Now start the real pool (and, since retention is disabled in this
	// test's config, only the pool -- see wire.Stack.Start's own doc). Its
	// sweepLoop runs every Config.LeaseDuration (see jobs.Pool's own doc)
	// and will notice the lease above is already expired, return the row
	// to pending, and its claimLoop will then claim and run it normally.
	st.Start()

	final := waitForStatus(t, base, created.ID, domain.StatusSucceeded, terminalDeadline)
	require.NotNil(t, final.Result)
	// Attempts reflects BOTH claims: the crashed worker's (1) and the real
	// pool's own subsequent one after recovery (2) -- proof the recovery
	// path, not some other route, is what actually finished this job.
	assert.Equal(t, 2, final.Attempts,
		"expected exactly two claims: the simulated crashed worker's, and the real pool's after ReleaseExpired recovered it")
}

// TestConcurrency_MultipleJobsAllReachTerminal submits several jobs at once
// against a pool whose Concurrency is fixed at 2 (see newTestConfig) and
// asserts every one of them reaches a terminal state -- proving the pool
// actually processes a backlog larger than its own concurrency limit,
// rather than only ever being exercised one job at a time by every other
// test in this package.
func TestConcurrency_MultipleJobsAllReachTerminal(t *testing.T) {
	requireDocker(t)
	requireFFmpeg(t)

	ts := newTestServer(t)

	const n = 6
	ids := make([]string, n)
	for i := 0; i < n; i++ {
		created := submitFile(t, ts.base, fixtureGoodMP3, "audio/mpeg", nil)
		ids[i] = created.ID
	}

	deadline := time.Now().Add(20 * time.Second)
	remaining := make(map[string]bool, n)
	for _, id := range ids {
		remaining[id] = true
	}
	for len(remaining) > 0 && time.Now().Before(deadline) {
		for id := range remaining {
			tr := getTranscription(t, ts.base, id)
			if terminalStatuses[tr.Status] {
				delete(remaining, id)
			}
		}
		if len(remaining) > 0 {
			time.Sleep(20 * time.Millisecond)
		}
	}
	require.Empty(t, remaining, "expected every submitted job to reach a terminal state within the deadline")

	for _, id := range ids {
		tr := getTranscription(t, ts.base, id)
		assert.Equal(t, domain.StatusSucceeded, tr.Status, "expected every job against the real success fixture to succeed")
	}
}
