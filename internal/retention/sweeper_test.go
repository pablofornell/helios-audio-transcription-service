package retention

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pablofornell/helios-audio-transcription-service/internal/domain"
)

// ---------------------------------------------------------------------------
// test harness
// ---------------------------------------------------------------------------

type harness struct {
	sweeper *Sweeper
	queue   *fakeQueue
	store   *fakeStore
	clock   *fakeClock
}

func testConfig() Config {
	return Config{
		MaxAge:   7 * 24 * time.Hour,
		Interval: 10 * time.Millisecond,
	}
}

func newHarness(cfg Config) *harness {
	queue := newFakeQueue()
	store := newFakeStore()
	clock := newFakeClock(time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC))
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	sweeper := New(cfg, Deps{
		Queue:  queue,
		Store:  store,
		Clock:  clock,
		Logger: logger,
	})
	return &harness{sweeper: sweeper, queue: queue, store: store, clock: clock}
}

func terminal(id string) domain.PurgedTranscription {
	return domain.PurgedTranscription{ID: id}
}

// ---------------------------------------------------------------------------
// sweepOnce
// ---------------------------------------------------------------------------

// singleBatchPurgeFn returns batch once, then (nil, nil) on every later call
// -- modelling a real Queue.PurgeTerminalBefore whose backlog is smaller
// than its own internal batch limit, so a second call finds nothing left
// due. Most of this file's tests only care about one batch's worth of
// behaviour; see TestSweeper_SweepOnce_LoopsAcrossMultipleBatchesUntilCaughtUp
// and TestSweeper_SweepOnce_StopsAtMaxBatchesPerSweepCeiling for the tests
// that specifically exercise sweepOnce's multi-batch loop (see FIX 2).
func singleBatchPurgeFn(batch []domain.PurgedTranscription) func(context.Context, time.Time) ([]domain.PurgedTranscription, error) {
	served := false
	return func(ctx context.Context, cutoff time.Time) ([]domain.PurgedTranscription, error) {
		if served {
			return nil, nil
		}
		served = true
		return batch, nil
	}
}

// TestSweeper_PurgesAndRemovesDirectories proves the happy path: every
// transcription PurgeTerminalBefore reports has its directory removed via
// Store.RemoveDir, at the store-relative path CONTRACT.md section 5 fixes.
func TestSweeper_PurgesAndRemovesDirectories(t *testing.T) {
	h := newHarness(testConfig())
	h.queue.purgeFn = singleBatchPurgeFn([]domain.PurgedTranscription{terminal("id-1"), terminal("id-2")})

	h.sweeper.sweepOnce(context.Background())

	assert.ElementsMatch(t, []string{"transcriptions/id-1", "transcriptions/id-2"}, h.store.removedDirsSnapshot())
}

// TestSweeper_SweepOnce_LoopsAcrossMultipleBatchesUntilCaughtUp proves
// sweepOnce keeps calling Queue.PurgeTerminalBefore -- across as many
// batches as it takes -- until a call comes back empty, rather than
// stopping after the very first one. Without this, a deployment completing
// more terminal jobs per Config.Interval than one batch's worth would never
// catch up: its backlog, and the on-disk directories that go with it, would
// grow forever while Config.MaxAge was silently no longer honoured.
func TestSweeper_SweepOnce_LoopsAcrossMultipleBatchesUntilCaughtUp(t *testing.T) {
	h := newHarness(testConfig())
	var calls int
	h.queue.purgeFn = func(ctx context.Context, cutoff time.Time) ([]domain.PurgedTranscription, error) {
		calls++
		switch calls {
		case 1:
			return []domain.PurgedTranscription{terminal("id-1"), terminal("id-2")}, nil
		case 2:
			return []domain.PurgedTranscription{terminal("id-3")}, nil
		default:
			return nil, nil
		}
	}

	h.sweeper.sweepOnce(context.Background())

	assert.Equal(t, 3, h.queue.purgeCallCount(),
		"sweepOnce must keep calling PurgeTerminalBefore until a call returns nothing, not stop after the first batch")
	assert.ElementsMatch(t, []string{"transcriptions/id-1", "transcriptions/id-2", "transcriptions/id-3"}, h.store.removedDirsSnapshot())
}

