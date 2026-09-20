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

	"github.com/pablofornell/helios-audio-transcription-service/internal/domain"
)

// TestRejectedUpload_NoAudioStream submits a real video-only MP4 (ffprobe
// demuxes it fine but finds zero audio streams) and asserts the job fails
// with ErrCodeNoAudioStream, surfaced by the API with the matching HTTP
// status (422, per CONTRACT.md section 6's code table) and error code.
func TestRejectedUpload_NoAudioStream(t *testing.T) {
	requireDocker(t)
	requireFFmpeg(t)

	ts := newTestServer(t)

	created := submitFile(t, ts.base, fixtureVideoOnlyMP4, "video/mp4", nil)

	final := waitForStatus(t, ts.base, created.ID, domain.StatusFailed, terminalDeadline)
	require.NotNil(t, final.Error, "expected an Error on a failed transcription")
	assert.Equal(t, domain.ErrCodeNoAudioStream, final.Error.Code)

	resp := getTranscript(t, ts.base, created.ID, "json")
	defer resp.Body.Close()
	// A failed transcription has no transcript to serve -- the handler
	// checks Status == succeeded before even rendering.
	assert.Equal(t, http.StatusConflict, resp.StatusCode)
}

// TestCancel_PendingNeverBecomesSucceeded submits, cancels, and asserts the
// job reaches canceled and never later reports succeeded -- outliving
// several lease windows before the final check, the same defect
// BLUEPRINT's own cancel scenario guards against (see
// TestCancel_RunningWorkerStopsAndSticks below for the "already claimed"
// half of this).
//
// The worker pool is deliberately not started until AFTER the cancel call
// lands: app.Repository.RequestCancel only moves a PENDING row straight to
// canceled -- a row that is already running by the time the cancel request
// arrives is merely flagged (see Store.RequestCancel's own doc), so
// canceling against a live pool would race it for which state actually
// gets observed. Submitting against a not-yet-started pool makes "canceled
// while still pending" deterministic, which is the specific transition
// this test exists to prove; TestCancel_RunningWorkerStopsAndSticks below
// is what proves the "already running" half.
func TestCancel_PendingNeverBecomesSucceeded(t *testing.T) {
	requireDocker(t)
	requireFFmpeg(t)

	cfg := newTestConfig(t)
	st := newRawStack(t, cfg, nil)
	base := serveStack(t, st)

	created := submitFile(t, base, fixtureGoodMP3, "audio/mpeg", nil)
	require.Equal(t, domain.StatusPending, created.Status)

	resp := cancelTranscription(t, base, created.ID)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	canceled := decodeJSON[domain.Transcription](t, resp)
	assert.Equal(t, domain.StatusCanceled, canceled.Status)

	// Only now start the pool: it must never claim (or ever mark
	// succeeded) a row that is already canceled.
	st.Start()

	time.Sleep(5 * cfg.Worker.LeaseDuration)
	still := getTranscription(t, base, created.ID)
	assert.Equal(t, domain.StatusCanceled, still.Status, "a canceled transcription must never later report succeeded")
}

// TestCancel_SemanticsMatchAppFakes cross-checks the real Postgres
// RequestCancel behaviour against what internal/app's in-memory fakes (see
// internal/app/fakes_test.go's memDB.requestCancel) assume: canceling a
// PENDING job sets CompletedAt immediately, since it moves straight to the
// terminal StatusCanceled with no worker ever touching it. If real
// Postgres disagreed with the fakes here, internal/app's own unit tests
// would be silently testing a fiction -- this is exactly the kind of
// cross-package drift this suite exists to catch.
//
// As in TestCancel_PendingNeverBecomesSucceeded above, the pool is never
// started: this test needs the cancel to land while the row is
// deterministically still pending, not racing a live claim loop.
func TestCancel_SemanticsMatchAppFakes(t *testing.T) {
	requireDocker(t)
	requireFFmpeg(t)

	cfg := newTestConfig(t)
	st := newRawStack(t, cfg, nil)
	base := serveStack(t, st)

	created := submitFile(t, base, fixtureGoodMP3, "audio/mpeg", nil)
	require.Equal(t, domain.StatusPending, created.Status)
	require.Nil(t, created.CompletedAt, "a freshly submitted (pending) job must not carry a CompletedAt yet")

	resp := cancelTranscription(t, base, created.ID)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	canceled := decodeJSON[domain.Transcription](t, resp)

	assert.Equal(t, domain.StatusCanceled, canceled.Status)
	require.NotNil(t, canceled.CompletedAt,
		"REAL FINDING CHECK: internal/app's memDB.requestCancel sets CompletedAt when canceling a pending job -- "+
			"real Postgres's Store.RequestCancel must do the same (see its CASE WHEN status = 'pending' THEN now() clause) "+
			"or the two packages' tests are asserting on a fiction that does not match production")
}

