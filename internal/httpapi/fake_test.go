// Package httpapi_test exercises internal/httpapi's router against a
// hand-written fake Service (no mocking library, per CONTRACT.md section
// 11), driven through httptest.NewServer/httptest.NewRecorder against the
// real router -- exactly as BLUEPRINT's own httpapi tests do.
package httpapi_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/trace/noop"

	"github.com/pablofornell/helios-audio-transcription-service/internal/app"
	"github.com/pablofornell/helios-audio-transcription-service/internal/domain"
	"github.com/pablofornell/helios-audio-transcription-service/internal/httpapi"
	"github.com/pablofornell/helios-audio-transcription-service/internal/observability"
)

// fakeService is a scriptable httpapi.Service: every test sets only the
// function fields it needs, and an unset field returns a sane zero-value
// default so handlers whose response shape a given test does not care
// about still get something reasonable back.
type fakeService struct {
	submitFn     func(ctx context.Context, req app.SubmitRequest) (*domain.Transcription, error)
	getFn        func(ctx context.Context, id string) (*domain.Transcription, error)
	listFn       func(ctx context.Context, f app.ListFilter) (app.Page, error)
	deleteFn     func(ctx context.Context, id string) error
	cancelFn     func(ctx context.Context, id string) (*domain.Transcription, error)
	retryFn      func(ctx context.Context, id string) (*domain.Transcription, error)
	prioritizeFn func(ctx context.Context, id string) (*domain.Transcription, error)
	openSourceFn func(ctx context.Context, id string) (io.ReadSeekCloser, *domain.Transcription, error)
}

var _ httpapi.Service = (*fakeService)(nil)

func (f *fakeService) Submit(ctx context.Context, req app.SubmitRequest) (*domain.Transcription, error) {
	if f.submitFn != nil {
		return f.submitFn(ctx, req)
	}
	return &domain.Transcription{ID: "generated-id", Status: domain.StatusPending}, nil
}

func (f *fakeService) Get(ctx context.Context, id string) (*domain.Transcription, error) {
	if f.getFn != nil {
		return f.getFn(ctx, id)
	}
	return &domain.Transcription{ID: id}, nil
}

func (f *fakeService) List(ctx context.Context, filter app.ListFilter) (app.Page, error) {
	if f.listFn != nil {
		return f.listFn(ctx, filter)
	}
	return app.Page{}, nil
}

func (f *fakeService) Delete(ctx context.Context, id string) error {
	if f.deleteFn != nil {
		return f.deleteFn(ctx, id)
	}
	return nil
}

func (f *fakeService) Cancel(ctx context.Context, id string) (*domain.Transcription, error) {
	if f.cancelFn != nil {
		return f.cancelFn(ctx, id)
	}
	return &domain.Transcription{ID: id}, nil
}

func (f *fakeService) Retry(ctx context.Context, id string) (*domain.Transcription, error) {
	if f.retryFn != nil {
		return f.retryFn(ctx, id)
	}
	return &domain.Transcription{ID: id}, nil
}

func (f *fakeService) Prioritize(ctx context.Context, id string) (*domain.Transcription, error) {
	if f.prioritizeFn != nil {
		return f.prioritizeFn(ctx, id)
	}
	return &domain.Transcription{ID: id}, nil
}

func (f *fakeService) OpenSource(ctx context.Context, id string) (io.ReadSeekCloser, *domain.Transcription, error) {
	if f.openSourceFn != nil {
		return f.openSourceFn(ctx, id)
	}
	return nil, nil, domain.New(domain.ErrCodeNotFound, "no source")
}

// fakeReadSeekCloser wraps an io.ReadSeeker with a Close that records
// whether it was called, so tests can assert getAudio closes the reader
// it was handed.
type fakeReadSeekCloser struct {
	io.ReadSeeker
	closed bool
}

func (f *fakeReadSeekCloser) Close() error {
	f.closed = true
	return nil
}

// fakeChecker is a scriptable observability.Checker: Check returns err
// (nil by default, meaning healthy).
type fakeChecker struct {
	name string
	err  error
}

func (c fakeChecker) Name() string                    { return c.name }
func (c fakeChecker) Check(ctx context.Context) error { return c.err }

// sampleTranscription returns a fully populated *domain.Transcription
// suitable for asserting DTO shape: every field the wire contract exposes
// is non-zero, and every field it must NOT expose (SourcePath, LeaseOwner,
// LeaseExpiresAt, NextAttemptAt) is also set -- so a bug that leaks one of
// those into the response shows up as a positive match instead of hiding
// behind an already-empty fixture.
func sampleTranscription() *domain.Transcription {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	started := now.Add(time.Second)
	completed := now.Add(time.Minute)
	next := now.Add(2 * time.Minute)
	owner := "worker-1"
	leaseExp := now.Add(90 * time.Second)

	return &domain.Transcription{
		ID:     "11111111-1111-4111-8111-111111111111",
		Status: domain.StatusSucceeded,
		Options: domain.Options{
			Model:    "large-v3-turbo-q5_0",
			Language: "en",
			Task:     domain.TaskTranscribe,
			BeamSize: 5,
		},
		Audio: domain.Audio{
			Filename:        "clip.mp3",
			MediaType:       "audio/mpeg",
			SizeBytes:       2048,
			DurationSeconds: 12.5,
			SampleRateHz:    16000,
			Channels:        1,
			Codec:           "mp3",
		},
		SourcePath: "transcriptions/11111111-1111-4111-8111-111111111111/source.mp3",
		Result: &domain.Result{
			Text:            "hello world",
			Language:        "en",
			DurationSeconds: 12.5,
			Model:           "large-v3-turbo-q5_0",
			Task:            domain.TaskTranscribe,
			Segments: []domain.Segment{
				{ID: 0, Start: 0, End: 1.2, Text: "hello world"},
			},
		},
		Attempts:       1,
		MaxAttempts:    3,
		CreatedAt:      now,
		UpdatedAt:      now,
		StartedAt:      &started,
		CompletedAt:    &completed,
		NextAttemptAt:  &next,
		LeaseOwner:     &owner,
		LeaseExpiresAt: &leaseExp,
	}
}

