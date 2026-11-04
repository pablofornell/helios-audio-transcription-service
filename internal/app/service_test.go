package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pablofornell/helios-audio-transcription-service/internal/domain"
	"github.com/pablofornell/helios-audio-transcription-service/internal/observability"
)

// ---------------------------------------------------------------------------
// test harness
// ---------------------------------------------------------------------------

// testHarness bundles a Service with every fake behind it, so each test can
// reach into whichever fake it needs to assert against. calls is shared by
// every fake, so its snapshot is one true, ordered timeline across ports.
type testHarness struct {
	svc     *Service
	repo    *fakeRepository
	queue   *fakeQueue
	store   *fakeStore
	clock   *fakeClock
	ids     *fakeIDGenerator
	calls   *callLog
	metrics *observability.Metrics
}

// newHarness builds a Service wired to fresh fakes, with production
// defaults sane enough that most tests need not touch them: a generous
// MaxUploadBytes and MinFreeBytes, and the supported turbo model as the
// default and sole allowed model. opts, if given, mutate the Deps before NewService is called, for
// the handful of tests that need a tighter limit.
func newHarness(t *testing.T, opts ...func(*Deps)) *testHarness {
	t.Helper()
	db := newMemDB()
	clock := newFakeClock(time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC))
	calls := newCallLog()

	h := &testHarness{
		repo:    newFakeRepository(db, calls),
		queue:   newFakeQueue(db, clock, calls),
		store:   newFakeStore(calls),
		clock:   clock,
		ids:     newFakeIDGenerator(),
		calls:   calls,
		metrics: observability.NewMetrics(prometheus.NewRegistry()),
	}

	deps := Deps{
		Repo:           h.repo,
		Queue:          h.queue,
		Store:          h.store,
		Clock:          h.clock,
		IDs:            h.ids,
		Logger:         slog.New(slog.NewTextHandler(io.Discard, nil)),
		Metrics:        h.metrics,
		DefaultModel:   "large-v3-turbo-q5_0",
		AllowedModels:  []string{"large-v3-turbo-q5_0"},
		MaxAttempts:    3,
		MaxUploadBytes: 1 << 20, // 1 MiB
		MinFreeBytes:   1024,
	}
	for _, opt := range opts {
		opt(&deps)
	}
	h.svc = NewService(deps)
	return h
}

// submitBody is a small, valid Submit request a test can mutate a field of.
func submitBody(t *testing.T, filename, body string) SubmitRequest {
	t.Helper()
	return SubmitRequest{
		Filename:  filename,
		MediaType: "audio/wav",
		Body:      strings.NewReader(body),
	}
}

// scrapeMetric returns the /metrics line whose series name is exactly name
// (ignoring any label suffix), or "" if it never appears -- used instead of
// reaching into observability.Metrics' unexported fields, which this
// package must not do.
func scrapeMetric(t *testing.T, m *observability.Metrics, name string) string {
	t.Helper()
	rec := httptest.NewRecorder()
	m.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	for _, line := range strings.Split(rec.Body.String(), "\n") {
		if strings.HasPrefix(line, name) {
			return line
		}
	}
	return ""
}

// ---------------------------------------------------------------------------
// Submit
// ---------------------------------------------------------------------------

