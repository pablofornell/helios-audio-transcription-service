package jobs

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pablofornell/helios-audio-transcription-service/internal/domain"
	"github.com/pablofornell/helios-audio-transcription-service/internal/observability"
)

// ---------------------------------------------------------------------------
// test harness
// ---------------------------------------------------------------------------

type harness struct {
	pool        *Pool
	queue       *fakeQueue
	store       *fakeStore
	audioProc   *fakeAudioProcessor
	transcriber *fakeTranscriber
	clock       *fakeClock
	metrics     *observability.Metrics
}

// testConfig is a Config tuned for fast, deterministic tests: short
// intervals so a synctest bubble only needs to advance a handful of
// milliseconds of virtual time to exercise several ticks.
func testConfig() Config {
	return Config{
		Concurrency:       2,
		PollInterval:      10 * time.Millisecond,
		LeaseDuration:     300 * time.Millisecond,
		HeartbeatInterval: 30 * time.Millisecond,
		JobTimeout:        5 * time.Second,
		MaxAttempts:       3,
		BackoffBase:       time.Second,
		BackoffMax:        10 * time.Second,
	}
}

func newHarness(cfg Config) *harness {
	queue := newFakeQueue()
	store := newFakeStore()
	audioProc := newFakeAudioProcessor()
	transcriber := newFakeTranscriber()
	clock := newFakeClock(time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC))
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	metrics := observability.NewMetrics(prometheus.NewRegistry())

	pool := NewPool(cfg, Deps{
		Queue:       queue,
		Store:       store,
		Audio:       audioProc,
		Transcriber: transcriber,
		Clock:       clock,
		Logger:      logger,
		Metrics:     metrics,
	})
	return &harness{
		pool:        pool,
		queue:       queue,
		store:       store,
		audioProc:   audioProc,
		transcriber: transcriber,
		clock:       clock,
		metrics:     metrics,
	}
}

// firstClaimLeaseToken is the token fakeQueue.Claim deterministically mints
// for the very first successful Claim call made against a freshly built
// fakeQueue (see fakeQueue.tokenSeq): every test in this file that seeds
// exactly one job and lets it be claimed exactly once before asserting on
// wavRelPath can rely on this literal instead of threading the claimed
// token through its transcriber/store assertions.
const firstClaimLeaseToken = "lease-token-1"

// newTranscription builds a claimable domain.Transcription the way
// app.Service.Submit would have left it: pending, with a store-relative
// SourcePath and the upload-known half of Audio already filled in.
func newTranscription(id string, maxAttempts int) *domain.Transcription {
	return &domain.Transcription{
		ID:          id,
		Status:      domain.StatusPending,
		Options:     domain.Options{Model: "large-v3-turbo-q5_0", Task: domain.TaskTranscribe, BeamSize: 5},
		Audio:       domain.Audio{Filename: "source.mp3", MediaType: "audio/mpeg", SizeBytes: 1024},
		SourcePath:  fmt.Sprintf("transcriptions/%s/source.mp3", id),
		MaxAttempts: maxAttempts,
	}
}

// counterValue reads the current value of a label-less counter series off
// m's private registry. observability.Metrics deliberately does not expose
// its raw *prometheus.Counter fields, so tests outside that package assert
// against a scrape, exactly as a real Prometheus server would see it.
func counterValue(t *testing.T, m *observability.Metrics, name string, labels map[string]string) float64 {
	t.Helper()
	families, err := m.Registry().Gather()
	require.NoError(t, err)
	for _, f := range families {
		if f.GetName() != name {
			continue
		}
		for _, metric := range f.GetMetric() {
			if labelsMatch(metric, labels) {
				return metric.GetCounter().GetValue()
			}
		}
	}
	return 0
}

func gaugeValue(t *testing.T, m *observability.Metrics, name string) float64 {
	t.Helper()
	families, err := m.Registry().Gather()
	require.NoError(t, err)
	for _, f := range families {
		if f.GetName() != name {
			continue
		}
		require.Len(t, f.GetMetric(), 1)
		return f.GetMetric()[0].GetGauge().GetValue()
	}
	t.Fatalf("metric %s not found in scrape", name)
	return 0
}

func labelsMatch(metric *dto.Metric, want map[string]string) bool {
	got := make(map[string]string, len(metric.GetLabel()))
	for _, l := range metric.GetLabel() {
		got[l.GetName()] = l.GetValue()
	}
	for k, v := range want {
		if got[k] != v {
			return false
		}
	}
	return true
}