// testDeps builds a minimal, valid httpapi.RouterDeps: a fresh Prometheus
// registry (so tests never collide on metric names), a logger writing to
// io.Discard, a no-op tracer provider, and a generous RouteTimeout so
// ordinary handler tests are never at risk of tripping chimw.Timeout by
// accident.
func testDeps(svc httpapi.Service, health *observability.Registry, metricsEnabled bool) httpapi.RouterDeps {
	return httpapi.RouterDeps{
		Service:        svc,
		Health:         health,
		Metrics:        observability.NewMetrics(prometheus.NewRegistry()),
		Logger:         slog.New(slog.NewTextHandler(io.Discard, nil)),
		TracerProvider: noop.NewTracerProvider(),
		MaxUploadBytes: 10 << 20, // 10 MiB: generous for tests, never actually large uploads.
		RouteTimeout:   5 * time.Second,
		MetricsEnabled: metricsEnabled,
	}
}

// errorEnvelope decodes the {"error":{"code","message","details"}} body
// writeError produces, without depending on httpapi's unexported DTO
// types.
type errorEnvelope struct {
	Error struct {
		Code    string         `json:"code"`
		Message string         `json:"message"`
		Details map[string]any `json:"details"`
	} `json:"error"`
}

// --- submit -----------------------------------------------------------

func TestSubmitTranscription_MultipartStreaming(t *testing.T) {
	var capturedReq app.SubmitRequest
	var capturedBody []byte

	svc := &fakeService{
		submitFn: func(ctx context.Context, req app.SubmitRequest) (*domain.Transcription, error) {
			capturedReq = req
			data, err := io.ReadAll(req.Body)
			require.NoError(t, err)
			capturedBody = data
			return &domain.Transcription{ID: "abc-123", Status: domain.StatusPending}, nil
		},
	}

	srv := httptest.NewServer(httpapi.NewRouter(testDeps(svc, observability.NewRegistry(), false)))
	defer srv.Close()

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)

	optPart, err := mw.CreateFormField("options")
	require.NoError(t, err)
	_, err = optPart.Write([]byte(`{"model":"large-v3-turbo-q5_0","language":"en"}`))
	require.NoError(t, err)

	fw, err := mw.CreateFormFile("file", "clip.mp3")
	require.NoError(t, err)
	wantBody := bytes.Repeat([]byte("abcd"), 1000)
	_, err = fw.Write(wantBody)
	require.NoError(t, err)
	require.NoError(t, mw.Close())

	req, err := http.NewRequest(http.MethodPost, srv.URL+"/api/v1/transcriptions", &buf)
	require.NoError(t, err)
	req.Header.Set("Content-Type", mw.FormDataContentType())

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusAccepted, resp.StatusCode)
	assert.Equal(t, "/api/v1/transcriptions/abc-123", resp.Header.Get("Location"))

	var body struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	assert.Equal(t, "abc-123", body.ID)
	assert.Equal(t, "pending", body.Status)

	assert.Equal(t, wantBody, capturedBody, "the streamed file part must reach Service.Submit's Body byte for byte")
	assert.Equal(t, "clip.mp3", capturedReq.Filename)
	assert.Equal(t, "large-v3-turbo-q5_0", capturedReq.Options.Model)
	assert.Equal(t, "en", capturedReq.Options.Language)
}

func TestSubmitTranscription_MalformedOptionsJSON(t *testing.T) {
	called := false
	svc := &fakeService{
		submitFn: func(ctx context.Context, req app.SubmitRequest) (*domain.Transcription, error) {
			called = true
			return &domain.Transcription{}, nil
		},
	}

	srv := httptest.NewServer(httpapi.NewRouter(testDeps(svc, observability.NewRegistry(), false)))
	defer srv.Close()

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	optPart, err := mw.CreateFormField("options")
	require.NoError(t, err)
	_, err = optPart.Write([]byte(`{not valid json`))
	require.NoError(t, err)
	fw, err := mw.CreateFormFile("file", "clip.mp3")
	require.NoError(t, err)
	_, err = fw.Write(bytes.Repeat([]byte("x"), 1<<16))
	require.NoError(t, err)
	require.NoError(t, mw.Close())

	req, err := http.NewRequest(http.MethodPost, srv.URL+"/api/v1/transcriptions", &buf)
	require.NoError(t, err)
	req.Header.Set("Content-Type", mw.FormDataContentType())

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	assert.False(t, called, "Service.Submit must never be called -- and so the file part never streamed -- when options JSON is malformed")

	var envelope errorEnvelope
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&envelope))
	assert.Equal(t, "invalid_input", envelope.Error.Code)
}

// lateOptionsCapturer mirrors app's unexported lateOptionsProvider
// interface structurally (Go interface satisfaction does not care which
// package declares it), so this test can prove req.Body -- as handed to
// Service.Submit -- actually carries the `options` part discovered after
// `file`, exactly as internal/app/service.go's Submit consults it.
type lateOptionsCapturer interface {
	LateOptions() (domain.Options, bool, error)
}