// TestCancel_RunningWorkerStopsAndSticks proves canceling a job that is
// ALREADY EXECUTING actually stops it -- app.Repository.RequestCancel's own
// database write alone cannot demonstrate that; it only flags
// cancel_requested and leaves status=running for the worker's own next
// Queue.Heartbeat to notice (see Store.RequestCancel's doc). This test uses
// fake.Config{Delay: ...} on a dedicated stack so there is a window to
// cancel a running job before the fake transcriber would otherwise finish
// it.
func TestCancel_RunningWorkerStopsAndSticks(t *testing.T) {
	requireDocker(t)
	requireFFmpeg(t)

	// Long enough that this test has a real window to observe "running"
	// and cancel it before Transcribe would otherwise return on its own;
	// short enough the test stays fast either way.
	slowTranscriber := newDelayedTranscriber(2 * time.Second)
	ts := newTestServerWithTranscriber(t, slowTranscriber)

	created := submitFile(t, ts.base, fixtureGoodMP3, "audio/mpeg", nil)
	waitForStatus(t, ts.base, created.ID, domain.StatusRunning, 2*time.Second)

	resp := cancelTranscription(t, ts.base, created.ID)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	waitForStatus(t, ts.base, created.ID, domain.StatusCanceled, 3*time.Second)

	// Outlive several heartbeat/lease windows: the worker's next Heartbeat
	// must notice cancel_requested and abandon the job without ever
	// calling Finish(succeeded) for it.
	time.Sleep(5 * ts.stack.Cfg.Worker.LeaseDuration)
	still := getTranscription(t, ts.base, created.ID)
	assert.Equal(t, domain.StatusCanceled, still.Status, "a canceled-while-running transcription must never later report succeeded")
}

// TestRetry_ResetsAttemptsAndRunsAgain forces a permanent, non-retryable
// failure (the same no-audio-stream rejection TestRejectedUpload_NoAudioStream
// exercises), retries it via POST /retry, and asserts it goes back to
// pending with Attempts reset to 0, then runs again -- reaching failed a
// second time, since the same bad fixture fails the same way every attempt.
//
// This is also the cross-check CONTRACT.md's task description calls for:
// internal/app's memDB.requeue (fakes_test.go) resets Attempts to 0 on
// requeue; real Postgres's Store.Requeue must do the same (its `attempts =
// 0` in the UPDATE) or the two packages' tests disagree about production
// behaviour.
func TestRetry_ResetsAttemptsAndRunsAgain(t *testing.T) {
	requireDocker(t)
	requireFFmpeg(t)

	ts := newTestServer(t)

	created := submitFile(t, ts.base, fixtureVideoOnlyMP4, "video/mp4", nil)
	failedOnce := waitForStatus(t, ts.base, created.ID, domain.StatusFailed, terminalDeadline)
	require.Equal(t, 1, failedOnce.Attempts, "a non-retryable failure must fail permanently after exactly one attempt")

	resp := retryTranscription(t, ts.base, created.ID)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	requeued := decodeJSON[domain.Transcription](t, resp)

	assert.Equal(t, domain.StatusPending, requeued.Status)
	assert.Equal(t, 0, requeued.Attempts,
		"REAL FINDING CHECK: internal/app's memDB.requeue resets Attempts to 0 on manual retry -- "+
			"real Postgres's Store.Requeue must do the same or the two packages' tests disagree about production")
	assert.Nil(t, requeued.Error, "a manual retry must clear the previous failure's Error")

	// It must actually run again, not just sit in pending: the same bad
	// fixture fails the same way, so it reaches failed a second time, this
	// time with Attempts back at 1 (not 2 -- confirming the reset was real,
	// not merely reported once in the retry response).
	failedAgain := waitForStatus(t, ts.base, created.ID, domain.StatusFailed, terminalDeadline)
	assert.Equal(t, 1, failedAgain.Attempts)
	require.NotNil(t, failedAgain.Error)
	assert.Equal(t, domain.ErrCodeNoAudioStream, failedAgain.Error.Code)
}