// ---------------------------------------------------------------------------
// 1. Happy path
// ---------------------------------------------------------------------------

// TestPool_HappyPath_Succeeds proves the full pipeline: claim, probe,
// normalize, transcribe, Finish(succeeded) with the probed Audio merged
// onto the upload-known fields and the Result the transcriber produced, and
// that the normalized WAV was removed afterward.
func TestPool_HappyPath_Succeeds(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(testConfig())
		id := "job-1"
		h.queue.seed(newTranscription(id, 3))

		h.transcriber.transcribeFn = func(ctx context.Context, req domain.TranscribeRequest) (*domain.Result, error) {
			assert.Equal(t, "/store/"+wavRelPath(id, firstClaimLeaseToken), req.AudioPath)
			assert.Equal(t, "large-v3-turbo-q5_0", req.Options.Model)
			return &domain.Result{Text: "hi there", Language: "en", Model: "large-v3-turbo-q5_0", Task: domain.TaskTranscribe, DurationSeconds: 12.5}, nil
		}

		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() { defer close(done); _ = h.pool.Run(ctx) }()

		synctest.Wait()

		require.Equal(t, 1, h.queue.totalFinishes())
		finishes := h.queue.finishesFor(id)
		require.Len(t, finishes, 1)
		got := finishes[0]

		assert.Equal(t, domain.StatusSucceeded, got.Status)
		require.NotNil(t, got.Result)
		assert.Equal(t, "hi there", got.Result.Text)

		require.NotNil(t, got.Audio)
		assert.Equal(t, "source.mp3", got.Audio.Filename, "upload-known field must be preserved")
		assert.Equal(t, "audio/mpeg", got.Audio.MediaType, "upload-known field must be preserved")
		assert.Equal(t, int64(1024), got.Audio.SizeBytes, "upload-known field must be preserved")
		assert.Equal(t, 12.5, got.Audio.DurationSeconds, "probed field must be merged in")
		assert.Equal(t, 44100, got.Audio.SampleRateHz, "probed field must be merged in")
		assert.Equal(t, 2, got.Audio.Channels, "probed field must be merged in")
		assert.Equal(t, "mp3", got.Audio.Codec, "probed field must be merged in")

		assert.Contains(t, h.store.removedPaths(), wavRelPath(id, firstClaimLeaseToken))

		assert.Equal(t, float64(1), counterValue(t, h.metrics, "helios_transcriptions_completed_total", map[string]string{"status": "succeeded"}))
		assert.Equal(t, float64(0), gaugeValue(t, h.metrics, "helios_jobs_in_flight"))

		cancel()
		<-done
	})
}

// ---------------------------------------------------------------------------
// 2. Non-retryable probe failure
// ---------------------------------------------------------------------------

// TestPool_ProbeFailure_NonRetryable_Fails proves a probe failure classified
// ErrCodeNoAudioStream (not Retryable) goes straight to StatusFailed with no
// scheduled retry, and never reaches the transcriber.
func TestPool_ProbeFailure_NonRetryable_Fails(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(testConfig())
		id := "job-2"
		h.queue.seed(newTranscription(id, 3))

		h.audioProc.probeFn = func(ctx context.Context, absPath string) (domain.Audio, error) {
			return domain.Audio{}, domain.New(domain.ErrCodeNoAudioStream, "file has no audio stream")
		}

		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() { defer close(done); _ = h.pool.Run(ctx) }()

		synctest.Wait()

		finishes := h.queue.finishesFor(id)
		require.Len(t, finishes, 1)
		got := finishes[0]
		assert.Equal(t, domain.StatusFailed, got.Status)
		require.NotNil(t, got.Err)
		assert.Equal(t, domain.ErrCodeNoAudioStream, got.Err.Code)
		assert.Nil(t, got.NextAttemptAt)
		assert.Equal(t, 0, h.transcriber.callCount(), "a probe failure must never reach the transcriber")
		assert.Nil(t, got.Audio, "Audio is not merged when Probe itself fails")

		cancel()
		<-done
	})
}

// ---------------------------------------------------------------------------
// 3. Retryable transcribe failure, attempts remaining
// ---------------------------------------------------------------------------