func TestSubmitTranscription_OptionsAfterFileIsHonoured(t *testing.T) {
	// README documents `-F file=@x -F options=...` -- file first, options
	// second, which is exactly the order curl sends and the order the
	// unfixed handler dropped silently (it returned as soon as it saw
	// `file`, having already committed to whatever opts were known so
	// far -- here, none). See D3.
	var capturedReq app.SubmitRequest
	var capturedBody []byte
	var lateOpts domain.Options
	var lateFound bool

	svc := &fakeService{
		submitFn: func(ctx context.Context, req app.SubmitRequest) (*domain.Transcription, error) {
			capturedReq = req
			data, err := io.ReadAll(req.Body)
			require.NoError(t, err)
			capturedBody = data
			if lp, ok := req.Body.(lateOptionsCapturer); ok {
				var lerr error
				lateOpts, lateFound, lerr = lp.LateOptions()
				require.NoError(t, lerr)
			}
			return &domain.Transcription{ID: "abc-123", Status: domain.StatusPending}, nil
		},
	}

	srv := httptest.NewServer(httpapi.NewRouter(testDeps(svc, observability.NewRegistry(), false)))
	defer srv.Close()

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)

	fw, err := mw.CreateFormFile("file", "clip.mp3")
	require.NoError(t, err)
	wantBody := bytes.Repeat([]byte("abcd"), 1000)
	_, err = fw.Write(wantBody)
	require.NoError(t, err)

	optPart, err := mw.CreateFormField("options")
	require.NoError(t, err)
	_, err = optPart.Write([]byte(`{"model":"large-v3-turbo-q5_0","task":"translate","wordTimestamps":true}`))
	require.NoError(t, err)
	require.NoError(t, mw.Close())

	req, err := http.NewRequest(http.MethodPost, srv.URL+"/api/v1/transcriptions", &buf)
	require.NoError(t, err)
	req.Header.Set("Content-Type", mw.FormDataContentType())

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	require.Equal(t, http.StatusAccepted, resp.StatusCode)
	assert.Equal(t, "clip.mp3", capturedReq.Filename)
	assert.Equal(t, wantBody, capturedBody,
		"the file part must still stream to Service.Submit byte for byte even when `options` arrives after it")
	assert.True(t, lateFound, "an `options` part arriving after `file` must be discovered, not silently ignored")
	assert.Equal(t, "large-v3-turbo-q5_0", lateOpts.Model)
	assert.Equal(t, domain.TaskTranslate, lateOpts.Task)
	assert.True(t, lateOpts.WordTimestamps)
}

func TestSubmitTranscription_CanceledUploadIsNotInternalError(t *testing.T) {
	// D4: a client disconnect mid-upload must map to 499 (ErrCodeCanceled),
	// never the 500 classifyStoreErr used to produce before it learned to
	// check errors.Is(err, context.Canceled) ahead of its ErrCodeInternal
	// fallback -- see internal/app/service_test.go for the fix itself.
	// This proves the HTTP layer reports it correctly once Service returns
	// it classified.
	svc := &fakeService{
		submitFn: func(ctx context.Context, req app.SubmitRequest) (*domain.Transcription, error) {
			_, _ = io.ReadAll(req.Body)
			return nil, domain.Wrap(domain.ErrCodeCanceled, "upload canceled", context.Canceled)
		},
	}
	srv := httptest.NewServer(httpapi.NewRouter(testDeps(svc, observability.NewRegistry(), false)))
	defer srv.Close()

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, err := mw.CreateFormFile("file", "clip.mp3")
	require.NoError(t, err)
	_, err = fw.Write([]byte("audio"))
	require.NoError(t, err)
	require.NoError(t, mw.Close())

	req, err := http.NewRequest(http.MethodPost, srv.URL+"/api/v1/transcriptions", &buf)
	require.NoError(t, err)
	req.Header.Set("Content-Type", mw.FormDataContentType())

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, 499, resp.StatusCode, "a canceled upload must never be reported as a 500")

	var envelope errorEnvelope
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&envelope))
	assert.Equal(t, "canceled", envelope.Error.Code)
}

// slowReader paces Read to return at most chunkBytes every delay, so a
// test can drive an upload that takes a deterministic, controllable amount
// of wall time without needing a real slow network.
type slowReader struct {
	r     io.Reader
	delay time.Duration
}

const slowReaderChunkBytes = 256

func (s *slowReader) Read(p []byte) (int, error) {
	time.Sleep(s.delay)
	if len(p) > slowReaderChunkBytes {
		p = p[:slowReaderChunkBytes]
	}
	return s.r.Read(p)
}

// slowReadSeekCloser is slowReader's mirror for the download side: an
// io.ReadSeekCloser (what Service.OpenSource returns) whose Read is paced
// the same way, so a getAudio test can drive a download that takes a
// deterministic, controllable amount of wall time. Seek is unpaced --
// http.ServeContent uses it only to find the content length and to seek to
// a Range's start, never in the timed copy loop itself -- and Close simply
// satisfies io.Closer, since no test here needs to observe it.
type slowReadSeekCloser struct {
	*bytes.Reader
	delay time.Duration
}

func (s *slowReadSeekCloser) Read(p []byte) (int, error) {
	time.Sleep(s.delay)
	if len(p) > slowReaderChunkBytes {
		p = p[:slowReaderChunkBytes]
	}
	return s.Reader.Read(p)
}

func (s *slowReadSeekCloser) Close() error { return nil }