func TestSubmit_Success(t *testing.T) {
	h := newHarness(t)

	const body = "hello whisper, this is audio"
	got, err := h.svc.Submit(context.Background(), submitBody(t, "My Recording.MP3", body))
	require.NoError(t, err)
	require.NotNil(t, got)

	assert.Equal(t, domain.StatusPending, got.Status)
	assert.Equal(t, 3, got.MaxAttempts)
	assert.Equal(t, h.clock.Now(), got.CreatedAt)
	assert.Equal(t, h.clock.Now(), got.UpdatedAt)
	assert.Equal(t, "large-v3-turbo-q5_0", got.Options.Model, "an empty Options.Model must resolve to Deps.DefaultModel")
	assert.Equal(t, "My Recording.MP3", got.Audio.Filename)
	assert.Equal(t, "audio/wav", got.Audio.MediaType)
	assert.Equal(t, int64(len(body)), got.Audio.SizeBytes)

	wantPath := "transcriptions/" + got.ID + "/source.mp3"
	assert.Equal(t, wantPath, got.SourcePath, "the source extension must come from the sanitized filename")

	data, ok := h.store.get(wantPath)
	require.True(t, ok, "the upload must be written to the exact store path")
	assert.Equal(t, body, string(data))

	stored, err := h.repo.Get(context.Background(), got.ID)
	require.NoError(t, err)
	assert.Equal(t, got.ID, stored.ID)
	assert.Equal(t, got.SourcePath, stored.SourcePath)
	assert.Equal(t, got.Options, stored.Options)

	assert.Contains(t, scrapeMetric(t, h.metrics, "helios_transcriptions_submitted_total"), " 1")
}

func TestSubmit_ResolvesDefaultModelAfterValidation(t *testing.T) {
	// A caller-supplied model outside the allow-list must still fail
	// Validate; only an EMPTY model is resolved to the configured default.
	h := newHarness(t)

	_, err := h.svc.Submit(context.Background(), SubmitRequest{
		Filename: "a.wav",
		Options:  domain.Options{Model: "not-a-real-model"},
		Body:     strings.NewReader("d"),
	})
	require.Error(t, err)
	assert.Equal(t, domain.ErrCodeInvalidInput, domain.CodeOf(err))
}

func TestSubmit_RejectsInvalidOptions(t *testing.T) {
	h := newHarness(t)

	_, err := h.svc.Submit(context.Background(), SubmitRequest{
		Filename: "a.wav",
		Options:  domain.Options{BeamSize: 99}, // out of [1,16]
		Body:     strings.NewReader("d"),
	})
	require.Error(t, err)
	assert.Equal(t, domain.ErrCodeInvalidInput, domain.CodeOf(err))
	assert.Equal(t, 0, countOf(h.store.calls.snapshot(), "store.Put"))
	assert.Equal(t, 0, countOf(h.repo.calls.snapshot(), "repo.Create"))
}

func TestSubmit_RefusesOnLowFreeSpaceWithoutWriting(t *testing.T) {
	h := newHarness(t)
	h.store.freeBytes = 100 // below the harness's default MinFreeBytes (1024)

	_, err := h.svc.Submit(context.Background(), submitBody(t, "a.wav", "data"))
	require.Error(t, err)
	assert.Equal(t, domain.ErrCodeDiskFull, domain.CodeOf(err))
	assert.Equal(t, 0, countOf(h.store.calls.snapshot(), "store.Put"),
		"a refused submission must never write anything")
	assert.Equal(t, 0, countOf(h.repo.calls.snapshot(), "repo.Create"))
}

func TestSubmit_PayloadTooLarge(t *testing.T) {
	h := newHarness(t, func(d *Deps) { d.MaxUploadBytes = 8 })

	got, err := h.svc.Submit(context.Background(), submitBody(t, "big.wav", strings.Repeat("x", 100)))
	require.Error(t, err)
	assert.Nil(t, got)
	assert.Equal(t, domain.ErrCodePayloadTooLarge, domain.CodeOf(err))
	assert.Equal(t, 0, countOf(h.repo.calls.snapshot(), "repo.Create"),
		"an oversized upload must never reach Repo.Create")

	id := h.ids.last()
	require.NotEmpty(t, id)
	assert.False(t, h.store.hasPrefix("transcriptions/"+id),
		"the half-written upload directory must be cleaned up")
	assert.Contains(t, h.store.calls.snapshot(), "store.RemoveDir")
}