// TestPool_TranscribeFailure_Retryable_SchedulesRetry proves a retryable
// failure (ErrCodeToolFailure) with attempts still under MaxAttempts is
// scheduled for retry with a NextAttemptAt computed by Pool.backoff, and
// that the normalized WAV is removed even though the job failed.
func TestPool_TranscribeFailure_Retryable_SchedulesRetry(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		cfg := testConfig()
		h := newHarness(cfg)
		h.pool.rand = fixedRand{frac: 1} // deterministic: backoff(1) == cfg.BackoffBase exactly

		id := "job-3"
		h.queue.seed(newTranscription(id, 3))
		h.transcriber.transcribeFn = func(ctx context.Context, req domain.TranscribeRequest) (*domain.Result, error) {
			return nil, domain.New(domain.ErrCodeToolFailure, "whisper-cli exited 1").WithDetails("stderr", "boom")
		}

		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() { defer close(done); _ = h.pool.Run(ctx) }()

		synctest.Wait()

		finishes := h.queue.finishesFor(id)
		require.Len(t, finishes, 1)
		got := finishes[0]
		assert.Equal(t, domain.StatusPending, got.Status)
		require.NotNil(t, got.Err)
		assert.Equal(t, domain.ErrCodeToolFailure, got.Err.Code)
		require.NotNil(t, got.NextAttemptAt)
		want := h.clock.Now().Add(cfg.BackoffBase)
		assert.True(t, got.NextAttemptAt.Equal(want), "NextAttemptAt = %s, want %s", got.NextAttemptAt, want)

		assert.Contains(t, h.store.removedPaths(), wavRelPath(id, firstClaimLeaseToken), "the WAV must be removed even though the job failed")
		assert.Equal(t, float64(0), counterValue(t, h.metrics, "helios_transcriptions_completed_total", map[string]string{"status": "pending"}),
			"a scheduled retry is not a terminal outcome and must not be counted as completed")

		cancel()
		<-done
	})
}

// ---------------------------------------------------------------------------
// 4. Retryable failure, attempts exhausted
// ---------------------------------------------------------------------------

// TestPool_TranscribeFailure_AttemptsExhausted_Fails proves the same
// retryable failure permanently fails the job once Attempts reaches
// MaxAttempts.
func TestPool_TranscribeFailure_AttemptsExhausted_Fails(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(testConfig())
		id := "job-4"
		h.queue.seed(newTranscription(id, 1)) // MaxAttempts 1: exhausted on the very first failure

		h.transcriber.transcribeFn = func(ctx context.Context, req domain.TranscribeRequest) (*domain.Result, error) {
			return nil, domain.New(domain.ErrCodeToolFailure, "whisper-cli exited 1")
		}

		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() { defer close(done); _ = h.pool.Run(ctx) }()

		synctest.Wait()

		finishes := h.queue.finishesFor(id)
		require.Len(t, finishes, 1)
		got := finishes[0]
		assert.Equal(t, domain.StatusFailed, got.Status)
		assert.Nil(t, got.NextAttemptAt)
		require.NotNil(t, got.Err)
		assert.Equal(t, domain.ErrCodeToolFailure, got.Err.Code)

		cancel()
		<-done
	})
}

// ---------------------------------------------------------------------------
// 5. Heartbeat-driven cancellation
// ---------------------------------------------------------------------------

// TestPool_HeartbeatCanceled_FinishesCanceled proves a heartbeat reporting
// canceled == true cancels the job context, interrupting whatever step is
// in progress, and the job is Finished as StatusCanceled — never as failed,
// never scheduled for retry.
func TestPool_HeartbeatCanceled_FinishesCanceled(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		cfg := testConfig()
		h := newHarness(cfg)
		id := "job-5"
		h.queue.seed(newTranscription(id, 3))

		interrupted := make(chan struct{})
		h.transcriber.transcribeFn = func(ctx context.Context, req domain.TranscribeRequest) (*domain.Result, error) {
			<-ctx.Done()
			close(interrupted)
			return nil, ctx.Err()
		}
		h.queue.heartbeatSeq[id] = []hbResult{{canceled: true}}

		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() { defer close(done); _ = h.pool.Run(ctx) }()

		synctest.Wait()
		time.Sleep(cfg.HeartbeatInterval + time.Millisecond)
		synctest.Wait()

		select {
		case <-interrupted:
		default:
			t.Fatal("the transcriber must have been interrupted by the canceled job context")
		}

		finishes := h.queue.finishesFor(id)
		require.Len(t, finishes, 1)
		got := finishes[0]
		assert.Equal(t, domain.StatusCanceled, got.Status)
		require.NotNil(t, got.Err)
		assert.Equal(t, domain.ErrCodeCanceled, got.Err.Code)
		assert.Equal(t, float64(1), counterValue(t, h.metrics, "helios_transcriptions_completed_total", map[string]string{"status": "canceled"}))

		cancel()
		<-done
	})
}