func TestSubmitTranscription_SlowUploadSurvivesShortServerWriteTimeout(t *testing.T) {
	// D2: net/http's http.Server.WriteTimeout starts counting the instant
	// headers are read and covers the WHOLE request -- including the body
	// this handler is still reading -- not just the time actually spent
	// writing a response. A merely slow (never stuck) upload would
	// otherwise have its connection severed before the 202 ever reaches
	// the client, who then retries and stores an orphan copy no one can
	// find. submitTranscription's own SetWriteDeadline extension (sized
	// off RouteTimeout) must let a slow-but-legitimate upload survive a
	// server-level WriteTimeout far shorter than the upload actually
	// takes.
	svc := &fakeService{
		submitFn: func(ctx context.Context, req app.SubmitRequest) (*domain.Transcription, error) {
			_, err := io.ReadAll(req.Body)
			require.NoError(t, err)
			return &domain.Transcription{ID: "slow-upload", Status: domain.StatusPending}, nil
		},
	}

	deps := testDeps(svc, observability.NewRegistry(), false)
	deps.RouteTimeout = 2 * time.Second // what the upload route's write deadline is extended to

	srv := httptest.NewUnstartedServer(httpapi.NewRouter(deps))
	srv.Config.WriteTimeout = 150 * time.Millisecond // far shorter than the paced upload below
	srv.Start()
	defer srv.Close()

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, err := mw.CreateFormFile("file", "clip.mp3")
	require.NoError(t, err)
	_, err = fw.Write(bytes.Repeat([]byte("x"), 4096))
	require.NoError(t, err)
	require.NoError(t, mw.Close())

	// ~16 chunks * 30ms = ~480ms: well past the 150ms server WriteTimeout
	// above, comfortably inside the 2s the fix extends it to.
	body := &slowReader{r: bytes.NewReader(buf.Bytes()), delay: 30 * time.Millisecond}
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/api/v1/transcriptions", body)
	require.NoError(t, err)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.ContentLength = int64(buf.Len())

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err, "the slow-but-legitimate upload must not have its connection severed mid-stream")
	defer resp.Body.Close()

	assert.Equal(t, http.StatusAccepted, resp.StatusCode)
}

func TestSubmitTranscription_MissingFilePart(t *testing.T) {
	svc := &fakeService{}
	srv := httptest.NewServer(httpapi.NewRouter(testDeps(svc, observability.NewRegistry(), false)))
	defer srv.Close()

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	optPart, err := mw.CreateFormField("options")
	require.NoError(t, err)
	_, err = optPart.Write([]byte(`{}`))
	require.NoError(t, err)
	require.NoError(t, mw.Close())

	req, err := http.NewRequest(http.MethodPost, srv.URL+"/api/v1/transcriptions", &buf)
	require.NoError(t, err)
	req.Header.Set("Content-Type", mw.FormDataContentType())

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)

	var envelope errorEnvelope
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&envelope))
	assert.Equal(t, "invalid_input", envelope.Error.Code)
	assert.Equal(t, "file", envelope.Error.Details["field"])
}

// --- error code -> status mapping --------------------------------------

func TestWriteError_StatusForEveryCode(t *testing.T) {
	// One entry per domain.ErrorCode defined in CONTRACT.md section 6 --
	// this table's length matching the taxonomy's is itself a check that
	// nothing was missed.
	cases := []struct {
		code domain.ErrorCode
		want int
	}{
		{domain.ErrCodeInvalidInput, http.StatusBadRequest},
		{domain.ErrCodeUnsupportedMedia, http.StatusUnsupportedMediaType},
		{domain.ErrCodeNoAudioStream, http.StatusUnprocessableEntity},
		{domain.ErrCodePayloadTooLarge, http.StatusRequestEntityTooLarge},
		{domain.ErrCodeNotFound, http.StatusNotFound},
		{domain.ErrCodeConflict, http.StatusConflict},
		{domain.ErrCodeDiskFull, http.StatusInsufficientStorage},
		{domain.ErrCodeModelUnavailable, http.StatusServiceUnavailable},
		{domain.ErrCodeToolFailure, http.StatusBadGateway},
		{domain.ErrCodeTimeout, http.StatusGatewayTimeout},
		{domain.ErrCodeCanceled, 499},
		{domain.ErrCodeInternal, http.StatusInternalServerError},
	}

	for _, tc := range cases {
		t.Run(string(tc.code), func(t *testing.T) {
			svc := &fakeService{
				getFn: func(ctx context.Context, id string) (*domain.Transcription, error) {
					return nil, domain.New(tc.code, "boom")
				},
			}
			r := httpapi.NewRouter(testDeps(svc, observability.NewRegistry(), false))

			req := httptest.NewRequest(http.MethodGet, "/api/v1/transcriptions/some-id", nil)
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, req)

			assert.Equal(t, tc.want, rec.Code)

			var envelope errorEnvelope
			require.NoError(t, json.NewDecoder(rec.Body).Decode(&envelope))
			assert.Equal(t, string(tc.code), envelope.Error.Code)
		})
	}
}

// --- list ---------------------------------------------------------------

func TestListTranscriptions_FiltersAndBadLimit(t *testing.T) {
	var captured app.ListFilter
	svc := &fakeService{
		listFn: func(ctx context.Context, f app.ListFilter) (app.Page, error) {
			captured = f
			return app.Page{Items: []*domain.Transcription{sampleTranscription()}, NextCursor: "next-cursor"}, nil
		},
	}
	r := httpapi.NewRouter(testDeps(svc, observability.NewRegistry(), false))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/transcriptions?library=true&status=succeeded&cursor=abc&limit=5", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, domain.StatusSucceeded, captured.Status)
	assert.Equal(t, "abc", captured.Cursor)
	assert.Equal(t, 5, captured.Limit)
	assert.True(t, captured.Library)

	var body struct {
		Items      []map[string]any `json:"items"`
		NextCursor string           `json:"nextCursor"`
	}
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&body))
	assert.Equal(t, "next-cursor", body.NextCursor)
	assert.Len(t, body.Items, 1)

	for _, query := range []string{"library=", "library=1", "library=yes", "library=true&library=false"} {
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/transcriptions?"+query, nil))
		assert.Equal(t, http.StatusBadRequest, rec.Code)
	}

	// A non-numeric limit is invalid_input.
	req2 := httptest.NewRequest(http.MethodGet, "/api/v1/transcriptions?limit=not-a-number", nil)
	rec2 := httptest.NewRecorder()
	r.ServeHTTP(rec2, req2)
	assert.Equal(t, http.StatusBadRequest, rec2.Code)

	// An unrecognised status is also invalid_input.
	req3 := httptest.NewRequest(http.MethodGet, "/api/v1/transcriptions?status=bogus", nil)
	rec3 := httptest.NewRecorder()
	r.ServeHTTP(rec3, req3)
	assert.Equal(t, http.StatusBadRequest, rec3.Code)
}