// TestSweeper_SweepOnce_StopsAtMaxBatchesPerSweepCeiling proves the other
// half of FIX 2: when a backlog is so large that every single batch comes
// back full, sweepOnce still stops after maxBatchesPerSweep calls rather
// than running forever on one tick.
func TestSweeper_SweepOnce_StopsAtMaxBatchesPerSweepCeiling(t *testing.T) {
	h := newHarness(testConfig())
	h.queue.purgeFn = func(ctx context.Context, cutoff time.Time) ([]domain.PurgedTranscription, error) {
		return []domain.PurgedTranscription{terminal("id")}, nil // an inexhaustible backlog
	}

	h.sweeper.sweepOnce(context.Background())

	assert.Equal(t, maxBatchesPerSweep, h.queue.purgeCallCount(),
		"sweepOnce must stop after maxBatchesPerSweep batches even though more work is still due")
}

// TestSweeper_SweepOnce_StopsWhenCtxDone proves the loop is also bounded by
// ctx: an already-done context stops it before even the first batch.
func TestSweeper_SweepOnce_StopsWhenCtxDone(t *testing.T) {
	h := newHarness(testConfig())
	h.queue.purgeFn = func(ctx context.Context, cutoff time.Time) ([]domain.PurgedTranscription, error) {
		return []domain.PurgedTranscription{terminal("id")}, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	h.sweeper.sweepOnce(ctx)

	assert.Equal(t, 0, h.queue.purgeCallCount(), "an already-done ctx must stop the loop before any batch is attempted")
}

// TestSweeper_CutoffIsClockNowMinusMaxAge proves the cutoff passed to
// Queue.PurgeTerminalBefore is computed from the injected Clock, not real
// wall-clock time, so it is exactly Clock.Now() - Config.MaxAge.
func TestSweeper_CutoffIsClockNowMinusMaxAge(t *testing.T) {
	cfg := testConfig()
	h := newHarness(cfg)

	h.sweeper.sweepOnce(context.Background())

	want := h.clock.Now().Add(-cfg.MaxAge)
	assert.True(t, h.queue.lastCutoff().Equal(want), "cutoff = %s, want %s", h.queue.lastCutoff(), want)
}

// TestSweeper_NoCandidatesRemovesNothing proves an empty purge result never
// touches the store and never counts as a sweep worth logging loudly (no
// assertion on logging itself, but no RemoveDir call is the observable
// half of that).
func TestSweeper_NoCandidatesRemovesNothing(t *testing.T) {
	h := newHarness(testConfig())
	// h.queue.purgeFn left nil: the fake's default returns (nil, nil).

	h.sweeper.sweepOnce(context.Background())

	assert.Empty(t, h.store.removedDirsSnapshot())
}

// TestSweeper_PurgeErrorSkipsTheWholeSweep proves a failure to even list
// purge candidates is logged and simply retried at the next tick -- it
// never panics and never touches the store.
func TestSweeper_PurgeErrorSkipsTheWholeSweep(t *testing.T) {
	h := newHarness(testConfig())
	h.queue.purgeFn = func(ctx context.Context, cutoff time.Time) ([]domain.PurgedTranscription, error) {
		return nil, domain.New(domain.ErrCodeInternal, "database unreachable")
	}

	h.sweeper.sweepOnce(context.Background())

	assert.Empty(t, h.store.removedDirsSnapshot())
}

// TestSweeper_RemoveDirFailureDoesNotAbortSweep proves the central
// per-transcription failure guarantee this package's own doc promises: one
// candidate's Store.RemoveDir failure is logged and skipped -- its database
// row is already gone, there is nothing to roll back -- while every other
// candidate in the same batch still has its directory removed normally.
func TestSweeper_RemoveDirFailureDoesNotAbortSweep(t *testing.T) {
	h := newHarness(testConfig())
	h.queue.purgeFn = singleBatchPurgeFn([]domain.PurgedTranscription{terminal("failing"), terminal("ok")})
	h.store.removeDirErr["transcriptions/failing"] = domain.New(domain.ErrCodeInternal, "disk on fire")

	h.sweeper.sweepOnce(context.Background())

	got := h.store.removedDirsSnapshot()
	assert.Contains(t, got, "transcriptions/ok", "the other candidate in the same batch must still have its directory removed")
	// The failing directory was still attempted -- fakeStore records every
	// call, success or not -- but it is the only one that did not
	// "succeed" from Store's point of view; Sweeper itself does not retry
	// or otherwise re-attempt it.
	assert.Contains(t, got, "transcriptions/failing")
}

// ---------------------------------------------------------------------------
// Run
// ---------------------------------------------------------------------------

// TestSweeper_RunSweepsImmediatelyAtStartup proves Run's first sweep does
// not wait for Config.Interval to elapse: with an interval long enough that
// no tick could plausibly fire on its own within the test, a purge is still
// requested once, right away.
func TestSweeper_RunSweepsImmediatelyAtStartup(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		cfg := Config{MaxAge: 7 * 24 * time.Hour, Interval: time.Hour}
		h := newHarness(cfg)
		h.queue.purgeFn = singleBatchPurgeFn([]domain.PurgedTranscription{terminal("id-1")})

		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() {
			defer close(done)
			_ = h.sweeper.Run(ctx)
		}()

		synctest.Wait()
		// Not asserting an exact count: sweepOnce itself may call
		// PurgeTerminalBefore more than once per sweep now (see FIX 2's
		// multi-batch loop, and singleBatchPurgeFn's one confirming empty
		// call) -- what this test promises is that Run's startup sweep ran
		// at all, before waiting a full Config.Interval.
		assert.GreaterOrEqual(t, h.queue.purgeCallCount(), 1, "Run must sweep once at startup, before waiting a full interval")
		assert.Contains(t, h.store.removedDirsSnapshot(), "transcriptions/id-1")

		cancel()
		<-done
	})
}