// ---------------------------------------------------------------------------
// 6. Lost lease
// ---------------------------------------------------------------------------

// TestPool_HeartbeatConflict_AbandonsWithoutFinish proves a heartbeat
// reporting a lost lease (ErrCodeConflict) cancels the job context and
// abandons the job WITHOUT ever calling Queue.Finish — the row belongs to
// whoever holds the lease now, and this worker must not touch it again.
func TestPool_HeartbeatConflict_AbandonsWithoutFinish(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		cfg := testConfig()
		h := newHarness(cfg)
		id := "job-6"
		h.queue.seed(newTranscription(id, 3))

		h.transcriber.transcribeFn = func(ctx context.Context, req domain.TranscribeRequest) (*domain.Result, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		}
		h.queue.heartbeatSeq[id] = []hbResult{{err: domain.New(domain.ErrCodeConflict, "lease no longer held")}}

		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() { defer close(done); _ = h.pool.Run(ctx) }()

		synctest.Wait()
		time.Sleep(cfg.HeartbeatInterval + time.Millisecond)
		synctest.Wait()

		assert.Equal(t, 0, h.queue.totalFinishes(), "a job that lost its lease must never be Finished")

		cancel()
		<-done
	})
}

// TestPool_HeartbeatConflict_DoesNotRemoveWAV proves the other half of
// finding 4: a worker that discovers its lease is lost must not remove the
// normalized WAV at all, because — now that the WAV path is per-claim (see
// wavRelPath) — a subsequent attempt, potentially already writing to a path
// this worker itself once used, must never have its file unlinked out from
// under it by a worker that no longer owns this row.
func TestPool_HeartbeatConflict_DoesNotRemoveWAV(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		cfg := testConfig()
		h := newHarness(cfg)
		id := "job-7"
		h.queue.seed(newTranscription(id, 3))

		h.transcriber.transcribeFn = func(ctx context.Context, req domain.TranscribeRequest) (*domain.Result, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		}
		h.queue.heartbeatSeq[id] = []hbResult{{err: domain.New(domain.ErrCodeConflict, "lease no longer held")}}

		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() { defer close(done); _ = h.pool.Run(ctx) }()

		synctest.Wait()
		time.Sleep(cfg.HeartbeatInterval + time.Millisecond)
		synctest.Wait()

		assert.NotContains(t, h.store.removedPaths(), wavRelPath(id, firstClaimLeaseToken),
			"a worker that has lost its lease must never remove the normalized WAV")

		cancel()
		<-done
	})
}

// TestPool_WAVPath_DoesNotCollideAcrossManualRetry pins FIX 3 directly: a
// manual retry -- Store.Requeue resets Attempts to 0 (see
// postgres/repository.go's own doc) -- can land its very next claim on the
// SAME Attempts number an earlier claim of the same job already used. An
// Attempts-keyed WAV path (the old wavRelPath(id, attempt)) would have
// collided here; a worker still cleaning up (or a subsequent attempt still
// writing) under that shared path could then unlink a file it does not
// own. Keying on the per-claim lease token instead -- a fresh UUID every
// single Claim mints, requeue or not -- must keep the two paths distinct
// even though both claims share Attempts == 1.
func TestPool_WAVPath_DoesNotCollideAcrossManualRetry(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(testConfig())
		id := "job-retry-collision"
		h.queue.seed(newTranscription(id, 3))

		var firstDst string
		h.audioProc.normalizeFn = func(ctx context.Context, srcAbs, dstAbs string) error {
			firstDst = dstAbs
			return nil
		}

		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() { defer close(done); _ = h.pool.Run(ctx) }()
		synctest.Wait()
		cancel()
		<-done

		require.NotEmpty(t, firstDst)
		require.Len(t, h.queue.finishesFor(id), 1)

		// Simulate a manual retry via Store.Requeue: Attempts resets to 0,
		// so the next Claim below mints Attempts == 1 again -- exactly the
		// same value the very first claim above got. An ordinary re-claim
		// after a lease expiry (see expireLease) would have incremented
		// Attempts instead; only Requeue produces this repeat.
		h.queue.resetAttemptsAndReseed(id)

		var secondDst string
		h.audioProc.normalizeFn = func(ctx context.Context, srcAbs, dstAbs string) error {
			secondDst = dstAbs
			return nil
		}

		ctx2, cancel2 := context.WithCancel(context.Background())
		done2 := make(chan struct{})
		go func() { defer close(done2); _ = h.pool.Run(ctx2) }()
		synctest.Wait()
		cancel2()
		<-done2

		require.NotEmpty(t, secondDst)
		assert.NotEqual(t, firstDst, secondDst,
			"a retry after Requeue must get a distinct WAV path even though it shares the same Attempts number as the earlier claim")

		require.Len(t, h.queue.finishesFor(id), 2, "both the original attempt and the retry must have Finished")
	})
}