// --- transcript -----------------------------------------------------------

func TestGetTranscript_Formats(t *testing.T) {
	succeeded := sampleTranscription()
	succeeded.Audio.Filename = "My.interview.mp3"

	cases := []struct {
		format      string
		contentType string
		disposition bool
	}{
		{"", "application/json", false},
		{"json", "application/json", false},
		{"txt", "text/plain; charset=utf-8", true},
		{"srt", "application/x-subrip; charset=utf-8", true},
		{"vtt", "text/vtt; charset=utf-8", true},
	}

	for _, tc := range cases {
		t.Run(tc.format, func(t *testing.T) {
			svc := &fakeService{
				getFn: func(ctx context.Context, id string) (*domain.Transcription, error) {
					return succeeded, nil
				},
			}
			r := httpapi.NewRouter(testDeps(svc, observability.NewRegistry(), false))

			url := "/api/v1/transcriptions/" + succeeded.ID + "/transcript"
			if tc.format != "" {
				url += "?format=" + tc.format
			}
			req := httptest.NewRequest(http.MethodGet, url, nil)
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, req)

			require.Equal(t, http.StatusOK, rec.Code)
			assert.Equal(t, tc.contentType, rec.Header().Get("Content-Type"))
			if tc.disposition {
				disp := rec.Header().Get("Content-Disposition")
				assert.Contains(t, disp, "attachment")
				assert.Contains(t, disp, `filename="My.interview-transcription.`+tc.format+`"`)
			} else {
				assert.Empty(t, rec.Header().Get("Content-Disposition"))
			}
			assert.NotEmpty(t, rec.Body.Bytes())
		})
	}
}

func TestGetTranscript_ConflictWhenUnfinished(t *testing.T) {
	pending := sampleTranscription()
	pending.Status = domain.StatusPending
	pending.Result = nil

	svc := &fakeService{
		getFn: func(ctx context.Context, id string) (*domain.Transcription, error) {
			return pending, nil
		},
	}
	r := httpapi.NewRouter(testDeps(svc, observability.NewRegistry(), false))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/transcriptions/"+pending.ID+"/transcript", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusConflict, rec.Code)
}

func TestGetTranscript_UnknownFormatIsBadRequest(t *testing.T) {
	svc := &fakeService{}
	r := httpapi.NewRouter(testDeps(svc, observability.NewRegistry(), false))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/transcriptions/some-id/transcript?format=xml", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

// TestGetTranscript_DownloadQueryParam pins ?download=1's opt-in effect on
// json specifically (txt/srt/vtt already carry Content-Disposition
// regardless, per TestGetTranscript_Formats above), and that an
// unrecognised value is silently treated the same as absent rather than
// rejected -- see parseDownloadQuery's own doc for why.
func TestGetTranscript_DownloadQueryParam(t *testing.T) {
	succeeded := sampleTranscription()
	succeeded.Audio.Filename = "My interview.mp3"

	cases := []struct {
		name        string
		format      string
		download    string
		disposition bool
	}{
		{"json without download is unchanged", "json", "", false},
		{"json with download=1 becomes an attachment", "json", "1", true},
		{"json with download=true becomes an attachment", "json", "true", true},
		{"json with an unrecognised download value is ignored", "json", "yes", false},
		{"txt without download is already an attachment", "txt", "", true},
		{"txt with download=1 stays an attachment", "txt", "1", true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc := &fakeService{
				getFn: func(ctx context.Context, id string) (*domain.Transcription, error) {
					return succeeded, nil
				},
			}
			r := httpapi.NewRouter(testDeps(svc, observability.NewRegistry(), false))

			url := "/api/v1/transcriptions/" + succeeded.ID + "/transcript?format=" + tc.format
			if tc.download != "" {
				url += "&download=" + tc.download
			}
			req := httptest.NewRequest(http.MethodGet, url, nil)
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, req)

			require.Equal(t, http.StatusOK, rec.Code)
			disp := rec.Header().Get("Content-Disposition")
			if tc.disposition {
				assert.Contains(t, disp, "attachment")
				assert.Contains(t, disp, `filename="My interview-transcription.`+tc.format+`"`)
			} else {
				assert.Empty(t, disp)
			}
		})
	}
}

// --- audio ------------------------------------------------------------

