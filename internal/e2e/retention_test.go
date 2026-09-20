package e2e

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pablofornell/helios-audio-transcription-service/cmd/helios-transcription-api/wire"
	"github.com/pablofornell/helios-audio-transcription-service/internal/domain"
)

// retentionSweepInterval is what keeps this test fast: short enough that
// the sweeper notices a backdated row within a couple of ticks.
// retentionMaxAge is real wall-clock seconds, not this package's usual
// tens-of-milliseconds worker knobs, since the "aged" job below is made
// eligible by backdating its completed_at directly in Postgres (there is
// no HTTP knob to submit a job "into the past"), not by waiting MaxAge
// itself out -- it only has to stay comfortably longer than this whole
// test takes to run.
const (
	retentionMaxAge        = 30 * time.Second
	retentionSweepInterval = 50 * time.Millisecond
)

// TestRetentionSweepPurgesAgedTranscriptionsOnly runs a real Sweeper
// against real Postgres rows and a real localfs.Store, and proves it
// reclaims both halves of a purged transcription -- the database row (via
// app.Queue.PurgeTerminalBefore) and its transcriptions/<id> directory on
// disk (via app.Store.RemoveDir) -- while leaving alone both a
// terminal-but-too-young row and a non-terminal one regardless of age, per
// app.Queue.PurgeTerminalBefore's own contract.
//
// The worker pool is deliberately never started for this test: aged and
// fresh are driven to succeeded by claiming and finishing them directly
// through app.Queue (repoOf(st) satisfies it -- the same interface
// jobs.Pool itself drives), exactly the shape a real worker's own
// Claim/Finish calls take. This is what lets nonTerminal's assertion below
// be a hard, non-flaky "it is still pending" instead of a race against a
// real pool that might legitimately finish it before this test gets a
// chance to observe otherwise.
func TestRetentionSweepPurgesAgedTranscriptionsOnly(t *testing.T) {
	requireDocker(t)
	requireFFmpeg(t)

	cfg := newTestConfig(t)
	cfg.Retention.Enabled = true
	cfg.Retention.MaxAge = retentionMaxAge
	cfg.Retention.Interval = retentionSweepInterval
	require.NoError(t, cfg.Validate())

	st := newRawStack(t, cfg, nil) // pool never started -- see doc above.
	base := serveStack(t, st)
	ctx := context.Background()

	aged := submitFile(t, base, fixtureGoodMP3, "audio/mpeg", nil)
	fresh := submitFile(t, base, fixtureGoodMP3, "audio/mpeg", nil)
	nonTerminal := submitFile(t, base, fixtureGoodMP3, "audio/mpeg", nil)

	finishAsSucceeded(t, ctx, st, aged.ID, fresh.ID)

	agedDir := filepath.Join(cfg.Storage.RootDir, "transcriptions", aged.ID)
	_, err := os.Stat(agedDir)
	require.NoError(t, err, "expected the aged job's directory to exist on disk before the sweep")
	freshDir := filepath.Join(cfg.Storage.RootDir, "transcriptions", fresh.ID)

	_, err = testPool.Exec(ctx,
		`UPDATE transcriptions SET completed_at = now() - interval '1 hour' WHERE id = $1`, aged.ID)
	require.NoError(t, err, "backdate the aged job's completed_at")

	startSweeperOnly(t, st)

	require.Eventually(t, func() bool {
		resp := doJSON(t, http.MethodGet, base+"/api/v1/transcriptions/"+aged.ID, nil)
		resp.Body.Close()
		return resp.StatusCode == http.StatusNotFound
	}, 5*time.Second, retentionSweepInterval, "expected the aged job's row to be purged")

	require.Eventually(t, func() bool {
		_, statErr := os.Stat(agedDir)
		return os.IsNotExist(statErr)
	}, 5*time.Second, retentionSweepInterval, "expected the aged job's directory to be purged from disk")

	freshStill := getTranscription(t, base, fresh.ID)
	assert.Equal(t, domain.StatusSucceeded, freshStill.Status, "a fresh terminal job must survive the sweep")
	_, err = os.Stat(freshDir)
	assert.NoError(t, err, "a fresh terminal job's directory must survive the sweep")

	// Give the sweeper several more ticks before the final check: it must
	// never purge a non-terminal row, no matter how many times it runs.
	time.Sleep(5 * retentionSweepInterval)
	nonTerminalStill := getTranscription(t, base, nonTerminal.ID)
	assert.Equal(t, domain.StatusPending, nonTerminalStill.Status,
		"a non-terminal job must survive the sweep regardless of age -- and, since this test's pool was never started, "+
			"it can only still be pending, never anything else")
	_, err = os.Stat(filepath.Join(cfg.Storage.RootDir, "transcriptions", nonTerminal.ID))
	assert.NoError(t, err, "a non-terminal job's directory must survive the sweep")
}

// finishAsSucceeded claims exactly len(wantIDs) rows directly through
// repoOf(st) (app.Queue) -- the same Claim/Finish calls a real worker makes
// -- and finishes each as succeeded with a minimal but real Result,
// asserting every claimed row's id is one of wantIDs (failing loudly,
// rather than silently mis-targeting, if Claim's own `ORDER BY
// next_attempt_at, created_at` ever picked something else).
func finishAsSucceeded(t *testing.T, ctx context.Context, st *wire.Stack, wantIDs ...string) {
	t.Helper()
	want := make(map[string]bool, len(wantIDs))
	for _, id := range wantIDs {
		want[id] = true
	}
	repo := repoOf(st)

	for i := 0; i < len(wantIDs); i++ {
		claimed, err := repo.Claim(ctx, "retention-test-owner", time.Minute)
		require.NoError(t, err)
		require.NotNilf(t, claimed, "expected a claimable row (%d of %d claimed so far)", i, len(wantIDs))
		require.Truef(t, want[claimed.ID], "Claim picked %s, which is not one of the rows this test meant to finish -- "+
			"either the claim order assumption (submission order) is wrong, or a prior claim in this loop mistargeted", claimed.ID)

		require.NotNil(t, claimed.LeaseToken, "Claim must mint a lease token")
		err = repo.Finish(ctx, domain.FinishRequest{
			ID:         claimed.ID,
			LeaseToken: *claimed.LeaseToken,
			Status:     domain.StatusSucceeded,
			Result: &domain.Result{
				Text:     "retention fixture result",
				Language: "en",
				Model:    claimed.Options.Model,
				Task:     claimed.Options.Task,
				Segments: []domain.Segment{{ID: 0, Start: 0, End: 1, Text: "retention fixture result"}},
			},
		})
		require.NoError(t, err)
	}
}