// ---------------------------------------------------------------------------
// 7. Lease token identity (finding 1) -- the fake itself must be honest
// ---------------------------------------------------------------------------

// TestFakeQueue_StaleLeaseToken_RejectedByHeartbeatAndFinish proves fakeQueue
// enforces the real lease guard (id + CURRENT lease_token + status=running),
// not merely the caller-supplied owner string. This is what finding 1
// actually hinged on: a worker's owner string is per-process and reused by
// every claim it ever makes, so a stale execution left over from a claim the
// row has since moved past — even one re-claimed by that very same owner —
// must still be rejected. Before this fake modeled lease identity, every
// such assertion in this package was vacuous: inverting Pool's real guard
// (using p.owner instead of the per-claim token) would have left this suite
// green.
func TestFakeQueue_StaleLeaseToken_RejectedByHeartbeatAndFinish(t *testing.T) {
	q := newFakeQueue()
	const owner = "same-owner-both-times"
	q.seed(newTranscription("job-stale", 3))

	first, err := q.Claim(context.Background(), owner, time.Minute)
	require.NoError(t, err)
	require.NotNil(t, first)
	require.NotNil(t, first.LeaseToken)
	staleToken := *first.LeaseToken

	// Simulate the lease expiring and this same row being re-claimed by
	// the SAME owner string -- exactly the scenario finding 1 describes: a
	// process's own claim loop re-claiming a row its own stale execution
	// is still (unknowingly) running.
	q.expireLease("job-stale")
	second, err := q.Claim(context.Background(), owner, time.Minute)
	require.NoError(t, err)
	require.NotNil(t, second)
	require.NotNil(t, second.LeaseToken)
	require.NotEqual(t, staleToken, *second.LeaseToken, "a re-claim must mint a fresh token even under the same owner")

	// The stale execution's Heartbeat, still carrying the first claim's
	// token, must be rejected even though the owner string it would have
	// sent is identical to the live claim's.
	_, err = q.Heartbeat(context.Background(), "job-stale", staleToken, time.Minute)
	require.Error(t, err)
	assert.Equal(t, domain.ErrCodeConflict, domain.CodeOf(err))

	// So must its Finish -- a stale execution must never be able to record
	// an outcome for a row it no longer holds the lease on.
	err = q.Finish(context.Background(), domain.FinishRequest{
		ID:         "job-stale",
		LeaseToken: staleToken,
		Status:     domain.StatusSucceeded,
		Result:     &domain.Result{Text: "must never be recorded"},
	})
	require.Error(t, err)
	assert.Equal(t, domain.ErrCodeConflict, domain.CodeOf(err))

	// The live claim's own Heartbeat and Finish, using the fresh token,
	// still work normally.
	canceled, err := q.Heartbeat(context.Background(), "job-stale", *second.LeaseToken, time.Minute)
	require.NoError(t, err)
	assert.False(t, canceled)

	err = q.Finish(context.Background(), domain.FinishRequest{
		ID:         "job-stale",
		LeaseToken: *second.LeaseToken,
		Status:     domain.StatusSucceeded,
		Result:     &domain.Result{Text: "the live result"},
	})
	require.NoError(t, err)
}

// TestFakeQueue_Finish_RejectsNonRunningRow proves Finish rejects a row that
// is not currently running (e.g. never claimed), independent of the token
// supplied — a real Finish's guard includes status='running' as well as the
// token match.
func TestFakeQueue_Finish_RejectsNonRunningRow(t *testing.T) {
	q := newFakeQueue()
	q.seed(newTranscription("job-not-running", 3))

	err := q.Finish(context.Background(), domain.FinishRequest{
		ID:         "job-not-running",
		LeaseToken: "whatever",
		Status:     domain.StatusSucceeded,
	})
	require.Error(t, err)
	assert.Equal(t, domain.ErrCodeConflict, domain.CodeOf(err))
}