func TestGetAudio_ServesContentAndHonoursRange(t *testing.T) {
	content := []byte("0123456789abcdef")
	tr := sampleTranscription()

	var lastRSC *fakeReadSeekCloser
	svc := &fakeService{
		openSourceFn: func(ctx context.Context, id string) (io.ReadSeekCloser, *domain.Transcription, error) {
			lastRSC = &fakeReadSeekCloser{ReadSeeker: bytes.NewReader(content)}
			return lastRSC, tr, nil
		},
	}
	r := httpapi.NewRouter(testDeps(svc, observability.NewRegistry(), false))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/transcriptions/"+tr.ID+"/audio", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, content, rec.Body.Bytes())
	assert.Equal(t, tr.Audio.MediaType, rec.Header().Get("Content-Type"))
	require.NotNil(t, lastRSC)
	assert.True(t, lastRSC.closed, "getAudio must close the reader Service.OpenSource returned")

	// A Range request is honoured because the handler goes through
	// http.ServeContent rather than io.Copy.
	req2 := httptest.NewRequest(http.MethodGet, "/api/v1/transcriptions/"+tr.ID+"/audio", nil)
	req2.Header.Set("Range", "bytes=2-5")
	rec2 := httptest.NewRecorder()
	r.ServeHTTP(rec2, req2)

	require.Equal(t, http.StatusPartialContent, rec2.Code)
	assert.Equal(t, content[2:6], rec2.Body.Bytes())
}

func TestGetAudio_NonAudioMediaTypeIsServedAsOctetStream(t *testing.T) {
	// D5: an uploaded HTML/SVG payload must never be echoed back with a
	// Content-Type a browser would render -- always nosniff, always
	// attachment, and any stored MediaType outside audio/* or video/* is
	// downgraded to application/octet-stream regardless of what the
	// uploading caller's multipart part claimed.
	content := []byte("<script>alert(1)</script>")
	tr := sampleTranscription()
	tr.Audio.MediaType = "text/html; charset=utf-8"
	tr.Audio.Filename = `evil".mp3`

	svc := &fakeService{
		openSourceFn: func(ctx context.Context, id string) (io.ReadSeekCloser, *domain.Transcription, error) {
			return &fakeReadSeekCloser{ReadSeeker: bytes.NewReader(content)}, tr, nil
		},
	}
	r := httpapi.NewRouter(testDeps(svc, observability.NewRegistry(), false))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/transcriptions/"+tr.ID+"/audio", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "application/octet-stream", rec.Header().Get("Content-Type"))
	assert.Equal(t, "nosniff", rec.Header().Get("X-Content-Type-Options"))

	disp := rec.Header().Get("Content-Disposition")
	assert.Contains(t, disp, "attachment")
	// The filename's embedded '"' must be escaped, not left to break out
	// of the quoted-string and forge extra Content-Disposition parameters.
	assert.Contains(t, disp, `filename="evil\".mp3"`)
}

// TestGetAudio_StreamingNotCanceledByRouteTimeout is Problem 1's first
// protection, narrowed to the phase it actually covers: mountAPI does not
// wrap GET .../audio in chimw.Timeout(routeTimeout), so once
// Service.OpenSource has returned (bounded on its own by
// h.audioOpenTimeout -- see TestGetAudio_OpenSourceTimeoutMaps504GatewayTimeout
// below for THAT phase), http.ServeContent's own streaming is never given
// chi's fixed-duration deadline, however small RouteTimeout is configured.
// content here is paced (slowReadSeekCloser) to take several multiples of
// an intentionally tiny RouteTimeout to fully stream: had this route still
// been in chimw.Timeout's Group, the response would have been cut off with
// a 504 partway through instead of completing.
//
// This only pins chimw.Timeout's own absence from the streaming phase, not
// every possible source of context cancellation -- see getAudio's own doc:
// the request context is still ultimately tied to the underlying
// connection by net/http itself (it is canceled when the connection
// drops), a separate concern this test does not exercise.
func TestGetAudio_StreamingNotCanceledByRouteTimeout(t *testing.T) {
	content := bytes.Repeat([]byte("x"), 4096)
	tr := sampleTranscription()

	const routeTimeout = 20 * time.Millisecond
	svc := &fakeService{
		openSourceFn: func(ctx context.Context, id string) (io.ReadSeekCloser, *domain.Transcription, error) {
			// OpenSource itself returns immediately -- only the stream
			// that follows is meant to outlast routeTimeout here.
			return &slowReadSeekCloser{Reader: bytes.NewReader(content), delay: 5 * time.Millisecond}, tr, nil
		},
	}

	deps := testDeps(svc, observability.NewRegistry(), false)
	deps.RouteTimeout = routeTimeout

	srv := httptest.NewServer(httpapi.NewRouter(deps))
	defer srv.Close()

	// len(content)/slowReaderChunkBytes chunks * 5ms is ~80ms, several
	// multiples of the 20ms RouteTimeout above -- comfortably enough to
	// have tripped chimw.Timeout partway through, had this route still
	// been in its Group.
	resp, err := http.Get(srv.URL + "/api/v1/transcriptions/" + tr.ID + "/audio")
	require.NoError(t, err)
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, content, body)
}

// TestGetAudio_OpenSourceTimeoutMaps504GatewayTimeout is Problem 1's third
// protection: h.audioOpenTimeout bounds Service.OpenSource itself, not
// just the stream that follows it -- see getAudio's own doc, "Third, the
// phase BEFORE any of the above even starts". Mounting this route outside
// chimw.Timeout removed the bound chimw.Timeout used to put on this phase
// too, not just on streaming, and a reviewer caught that a hung database
// call inside OpenSource would otherwise now hold this handler's goroutine
// open forever.
//
// Service.OpenSource here blocks until its own openCtx expires and returns
// ctx.Err() -- a bare context.DeadlineExceeded, exactly how a hung
// internal/postgres query surfaces one once its own context is canceled --
// and the response must be exactly the 504 Gateway Timeout chimw.Timeout
// itself would have produced for any other route (domain.CodeOf classifies
// a bare context.DeadlineExceeded as ErrCodeTimeout on its own -- see its
// own doc -- and errors.go's statusForCode maps that to
// http.StatusGatewayTimeout), delivered promptly rather than after a hang.
func TestGetAudio_OpenSourceTimeoutMaps504GatewayTimeout(t *testing.T) {
	tr := sampleTranscription()

	const routeTimeout = 20 * time.Millisecond
	svc := &fakeService{
		openSourceFn: func(ctx context.Context, id string) (io.ReadSeekCloser, *domain.Transcription, error) {
			<-ctx.Done()
			return nil, nil, ctx.Err()
		},
	}

	deps := testDeps(svc, observability.NewRegistry(), false)
	deps.RouteTimeout = routeTimeout

	r := httpapi.NewRouter(deps)

	start := time.Now()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/transcriptions/"+tr.ID+"/audio", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	elapsed := time.Since(start)

	require.Equal(t, http.StatusGatewayTimeout, rec.Code)
	assert.Less(t, elapsed, 2*time.Second,
		"getAudio must return promptly once h.audioOpenTimeout elapses, not hang on a stuck Service.OpenSource")

	var envelope errorEnvelope
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&envelope))
	assert.Equal(t, "timeout", envelope.Error.Code)
}