func TestSubmit_CleansUpWhenCreateFails(t *testing.T) {
	h := newHarness(t)
	h.repo.createErrAny = domain.New(domain.ErrCodeInternal, "create boom")

	_, err := h.svc.Submit(context.Background(), submitBody(t, "clip.wav", "audio-bytes"))
	require.Error(t, err)
	assert.Equal(t, domain.ErrCodeInternal, domain.CodeOf(err))

	id := h.ids.last()
	require.NotEmpty(t, id)
	assert.False(t, h.store.hasPrefix("transcriptions/"+id),
		"a failed Repo.Create must not leave the uploaded bytes behind")
	assert.Contains(t, h.store.calls.snapshot(), "store.RemoveDir")
}

// ---------------------------------------------------------------------------
// classifyStoreErr: a client disconnect must not be reported as ErrCodeInternal (D4)
// ---------------------------------------------------------------------------

// firstFakeSubmitID is the id newHarness's default, unseeded
// fakeIDGenerator hands out on the very first Submit call -- see its own
// doc. Hardcoded here (rather than discovered via h.ids.last(), which only
// has a value AFTER Submit runs) because these tests need to know the
// store path Submit will write to BEFORE calling Submit, to preset
// h.store.putErr on it.
const firstFakeSubmitID = "00000000-0000-0000-0000-000000000001"

func TestSubmit_CanceledUploadIsErrCodeCanceledNotInternal(t *testing.T) {
	// localfs.Store wraps ctx.Err() with fmt.Errorf("...: %w", err) -- a
	// plain error, not a *domain.Error -- so classifyStoreErr must find
	// context.Canceled itself via errors.Is before it ever falls through
	// to its ErrCodeInternal default. Before D4, this reported "internal"
	// (500) for what is really just an ordinary client disconnect.
	h := newHarness(t)
	wantPath := "transcriptions/" + firstFakeSubmitID + "/source.wav"
	h.store.putErr = map[string]error{
		wantPath: fmt.Errorf("localfs: write canceled: %w", context.Canceled),
	}

	_, err := h.svc.Submit(context.Background(), submitBody(t, "clip.wav", "data"))
	require.Error(t, err)
	assert.Equal(t, domain.ErrCodeCanceled, domain.CodeOf(err))
}

func TestSubmit_DeadlineExceededUploadIsErrCodeTimeoutNotInternal(t *testing.T) {
	h := newHarness(t)
	wantPath := "transcriptions/" + firstFakeSubmitID + "/source.wav"
	h.store.putErr = map[string]error{
		wantPath: fmt.Errorf("localfs: write timed out: %w", context.DeadlineExceeded),
	}

	_, err := h.svc.Submit(context.Background(), submitBody(t, "clip.wav", "data"))
	require.Error(t, err)
	assert.Equal(t, domain.ErrCodeTimeout, domain.CodeOf(err))
}

// ---------------------------------------------------------------------------
// Submit: options discovered only after req.Body's own EOF (D3)
// ---------------------------------------------------------------------------

// lateOptionsBody is a minimal io.Reader that also implements Submit's
// unexported lateOptionsProvider interface, standing in for
// internal/httpapi's fileWithLateOptions -- proving Submit's own handling
// of that interface without depending on the HTTP layer at all.
type lateOptionsBody struct {
	io.Reader
	late      domain.Options
	lateFound bool
	lateErr   error
}

func (b *lateOptionsBody) LateOptions() (domain.Options, bool, error) {
	return b.late, b.lateFound, b.lateErr
}

func TestSubmit_AppliesOptionsDiscoveredAfterBodyEOF(t *testing.T) {
	h := newHarness(t)
	body := &lateOptionsBody{
		Reader:    strings.NewReader("audio-bytes"),
		late:      domain.Options{Model: "large-v3-turbo-q5_0", Task: domain.TaskTranslate, WordTimestamps: true},
		lateFound: true,
	}

	got, err := h.svc.Submit(context.Background(), SubmitRequest{Filename: "a.wav", Body: body})
	require.NoError(t, err)
	assert.Equal(t, "large-v3-turbo-q5_0", got.Options.Model)
	assert.Equal(t, domain.TaskTranslate, got.Options.Task)
	assert.True(t, got.Options.WordTimestamps)
}