// TestFakeQueue_Finish_RejectsAlreadyDoneContext and
// TestFakeQueue_ReleaseForShutdown_RejectsAlreadyDoneContext pin FIX 6: the
// fake must honor ctx, not merely the lease guard, or replacing job.go's
// `context.WithTimeout(context.WithoutCancel(jobCtx), ...)` with
// `context.WithTimeout(jobCtx, ...)` -- silently turning a forced-shutdown
// Finish/ReleaseForShutdown into a write against an already-canceled
// context that pgx would refuse before ever reaching the database -- would
// keep this whole suite green. Even a row with a perfectly valid, matching
// lease must still be rejected when ctx is already done.
func TestFakeQueue_Finish_RejectsAlreadyDoneContext(t *testing.T) {
	q := newFakeQueue()
	q.seed(newTranscription("job-done-ctx", 3))
	claimed, err := q.Claim(context.Background(), "w1", time.Minute)
	require.NoError(t, err)
	require.NotNil(t, claimed.LeaseToken)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err = q.Finish(ctx, domain.FinishRequest{
		ID:         "job-done-ctx",
		LeaseToken: *claimed.LeaseToken,
		Status:     domain.StatusSucceeded,
		Result:     &domain.Result{Text: "must never land"},
	})
	require.Error(t, err, "an already-canceled ctx must reject Finish even with a perfectly valid lease")
	assert.ErrorIs(t, err, context.Canceled)
}

func TestFakeQueue_ReleaseForShutdown_RejectsAlreadyDoneContext(t *testing.T) {
	q := newFakeQueue()
	q.seed(newTranscription("job-done-ctx-2", 3))
	claimed, err := q.Claim(context.Background(), "w1", time.Minute)
	require.NoError(t, err)
	require.NotNil(t, claimed.LeaseToken)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err = q.ReleaseForShutdown(ctx, "job-done-ctx-2", *claimed.LeaseToken)
	require.Error(t, err, "an already-canceled ctx must reject ReleaseForShutdown even with a perfectly valid lease")
	assert.ErrorIs(t, err, context.Canceled)
}

// ---------------------------------------------------------------------------
// 8. Concurrency ceiling
// ---------------------------------------------------------------------------

// TestPool_ConcurrencyCeiling proves Config.Concurrency + 1 available jobs
// never produce more than Config.Concurrency concurrent executions.
func TestPool_ConcurrencyCeiling(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		cfg := testConfig()
		cfg.Concurrency = 2
		h := newHarness(cfg)

		var current, maxSeen atomic.Int64
		gate := make(chan struct{})
		h.transcriber.transcribeFn = func(ctx context.Context, req domain.TranscribeRequest) (*domain.Result, error) {
			n := current.Add(1)
			defer current.Add(-1)
			for {
				m := maxSeen.Load()
				if n <= m || maxSeen.CompareAndSwap(m, n) {
					break
				}
			}
			<-gate
			return &domain.Result{Model: "large-v3-turbo-q5_0"}, nil
		}

		const total = 5 // Concurrency + 1, and then some
		for i := 0; i < total; i++ {
			h.queue.seed(newTranscription(fmt.Sprintf("job-%d", i), 3))
		}

		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() { defer close(done); _ = h.pool.Run(ctx) }()

		synctest.Wait()
		assert.LessOrEqual(t, maxSeen.Load(), int64(cfg.Concurrency))
		assert.Equal(t, cfg.Concurrency, h.transcriber.callCount(),
			"only Concurrency jobs may have started; the rest must wait for a free slot")

		close(gate)
		synctest.Wait()

		// The first wave just freed up Concurrency slots; give the claim
		// loop enough ticks to work through the remaining waves.
		time.Sleep(cfg.PollInterval * time.Duration(total*3))
		synctest.Wait()

		assert.Equal(t, total, h.transcriber.callCount())
		assert.LessOrEqual(t, maxSeen.Load(), int64(cfg.Concurrency), "concurrency ceiling must never have been exceeded, in any wave")

		cancel()
		<-done
	})
}

// ---------------------------------------------------------------------------
// 9. Shutdown drains rather than abandons
// ---------------------------------------------------------------------------