// TestGetAudio_SlowDownloadSurvivesShortServerWriteTimeout is Problem 1's
// second protection, the mirror image of
// TestSubmitTranscription_SlowUploadSurvivesShortServerWriteTimeout: the
// underlying *http.Server's WriteTimeout starts counting from the instant
// headers are read and covers the whole response, so a large file streamed
// to a merely slow (never stuck) client must survive a server-level
// WriteTimeout far shorter than the download actually takes. getAudio's
// deadlineResettingWriter renews the connection's write deadline to
// AudioWriteIdleTimeout on every chunk actually written -- see that var's
// own doc -- so a transfer that keeps making progress, however slowly,
// never runs into either the original short server WriteTimeout or
// AudioWriteIdleTimeout itself (each 30ms paced chunk below comfortably
// renews well within the ten-minute production default, never overridden
// by this test).
func TestGetAudio_SlowDownloadSurvivesShortServerWriteTimeout(t *testing.T) {
	content := bytes.Repeat([]byte("x"), 4096)
	tr := sampleTranscription()

	svc := &fakeService{
		openSourceFn: func(ctx context.Context, id string) (io.ReadSeekCloser, *domain.Transcription, error) {
			return &slowReadSeekCloser{Reader: bytes.NewReader(content), delay: 30 * time.Millisecond}, tr, nil
		},
	}

	deps := testDeps(svc, observability.NewRegistry(), false)

	srv := httptest.NewUnstartedServer(httpapi.NewRouter(deps))
	srv.Config.WriteTimeout = 150 * time.Millisecond // far shorter than the paced download below
	srv.Start()
	defer srv.Close()

	// len(content)/slowReaderChunkBytes chunks * 30ms is well past the
	// 150ms server WriteTimeout above; each individual chunk's write
	// still lands comfortably inside AudioWriteIdleTimeout, which is all
	// that matters once deadlineResettingWriter is renewing it per chunk.
	resp, err := http.Get(srv.URL + "/api/v1/transcriptions/" + tr.ID + "/audio")
	require.NoError(t, err)
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, content, body)
}

// closeSignalingReadSeekCloser wraps an io.ReadSeeker and sends on closed
// the instant Close is called, so a test can observe getAudio actually
// returning (defer rc.Close() runs on every path out of the handler)
// without depending on anything the client side sees -- which, for
// TestGetAudio_StalledDownloadHitsIdleDeadline below, deliberately never
// reads far enough to observe the response complete or fail on its own.
type closeSignalingReadSeekCloser struct {
	io.ReadSeeker
	closed chan<- struct{}
}

func (c *closeSignalingReadSeekCloser) Close() error {
	select {
	case c.closed <- struct{}{}:
	default:
	}
	return nil
}

// TestGetAudio_StalledDownloadHitsIdleDeadline is the regression test for
// the failure a reviewer of an earlier version of this fix reproduced:
// that version cleared getAudio's write deadline outright rather than
// renewing it, so a client that opened the connection and then never read
// a single byte of the response body left the handler -- and the
// goroutine, file handle and Service.OpenSource reader behind it -- blocked
// indefinitely. AudioWriteIdleTimeout is shrunk here so the test does not
// itself have to wait out the ten-minute production default; the content
// is large enough (64 MiB) to comfortably exceed the combined kernel send/
// receive buffers on a loopback connection nobody is draining, so the
// server is still mid-transfer, genuinely blocked on a Write, when the
// idle deadline elapses.
func TestGetAudio_StalledDownloadHitsIdleDeadline(t *testing.T) {
	old := httpapi.AudioWriteIdleTimeout
	httpapi.AudioWriteIdleTimeout = 50 * time.Millisecond
	defer func() { httpapi.AudioWriteIdleTimeout = old }()

	content := bytes.Repeat([]byte("x"), 64<<20) // 64 MiB
	tr := sampleTranscription()

	closed := make(chan struct{}, 1)
	svc := &fakeService{
		openSourceFn: func(ctx context.Context, id string) (io.ReadSeekCloser, *domain.Transcription, error) {
			return &closeSignalingReadSeekCloser{ReadSeeker: bytes.NewReader(content), closed: closed}, tr, nil
		},
	}

	srv := httptest.NewServer(httpapi.NewRouter(testDeps(svc, observability.NewRegistry(), false)))
	defer srv.Close()

	// A raw connection, deliberately read from only up through the
	// response headers below and never again: net/http's own Client
	// would otherwise let the kernel drain the socket into buffers of
	// its own behind the scenes, defeating the entire point of "the
	// client never reads".
	conn, err := net.Dial("tcp", srv.Listener.Addr().String())
	require.NoError(t, err)
	defer conn.Close()

	_, err = fmt.Fprintf(conn, "GET /api/v1/transcriptions/%s/audio HTTP/1.1\r\nHost: %s\r\n\r\n",
		tr.ID, srv.Listener.Addr().String())
	require.NoError(t, err)

	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	start := time.Now()
	select {
	case <-closed:
		assert.Less(t, time.Since(start), 2*time.Second,
			"getAudio took far longer than AudioWriteIdleTimeout to give up on a stalled client")
	case <-time.After(2 * time.Second):
		t.Fatal("getAudio did not return within a bounded time after the client stopped reading -- the write deadline was never renewed/enforced")
	}
}