func TestSubmit_LateOptionsNotFoundKeepsOriginal(t *testing.T) {
	h := newHarness(t)
	body := &lateOptionsBody{Reader: strings.NewReader("audio-bytes"), lateFound: false}

	got, err := h.svc.Submit(context.Background(), SubmitRequest{
		Filename: "a.wav",
		Options:  domain.Options{Model: "large-v3-turbo-q5_0"},
		Body:     body,
	})
	require.NoError(t, err)
	assert.Equal(t, "large-v3-turbo-q5_0", got.Options.Model, "no late options found must leave req.Options as the final word")
}

func TestSubmit_InvalidLateOptionsCleansUpAlreadyWrittenBytes(t *testing.T) {
	h := newHarness(t)
	body := &lateOptionsBody{
		Reader:    strings.NewReader("audio-bytes"),
		late:      domain.Options{Model: "not-an-allowed-model"},
		lateFound: true,
	}

	_, err := h.svc.Submit(context.Background(), SubmitRequest{Filename: "a.wav", Body: body})
	require.Error(t, err)
	assert.Equal(t, domain.ErrCodeInvalidInput, domain.CodeOf(err))

	id := h.ids.last()
	require.NotEmpty(t, id)
	assert.False(t, h.store.hasPrefix("transcriptions/"+id),
		"options rejected only after the file's bytes were already written must still clean up the upload")
}

func TestSubmit_LateOptionsErrorPropagates(t *testing.T) {
	h := newHarness(t)
	body := &lateOptionsBody{
		Reader:  strings.NewReader("audio-bytes"),
		lateErr: domain.New(domain.ErrCodeInvalidInput, "malformed options JSON").WithDetails("field", "options"),
	}

	_, err := h.svc.Submit(context.Background(), SubmitRequest{Filename: "a.wav", Body: body})
	require.Error(t, err)
	assert.Equal(t, domain.ErrCodeInvalidInput, domain.CodeOf(err))
}

func TestSubmit_SanitizesFilename(t *testing.T) {
	h := newHarness(t)

	got, err := h.svc.Submit(context.Background(), submitBody(t, "../../etc/passwd", "data"))
	require.NoError(t, err)
	assert.Equal(t, "passwd", got.Audio.Filename)
	assert.Equal(t, "transcriptions/"+got.ID+"/source.bin", got.SourcePath,
		"a filename with no extension must fall back to .bin")
}

// ---------------------------------------------------------------------------
// sanitizeFilename (unit)
// ---------------------------------------------------------------------------

func TestSanitizeFilename(t *testing.T) {
	tests := []struct {
		name         string
		in           string
		wantFilename string
		wantExt      string
	}{
		{"path traversal", "../../etc/passwd", "passwd", defaultUploadExt},
		{"windows-style traversal", `..\..\Windows\System32\config.sys`, "config.sys", ".sys"},
		{"no extension", "recording", "recording", defaultUploadExt},
		{"dotfile has no extension", ".gitignore", ".gitignore", defaultUploadExt},
		{"trailing dot has no extension", "clip.", "clip.", defaultUploadExt},
		{"uppercase extension is lowercased for ext but not filename", "Track.WAV", "Track.WAV", ".wav"},
		{"control characters are stripped", "bad\x00name\x1f.mp3", "badname.mp3", ".mp3"},
		{"empty becomes upload", "", "upload", defaultUploadExt},
		{"whitespace-only becomes upload", "   ", "upload", defaultUploadExt},
		{"embedded slash is treated as a directory separator", "a/b/c.flac", "c.flac", ".flac"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			filename, ext := sanitizeFilename(tt.in)
			assert.Equal(t, tt.wantFilename, filename)
			assert.Equal(t, tt.wantExt, ext)
		})
	}
}

func TestSanitizeFilename_CapsLength(t *testing.T) {
	long := strings.Repeat("a", 400) + ".wav"
	filename, ext := sanitizeFilename(long)
	assert.Len(t, filename, maxSanitizedFilenameBytes)
	assert.Equal(t, defaultUploadExt, ext, "truncation cutting off the real extension must fall back to .bin")
}