// TestPool_Shutdown_DrainsInFlightJob proves Shutdown waits for a job that
// is still within its grace period to finish on its own, and lets it
// Finish normally rather than abandoning it.
func TestPool_Shutdown_DrainsInFlightJob(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(testConfig())
		id := "job-9"
		h.queue.seed(newTranscription(id, 3))

		release := make(chan struct{})
		h.transcriber.transcribeFn = func(ctx context.Context, req domain.TranscribeRequest) (*domain.Result, error) {
			<-release
			return &domain.Result{Model: "large-v3-turbo-q5_0"}, nil
		}

		ctx, cancelRun := context.WithCancel(context.Background())
		runDone := make(chan struct{})
		go func() { defer close(runDone); _ = h.pool.Run(ctx) }()

		synctest.Wait()
		require.Equal(t, 1, h.transcriber.callCount())

		cancelRun() // stop claiming new work; the in-flight job keeps running
		synctest.Wait()

		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), time.Second)
		defer shutdownCancel()
		shutdownDone := make(chan struct{})
		var shutdownErr error
		go func() {
			defer close(shutdownDone)
			shutdownErr = h.pool.Shutdown(shutdownCtx)
		}()

		synctest.Wait() // Shutdown is now blocked waiting for the in-flight job
		assert.Equal(t, 0, h.queue.totalFinishes(), "the job must not be finished, or abandoned, before it completes on its own")

		close(release)
		synctest.Wait()

		<-shutdownDone
		require.NoError(t, shutdownErr)

		finishes := h.queue.finishesFor(id)
		require.Len(t, finishes, 1)
		assert.Equal(t, domain.StatusSucceeded, finishes[0].Status, "Shutdown must drain the job to completion, not abandon it mid-write")

		<-runDone
	})
}

// TestPool_Shutdown_ForceCancelsAfterDeadline proves the other half of
// Shutdown: once its ctx's grace period expires, it force-cancels every
// still in-flight job's context. Two jobs are in flight to cover both of
// finish's forced-shutdown outcomes (finding 2):
//   - failID's execute is genuinely interrupted by the forced cancel (it
//     only returns once its own context is done, with no real result) and
//     must be abandoned -- never Finished, let alone Finished as failed --
//     so lease expiry can requeue it.
//   - succeedID's execute ignores the forced cancel and goes on to produce
//     a real result anyway (modeling the race window between execute
//     returning with a result in hand and runOne noticing the forced
//     flag). Discarding that result would silently lose a transcript that
//     genuinely completed, so it must still be Finished as succeeded even
//     though Shutdown force-canceled its context.
func TestPool_Shutdown_ForceCancelsAfterDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		cfg := testConfig()
		cfg.JobTimeout = time.Second // bounds Shutdown's post-force-cancel wait
		h := newHarness(cfg)
		failID := "job-10"
		succeedID := "job-11"
		h.queue.seed(newTranscription(failID, 3))
		h.queue.seed(newTranscription(succeedID, 3))

		release := make(chan struct{})
		h.transcriber.transcribeFn = func(ctx context.Context, req domain.TranscribeRequest) (*domain.Result, error) {
			if strings.Contains(req.AudioPath, succeedID) {
				<-release // ignores the forced cancel; returns a real result once released
				return &domain.Result{Model: "large-v3-turbo-q5_0", Text: "finished despite forced shutdown"}, nil
			}
			<-ctx.Done() // only returns once its own context is canceled
			return nil, ctx.Err()
		}

		ctx, cancelRun := context.WithCancel(context.Background())
		runDone := make(chan struct{})
		go func() { defer close(runDone); _ = h.pool.Run(ctx) }()

		synctest.Wait()
		cancelRun()
		synctest.Wait()

		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
		defer shutdownCancel()

		shutdownDone := make(chan struct{})
		var shutdownErr error
		go func() {
			defer close(shutdownDone)
			shutdownErr = h.pool.Shutdown(shutdownCtx)
		}()

		time.Sleep(20 * time.Millisecond) // past shutdownCtx's deadline
		synctest.Wait()                   // forceCancelAll has now fired for both jobs

		// succeedID is still blocked on release, ignoring its now-canceled
		// context, so Shutdown cannot have returned yet.
		close(release)
		synctest.Wait()

		<-shutdownDone
		require.NoError(t, shutdownErr, "both forced jobs returned promptly")

		assert.Empty(t, h.queue.finishesFor(failID),
			"a job canceled by Shutdown's drain deadline, with no result in hand, must never be Finished")

		// FIX 1: a genuinely interrupted forced-shutdown job must not simply
		// be left running for lease expiry to eventually (and, if it was on
		// its last attempt, permanently) fail -- its claim must be given
		// back via Queue.ReleaseForShutdown instead, WITHOUT consuming the
		// attempt it was claimed under.
		releaseCalls := h.queue.releaseForShutdownCallsFor(failID)
		require.Len(t, releaseCalls, 1,
			"a job Shutdown force-canceled with no result in hand must have its claim released via ReleaseForShutdown")
		assert.Equal(t, "lease-token-1", releaseCalls[0].leaseToken,
			"ReleaseForShutdown must be called with this attempt's own lease token")
		assert.Empty(t, h.queue.releaseForShutdownCallsFor(succeedID),
			"a job that finished successfully must never go through ReleaseForShutdown")

		succeedFinishes := h.queue.finishesFor(succeedID)
		require.Len(t, succeedFinishes, 1,
			"a job that produced a real result must still be Finished even though Shutdown force-canceled it")
		assert.Equal(t, domain.StatusSucceeded, succeedFinishes[0].Status)
		require.NotNil(t, succeedFinishes[0].Result)
		assert.Equal(t, "finished despite forced shutdown", succeedFinishes[0].Result.Text)

		<-runDone
	})
}