// --- not found, panic recovery -----------------------------------------

func TestGetTranscription_NotFound(t *testing.T) {
	svc := &fakeService{
		getFn: func(ctx context.Context, id string) (*domain.Transcription, error) {
			return nil, domain.New(domain.ErrCodeNotFound, "no such transcription")
		},
	}
	r := httpapi.NewRouter(testDeps(svc, observability.NewRegistry(), false))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/transcriptions/missing", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestPanicIsRecoveredAs500(t *testing.T) {
	svc := &fakeService{
		getFn: func(ctx context.Context, id string) (*domain.Transcription, error) {
			panic("boom")
		},
	}
	r := httpapi.NewRouter(testDeps(svc, observability.NewRegistry(), false))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/transcriptions/panic-id", nil)
	rec := httptest.NewRecorder()

	require.NotPanics(t, func() {
		r.ServeHTTP(rec, req)
	})

	assert.Equal(t, http.StatusInternalServerError, rec.Code)

	var envelope errorEnvelope
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&envelope))
	assert.Equal(t, "internal", envelope.Error.Code)
}

// --- DTO shape ----------------------------------------------------------

func TestTranscriptionDTO_HidesInternalFields(t *testing.T) {
	tr := sampleTranscription()
	svc := &fakeService{
		getFn: func(ctx context.Context, id string) (*domain.Transcription, error) {
			return tr, nil
		},
	}
	r := httpapi.NewRouter(testDeps(svc, observability.NewRegistry(), false))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/transcriptions/"+tr.ID, nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	raw := rec.Body.String()
	for _, forbidden := range []string{"sourcePath", "leaseOwner", "leaseExpiresAt", "nextAttemptAt"} {
		assert.NotContains(t, raw, forbidden, "response must never expose %q", forbidden)
	}

	var decoded map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &decoded))
	assert.Equal(t, tr.ID, decoded["id"])
	assert.Equal(t, string(tr.Status), decoded["status"])
}

// --- health, readiness, metrics -----------------------------------------

func TestHealthz(t *testing.T) {
	svc := &fakeService{}
	r := httpapi.NewRouter(testDeps(svc, observability.NewRegistry(), false))

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestReadyz_Healthy(t *testing.T) {
	registry := observability.NewRegistry()
	require.NoError(t, registry.Register(fakeChecker{name: "db"}))

	svc := &fakeService{}
	r := httpapi.NewRouter(testDeps(svc, registry, false))

	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestReadyz_Unhealthy(t *testing.T) {
	registry := observability.NewRegistry()
	require.NoError(t, registry.Register(fakeChecker{name: "db", err: errors.New("connection refused")}))

	svc := &fakeService{}
	r := httpapi.NewRouter(testDeps(svc, registry, false))

	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)

	var body struct {
		OK     bool `json:"ok"`
		Checks map[string]struct {
			OK     bool   `json:"ok"`
			Detail string `json:"detail"`
		} `json:"checks"`
	}
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&body))
	assert.False(t, body.OK)
	assert.False(t, body.Checks["db"].OK)
	assert.Equal(t, "connection refused", body.Checks["db"].Detail)
}

func TestMetricsEndpoint_EnabledAndDisabled(t *testing.T) {
	t.Run("enabled", func(t *testing.T) {
		svc := &fakeService{}
		r := httpapi.NewRouter(testDeps(svc, observability.NewRegistry(), true))

		// Warm up the http_requests counter with one real request, so the
		// scrape below has at least one sample of it to expose.
		warmReq := httptest.NewRequest(http.MethodGet, "/healthz", nil)
		warmRec := httptest.NewRecorder()
		r.ServeHTTP(warmRec, warmReq)

		req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusOK, rec.Code)
		assert.Contains(t, rec.Body.String(), "helios_http_requests_total")
	})

	t.Run("disabled", func(t *testing.T) {
		svc := &fakeService{}
		r := httpapi.NewRouter(testDeps(svc, observability.NewRegistry(), false))

		req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusNotFound, rec.Code)
	})
}

func TestMetrics_NonStandardMethodDoesNotGrowLabelCardinality(t *testing.T) {
	// D1: Go's server accepts any RFC 7230 token as a request method, so an
	// unauthenticated caller running `curl -X RANDOM1`, `curl -X RANDOM2`,
	// ... must never mint a new "method" label series per distinct token --
	// that is an unbounded-memory-growth vector, not a feature. Every
	// nonstandard method must collapse to the single "other" label value.
	svc := &fakeService{}
	r := httpapi.NewRouter(testDeps(svc, observability.NewRegistry(), true))

	nonstandard := []string{"RANDOM1", "RANDOM2", "FROBNICATE"}
	for _, method := range nonstandard {
		req := httptest.NewRequest(method, "/healthz", nil)
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
	}

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	body := rec.Body.String()
	for _, method := range nonstandard {
		assert.NotContains(t, body, `method="`+method+`"`,
			"a nonstandard method must never appear as its own Prometheus label value")
	}
	assert.Contains(t, body, `method="other"`)
}