// ---------------------------------------------------------------------------
// Get / List
// ---------------------------------------------------------------------------

func TestGet(t *testing.T) {
	h := newHarness(t)
	got, err := h.svc.Submit(context.Background(), submitBody(t, "a.wav", "d"))
	require.NoError(t, err)

	fetched, err := h.svc.Get(context.Background(), got.ID)
	require.NoError(t, err)
	assert.Equal(t, got.ID, fetched.ID)

	_, err = h.svc.Get(context.Background(), uuid.NewString())
	assert.Equal(t, domain.ErrCodeNotFound, domain.CodeOf(err))
}

func TestList_LimitNormalization(t *testing.T) {
	tests := []struct {
		name      string
		limit     int
		wantLimit int
	}{
		{"zero uses default", 0, defaultListLimit},
		{"negative uses default", -5, defaultListLimit},
		{"within range is kept", 10, 10},
		{"excessive is capped", 100000, maxListLimit},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t)
			_, err := h.svc.List(context.Background(), ListFilter{Limit: tt.limit})
			require.NoError(t, err)
			assert.Equal(t, tt.wantLimit, h.repo.lastListFilter.Limit)
		})
	}
}

// ---------------------------------------------------------------------------
// Delete
// ---------------------------------------------------------------------------

func TestDelete_RemovesRowAndDirectory(t *testing.T) {
	h := newHarness(t)
	got, err := h.svc.Submit(context.Background(), submitBody(t, "a.wav", "d"))
	require.NoError(t, err)
	require.True(t, h.store.hasPrefix("transcriptions/"+got.ID))

	require.NoError(t, h.svc.Delete(context.Background(), got.ID))

	_, err = h.repo.Get(context.Background(), got.ID)
	assert.Equal(t, domain.ErrCodeNotFound, domain.CodeOf(err))
	assert.False(t, h.store.hasPrefix("transcriptions/"+got.ID))
}

func TestDelete_ToleratesStoreRemovalFailure(t *testing.T) {
	h := newHarness(t)
	got, err := h.svc.Submit(context.Background(), submitBody(t, "a.wav", "d"))
	require.NoError(t, err)
	h.store.removeDirErr = errors.New("simulated fs failure")

	err = h.svc.Delete(context.Background(), got.ID)
	require.NoError(t, err, "a store cleanup failure must not fail Delete once the row is gone")

	_, err = h.repo.Get(context.Background(), got.ID)
	assert.Equal(t, domain.ErrCodeNotFound, domain.CodeOf(err), "the row must still be gone")
}

func TestDelete_NotFound(t *testing.T) {
	h := newHarness(t)
	err := h.svc.Delete(context.Background(), uuid.NewString())
	assert.Equal(t, domain.ErrCodeNotFound, domain.CodeOf(err))
	assert.Equal(t, 0, countOf(h.store.calls.snapshot(), "store.RemoveDir"))
}

func TestDelete_AllowsDeletingARunningJob(t *testing.T) {
	h := newHarness(t)
	got, err := h.svc.Submit(context.Background(), submitBody(t, "a.wav", "d"))
	require.NoError(t, err)

	running, err := h.repo.Get(context.Background(), got.ID)
	require.NoError(t, err)
	running.Status = domain.StatusRunning
	h.repo.db.seed(running)

	assert.NoError(t, h.svc.Delete(context.Background(), got.ID))
}

// ---------------------------------------------------------------------------
// Cancel / Retry
// ---------------------------------------------------------------------------

func TestCancel_ConflictPassesThrough(t *testing.T) {
	h := newHarness(t)
	got, err := h.svc.Submit(context.Background(), submitBody(t, "a.wav", "d"))
	require.NoError(t, err)

	canceled, err := h.svc.Cancel(context.Background(), got.ID)
	require.NoError(t, err)
	assert.Equal(t, domain.StatusCanceled, canceled.Status)

	_, err = h.svc.Cancel(context.Background(), got.ID)
	assert.Equal(t, domain.ErrCodeConflict, domain.CodeOf(err), "canceling an already-terminal job must conflict")
}