// TestPool_Shutdown_NeverReturnsDrainedWithAnUnaccountedJob stress-tests
// FIX 4. A job whose Claim happens to complete BEFORE Shutdown is called is
// entirely legitimate and Shutdown must drain it like any other in-flight
// job (wait for its Finish) before returning nil -- that is not the race
// this test is after. The actual bug this fix closes is Shutdown's
// p.stopping.Store and p.wg.Wait interleaving with claimAndStart's stopping
// check and startJob's p.wg.Add(1) such that Shutdown can observe a zero
// WaitGroup count and return nil ("everything drained") a moment before a
// just-claimed job's Add(1) runs -- i.e. Shutdown reporting "drained" while
// a job it never waited for is only just starting. So the invariant this
// test actually checks, at the instant Shutdown returns nil, is: if the
// transcriber has been invoked at all, Shutdown must already have recorded
// that job's Finish (proof it genuinely waited for it), never "invoked but
// not yet accounted for".
//
// Deliberately real goroutines and real time (not synctest, whose
// deterministic scheduler would not reproduce a genuine scheduling race),
// with fakeQueue.claimDelay widening the window a real database round trip
// would have, and many iterations under -race to have a realistic chance of
// catching it.
func TestPool_Shutdown_NeverReturnsDrainedWithAnUnaccountedJob(t *testing.T) {
	const iterations = 300
	for i := 0; i < iterations; i++ {
		h := newHarness(testConfig())
		h.queue.claimDelay = 200 * time.Microsecond
		id := fmt.Sprintf("job-race-%d", i)
		h.queue.seed(newTranscription(id, 3))

		var started atomic.Bool
		h.transcriber.transcribeFn = func(ctx context.Context, req domain.TranscribeRequest) (*domain.Result, error) {
			started.Store(true)
			return &domain.Result{Model: "large-v3-turbo-q5_0"}, nil
		}

		ctx, cancel := context.WithCancel(context.Background())
		runDone := make(chan struct{})
		go func() { defer close(runDone); _ = h.pool.Run(ctx) }()

		// Give the claim loop a moment to actually enter its Claim round
		// trip (claimDelay keeps it "in flight" a little while) before
		// racing Shutdown against it.
		time.Sleep(50 * time.Microsecond)
		shutdownErr := h.pool.Shutdown(context.Background())
		require.NoError(t, shutdownErr)

		// Measured the INSTANT Shutdown returns -- not after the test's own
		// cleanup below, which would let a genuinely orphaned job finish on
		// its own and mask exactly the bug this test exists to catch.
		startedNow := started.Load()
		finishesNow := h.queue.totalFinishes()

		cancel()
		<-runDone

		if startedNow {
			assert.Equal(t, 1, finishesNow,
				"the transcriber ran, so Shutdown must already have drained (Finished) it before returning nil; iteration %d", i)
		}
	}
}

// ---------------------------------------------------------------------------
// HealthChecker
// ---------------------------------------------------------------------------

// TestPool_Check_ReportsWedgedClaimLoop proves Check passes as soon as a
// Pool is constructed (before Run has even been called) and fails once the
// claim loop has plausibly gone silent for too long.
func TestPool_Check_ReportsWedgedClaimLoop(t *testing.T) {
	h := newHarness(testConfig())
	assert.Equal(t, "worker_pool", h.pool.Name())
	assert.NoError(t, h.pool.Check(context.Background()))

	h.clock.Advance(time.Hour)
	assert.Error(t, h.pool.Check(context.Background()))
}