// TestRetry_ConflictUnlessFailedOrCanceled asserts POST /retry on a pending
// job -- neither failed nor canceled -- is rejected as a 409 conflict, per
// CONTRACT.md section 6.
func TestRetry_ConflictUnlessFailedOrCanceled(t *testing.T) {
	requireDocker(t)
	requireFFmpeg(t)

	ts := newTestServer(t)

	created := submitFile(t, ts.base, fixtureGoodMP3, "audio/mpeg", nil)

	resp := retryTranscription(t, ts.base, created.ID)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusConflict, resp.StatusCode)
	derr := decodeError(t, resp)
	assert.Equal(t, domain.ErrCodeConflict, derr.Code)
}

// TestDelete_RemovesRowAndStoredDirectory submits, lets it succeed (so
// there is a real stored source file to prove gets removed), deletes it,
// and asserts both the database row (404 on a subsequent Get) and the
// transcriptions/<id> directory on disk are gone.
func TestDelete_RemovesRowAndStoredDirectory(t *testing.T) {
	requireDocker(t)
	requireFFmpeg(t)

	ts := newTestServer(t)

	created := submitFile(t, ts.base, fixtureGoodMP3, "audio/mpeg", nil)
	final := waitForStatus(t, ts.base, created.ID, domain.StatusSucceeded, terminalDeadline)

	dir := filepath.Join(ts.stack.Cfg.Storage.RootDir, "transcriptions", final.ID)
	_, err := os.Stat(dir)
	require.NoError(t, err, "expected the transcription's directory to exist on disk before delete")

	deleteTranscription(t, ts.base, final.ID)

	resp := doJSON(t, http.MethodGet, ts.base+"/api/v1/transcriptions/"+final.ID, nil)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusNotFound, resp.StatusCode, "expected the row to be gone")

	_, err = os.Stat(dir)
	assert.True(t, os.IsNotExist(err), "expected the transcription's directory to be removed from disk")
}

// ---------------------------------------------------------------------------
// helpers specific to the running-cancel scenario: a Transcriber that
// blocks for a fixed delay (respecting ctx), so a test has a real window to
// observe "running" and act on it before the fake transcriber would
// otherwise return.
// ---------------------------------------------------------------------------

// delayedTranscriber wraps internal/transcriber/fake's own Delay knob (see
// fake.Config.Delay) under a name local to this file, purely so
// TestCancel_RunningWorkerStopsAndSticks's intent reads clearly at the call
// site.
func newDelayedTranscriber(delay time.Duration) *fakeDelayedTranscriber {
	return &fakeDelayedTranscriber{delay: delay}
}

type fakeDelayedTranscriber struct {
	delay time.Duration
}

func (f *fakeDelayedTranscriber) Available(ctx context.Context) error { return nil }

func (f *fakeDelayedTranscriber) Transcribe(ctx context.Context, req domain.TranscribeRequest) (*domain.Result, error) {
	select {
	case <-time.After(f.delay):
	case <-ctx.Done():
		return nil, domain.Wrap(domain.ErrCodeCanceled, "delayed transcribe canceled", ctx.Err())
	}
	return &domain.Result{
		Text:     "delayed fake result",
		Language: "en",
		Model:    req.Options.Model,
		Task:     req.Options.Task,
		Segments: []domain.Segment{{ID: 0, Start: 0, End: 1, Text: "delayed fake result"}},
	}, nil
}