func TestRetry_ConflictPassesThrough(t *testing.T) {
	h := newHarness(t)
	got, err := h.svc.Submit(context.Background(), submitBody(t, "a.wav", "d"))
	require.NoError(t, err)

	// Still pending: Requeue only accepts failed or canceled jobs.
	_, err = h.svc.Retry(context.Background(), got.ID)
	assert.Equal(t, domain.ErrCodeConflict, domain.CodeOf(err))
}

func TestRetry_Success(t *testing.T) {
	h := newHarness(t)
	got, err := h.svc.Submit(context.Background(), submitBody(t, "a.wav", "d"))
	require.NoError(t, err)

	row, err := h.repo.Get(context.Background(), got.ID)
	require.NoError(t, err)
	row.Status = domain.StatusFailed
	row.Error = domain.New(domain.ErrCodeToolFailure, "whisper-cli exited 1")
	h.repo.db.seed(row)

	retried, err := h.svc.Retry(context.Background(), got.ID)
	require.NoError(t, err)
	assert.Equal(t, domain.StatusPending, retried.Status)
	assert.Nil(t, retried.Error)
}

func TestPrioritize_PendingJobGetsPriorityAt(t *testing.T) {
	h := newHarness(t)
	got, err := h.svc.Submit(context.Background(), submitBody(t, "a.wav", "d"))
	require.NoError(t, err)

	prioritized, err := h.svc.Prioritize(context.Background(), got.ID)
	require.NoError(t, err)
	assert.Equal(t, domain.StatusPending, prioritized.Status)
	require.NotNil(t, prioritized.PriorityAt)
	assert.Equal(t, 1, countOf(h.calls.snapshot(), "repo.Prioritize"))
}

func TestPrioritize_FailedJobResetsRetryState(t *testing.T) {
	h := newHarness(t)
	got, err := h.svc.Submit(context.Background(), submitBody(t, "a.wav", "d"))
	require.NoError(t, err)

	row, err := h.repo.Get(context.Background(), got.ID)
	require.NoError(t, err)
	row.Status = domain.StatusFailed
	row.Attempts = 3
	row.Error = domain.New(domain.ErrCodeToolFailure, "whisper-cli exited 1")
	h.repo.db.seed(row)

	prioritized, err := h.svc.Prioritize(context.Background(), got.ID)
	require.NoError(t, err)
	assert.Equal(t, domain.StatusPending, prioritized.Status)
	assert.Equal(t, 0, prioritized.Attempts)
	assert.Nil(t, prioritized.Error)
	assert.NotNil(t, prioritized.PriorityAt)
}

func TestPrioritize_ConflictAndNotFoundPassThrough(t *testing.T) {
	h := newHarness(t)
	got, err := h.svc.Submit(context.Background(), submitBody(t, "a.wav", "d"))
	require.NoError(t, err)

	_, err = h.svc.Cancel(context.Background(), got.ID)
	require.NoError(t, err)
	_, err = h.svc.Prioritize(context.Background(), got.ID)
	assert.Equal(t, domain.ErrCodeConflict, domain.CodeOf(err), "a canceled job cannot get priority")

	_, err = h.svc.Prioritize(context.Background(), uuid.NewString())
	assert.Equal(t, domain.ErrCodeNotFound, domain.CodeOf(err))
}

// ---------------------------------------------------------------------------
// OpenSource
// ---------------------------------------------------------------------------

func TestOpenSource_Success(t *testing.T) {
	h := newHarness(t)
	got, err := h.svc.Submit(context.Background(), submitBody(t, "clip.wav", "audio-data"))
	require.NoError(t, err)

	rc, row, err := h.svc.OpenSource(context.Background(), got.ID)
	require.NoError(t, err)
	defer rc.Close()

	data, err := io.ReadAll(rc)
	require.NoError(t, err)
	assert.Equal(t, "audio-data", string(data))
	assert.Equal(t, got.ID, row.ID)
}