// TestSweeper_RunTicksAtInterval proves the periodic half: after the
// startup sweep, Run keeps sweeping every Config.Interval until ctx is
// canceled -- it does not sweep exactly once and then stop.
func TestSweeper_RunTicksAtInterval(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		cfg := Config{MaxAge: 7 * 24 * time.Hour, Interval: 10 * time.Millisecond}
		h := newHarness(cfg)

		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() {
			defer close(done)
			_ = h.sweeper.Run(ctx)
		}()

		synctest.Wait()
		require.Equal(t, 1, h.queue.purgeCallCount())

		time.Sleep(cfg.Interval * 3)
		synctest.Wait()
		// Not asserting an exact count: time.Ticker only ever buffers one
		// pending tick, so how many of the (virtual) ticks fired during
		// this sleep get coalesced is a ticker implementation detail, not
		// part of Sweeper's contract. What Sweeper does promise is "more
		// than the one startup sweep" once Interval has elapsed.
		assert.GreaterOrEqual(t, h.queue.purgeCallCount(), 2, "Run must keep sweeping periodically, not just once at startup")

		cancel()
		<-done
	})
}

// TestSweeper_RunReturnsPromptlyOnCancel proves Run does not block past ctx
// being canceled waiting on anything -- no leaked ticker goroutine, no
// extra sweep after the fact.
func TestSweeper_RunReturnsPromptlyOnCancel(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(testConfig())

		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() {
			defer close(done)
			_ = h.sweeper.Run(ctx)
		}()

		synctest.Wait()
		cancel()
		synctest.Wait()

		select {
		case <-done:
		default:
			t.Fatal("Run did not return after ctx was canceled")
		}
	})
}