func TestOpenSource_MissingRow(t *testing.T) {
	h := newHarness(t)
	_, _, err := h.svc.OpenSource(context.Background(), uuid.NewString())
	assert.Equal(t, domain.ErrCodeNotFound, domain.CodeOf(err))
}

func TestOpenSource_MissingFile(t *testing.T) {
	h := newHarness(t)
	got, err := h.svc.Submit(context.Background(), submitBody(t, "clip.wav", "audio-data"))
	require.NoError(t, err)

	// The row survives but its stored file vanishes underneath it (e.g. an
	// operator manually cleared the volume) -- OpenSource must still report
	// ErrCodeNotFound, the same as a missing row.
	require.NoError(t, h.store.Remove(context.Background(), got.SourcePath))

	_, _, err = h.svc.OpenSource(context.Background(), got.ID)
	assert.Equal(t, domain.ErrCodeNotFound, domain.CodeOf(err))
}

// ---------------------------------------------------------------------------
// Invalid IDs short-circuit before the repository is touched
// ---------------------------------------------------------------------------

func TestInvalidID_ShortCircuitsBeforeRepository(t *testing.T) {
	h := newHarness(t)
	const bad = "not-a-uuid"

	_, err := h.svc.Get(context.Background(), bad)
	assert.Equal(t, domain.ErrCodeInvalidInput, domain.CodeOf(err))

	err = h.svc.Delete(context.Background(), bad)
	assert.Equal(t, domain.ErrCodeInvalidInput, domain.CodeOf(err))

	_, err = h.svc.Cancel(context.Background(), bad)
	assert.Equal(t, domain.ErrCodeInvalidInput, domain.CodeOf(err))

	_, err = h.svc.Retry(context.Background(), bad)
	assert.Equal(t, domain.ErrCodeInvalidInput, domain.CodeOf(err))

	_, err = h.svc.Prioritize(context.Background(), bad)
	assert.Equal(t, domain.ErrCodeInvalidInput, domain.CodeOf(err))

	_, _, err = h.svc.OpenSource(context.Background(), bad)
	assert.Equal(t, domain.ErrCodeInvalidInput, domain.CodeOf(err))

	calls := h.calls.snapshot()
	assert.Equal(t, 0, countOf(calls, "repo.Get"))
	assert.Equal(t, 0, countOf(calls, "repo.Delete"))
	assert.Equal(t, 0, countOf(calls, "repo.RequestCancel"))
	assert.Equal(t, 0, countOf(calls, "repo.Requeue"))
	assert.Equal(t, 0, countOf(calls, "repo.Prioritize"))
}

// ---------------------------------------------------------------------------
// memDB/fakeQueue fidelity: app.Service itself never drives Queue (see
// Deps.Queue's own doc), but memDB backs both fakeRepository and fakeQueue
// on the SAME rows, exactly like one Postgres table backs both
// app.Repository and app.Queue in production -- so if this fake drifts from
// the real Store's cancel_requested handling (CONTRACT.md, and
// postgres/queue.go's own doc), a future internal/jobs- or
// internal/httpapi-level test built on this same fake would silently pass
// against behaviour the real database does not have. These three tests pin
// exactly the three places that had drifted.
// ---------------------------------------------------------------------------

// TestFakeQueue_Claim_ExcludesCancelRequestedPendingRow proves memDB.claim
// mirrors the real Claim's `AND NOT cancel_requested` predicate: a pending
// row still flagged cancel_requested -- the state left behind when a
// retryable failure races RequestCancel and the worker's Finish loses that
// race, landing back on pending with the flag still set -- must never be
// claimed again.
func TestFakeQueue_Claim_ExcludesCancelRequestedPendingRow(t *testing.T) {
	h := newHarness(t)
	got, err := h.svc.Submit(context.Background(), submitBody(t, "a.wav", "data"))
	require.NoError(t, err)

	claimed, err := h.queue.Claim(context.Background(), "w1", time.Minute)
	require.NoError(t, err)
	require.NotNil(t, claimed)
	require.NotNil(t, claimed.LeaseToken)

	_, err = h.repo.RequestCancel(context.Background(), got.ID)
	require.NoError(t, err)

	// A retryable failure lands the row back on pending before the worker
	// ever notices the cancellation via Heartbeat -- see RequestCancel's own
	// doc on why a running job is merely flagged, not resolved on the spot.
	next := h.clock.Now()
	require.NoError(t, h.queue.Finish(context.Background(), domain.FinishRequest{
		ID: got.ID, LeaseToken: *claimed.LeaseToken, Status: domain.StatusPending,
		NextAttemptAt: &next,
	}))

	again, err := h.queue.Claim(context.Background(), "w2", time.Minute)
	require.NoError(t, err)
	assert.Nil(t, again, "a cancel-requested pending row must never be claimed")
}

// TestFakeQueue_Finish_SucceededOnCancelRequestedRowRecordsCanceled proves
// memDB.finish mirrors the real Store.Finish's succeeded->canceled
// downgrade: a job the API already answered 200 to a cancel for must never
// be recorded succeeded, even when the worker's own outcome -- computed
// before it noticed the cancellation -- was a genuine success.
func TestFakeQueue_Finish_SucceededOnCancelRequestedRowRecordsCanceled(t *testing.T) {
	h := newHarness(t)
	got, err := h.svc.Submit(context.Background(), submitBody(t, "a.wav", "data"))
	require.NoError(t, err)

	claimed, err := h.queue.Claim(context.Background(), "w1", time.Minute)
	require.NoError(t, err)
	require.NotNil(t, claimed.LeaseToken)

	_, err = h.repo.RequestCancel(context.Background(), got.ID)
	require.NoError(t, err)

	require.NoError(t, h.queue.Finish(context.Background(), domain.FinishRequest{
		ID: got.ID, LeaseToken: *claimed.LeaseToken, Status: domain.StatusSucceeded,
		Result: &domain.Result{Text: "must never be recorded"},
	}))

	row, err := h.repo.Get(context.Background(), got.ID)
	require.NoError(t, err)
	assert.Equal(t, domain.StatusCanceled, row.Status, "a cancel-requested row must never be recorded succeeded")
}

// TestFakeQueue_ReleaseExpired_ReapsCancelRequestedPendingRow proves
// memDB.releaseExpired mirrors the real ReleaseExpired's second job: a
// pending row still flagged cancel_requested -- unclaimable by claim's own
// predicate above, but never actually resolved either -- is reaped straight
// to StatusCanceled, counted in the same returned total as ordinary lease
// recoveries.
func TestFakeQueue_ReleaseExpired_ReapsCancelRequestedPendingRow(t *testing.T) {
	h := newHarness(t)
	got, err := h.svc.Submit(context.Background(), submitBody(t, "a.wav", "data"))
	require.NoError(t, err)

	claimed, err := h.queue.Claim(context.Background(), "w1", time.Minute)
	require.NoError(t, err)
	require.NotNil(t, claimed.LeaseToken)

	_, err = h.repo.RequestCancel(context.Background(), got.ID)
	require.NoError(t, err)

	next := h.clock.Now().Add(-time.Minute) // already due
	require.NoError(t, h.queue.Finish(context.Background(), domain.FinishRequest{
		ID: got.ID, LeaseToken: *claimed.LeaseToken, Status: domain.StatusPending,
		NextAttemptAt: &next,
	}))

	n, err := h.queue.ReleaseExpired(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, n, "ReleaseExpired must count the cancellation reap in its total")

	row, err := h.repo.Get(context.Background(), got.ID)
	require.NoError(t, err)
	assert.Equal(t, domain.StatusCanceled, row.Status)

	again, err := h.queue.Claim(context.Background(), "w2", time.Minute)
	require.NoError(t, err)
	assert.Nil(t, again, "a canceled job must never be claimable")
}
