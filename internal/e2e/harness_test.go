package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/pablofornell/helios-audio-transcription-service/cmd/helios-transcription-api/wire"
	"github.com/pablofornell/helios-audio-transcription-service/internal/app"
	"github.com/pablofornell/helios-audio-transcription-service/internal/config"
	"github.com/pablofornell/helios-audio-transcription-service/internal/domain"
	"github.com/pablofornell/helios-audio-transcription-service/internal/postgres"
)

// testPool is a pgxpool.Pool this package keeps open against the shared
// testcontainers Postgres purely for setup/assertion SQL this suite has no
// HTTP surface for (truncating the table between tests, backdating a row's
// completed_at for the retention scenario -- see truncateAll and
// retention_test.go). It is deliberately separate from the pool each
// wire.Build call opens for a Stack's own Service/JobPool/HealthRegistry:
// those must go through the exact same construction path main() uses (see
// this package's own doc), not share a pool with this package's own
// out-of-band bookkeeping.
var testPool *pgxpool.Pool

// pgxPoolFor opens a pgxpool.Pool against dsn. Used once, from TestMain, to
// build testPool.
func pgxPoolFor(ctx context.Context, dsn string) (*pgxpool.Pool, error) {
	return pgxpool.New(ctx, dsn)
}

// truncateAll resets the transcriptions table so this package's tests --
// which run sequentially, never in parallel (none call t.Parallel) -- never
// see another test's rows. Every wire.Build call in this package points at
// the same underlying database, so truncating via testPool is visible to
// every Stack a test subsequently builds.
func truncateAll(t *testing.T) {
	t.Helper()
	_, err := testPool.Exec(context.Background(), `TRUNCATE transcriptions`)
	require.NoError(t, err)
}

// repoOf returns an app.Repository/app.Queue view onto st's own database
// pool -- the same *postgres.Store wire.Build constructs internally for
// Service/JobPool, but wire.Stack does not export it (Service and JobPool
// are the only sanctioned ways to touch a row in production). A handful of
// scenarios in this package (simulating a crashed worker's lease, driving a
// retention fixture straight to succeeded without waiting on the real
// worker pool) need to Claim/Finish a row directly, the same way a real
// worker would, without going through HTTP -- see CONTRACT.md's own
// allowance for this suite to "reach into internals... to assert stored
// state". Constructing a second *postgres.Store over st.DB is cheap and
// exactly mirrors what wire.Build itself does with the same pool.
func repoOf(st *wire.Stack) *postgres.Store {
	return postgres.New(st.DB)
}

// newTestConfig builds a config.Config pointed at the shared test container
// (testDSN) and a fresh temp directory, tuned so every timing knob a
// scenario in this package waits on -- claim, lease, heartbeat, backoff --
// fires in tens of milliseconds rather than the production defaults'
// seconds to minutes. It runs the same cfg.Validate a real config.Load
// would, so a mistake here fails immediately with the same message an
// operator would see.
func newTestConfig(t *testing.T) *config.Config {
	t.Helper()

	cfg := &config.Config{
		HTTP: config.HTTPConfig{
			Addr:            ":0",
			ReadTimeout:     5 * time.Second,
			WriteTimeout:    5 * time.Second,
			IdleTimeout:     30 * time.Second,
			ShutdownTimeout: 5 * time.Second,
			MaxUploadBytes:  64 << 20, // 64 MiB: far above any fixture this suite uploads, unless a test overrides it.
		},
		Database: config.DatabaseConfig{
			DSN:            testDSN,
			ConnectTimeout: 10 * time.Second,
			MaxConns:       10,
			// TestMain already migrated the shared container once; each
			// wire.Build call below would otherwise re-run (idempotent, but
			// pointless) goose migrations against the same schema.
			AutoMigrate: false,
		},
		Storage: config.StorageConfig{
			RootDir:      t.TempDir(),
			MinFreeBytes: 0,
		},
		Transcriber: config.TranscriberConfig{
			// wire.Build's default transcriber selection reads this, but
			// every test in this package overrides it via
			// wire.WithTranscriber -- see newRawStack -- so it is set here
			// only so a hand-built Config still describes a coherent,
			// Validate-able configuration on its own.
			Kind:          "fake",
			Binary:        "whisper-cli",
			ModelDir:      t.TempDir(),
			DefaultModel:  "large-v3-turbo-q5_0",
			AllowedModels: []string{"large-v3-turbo-q5_0"},
			Threads:       1,
			Timeout:       2 * time.Second,
		},
		Audio: config.AudioConfig{
			FFmpegBinary:  "ffmpeg",
			FFprobeBinary: "ffprobe",
			MaxDuration:   time.Hour,
			Timeout:       5 * time.Second,
		},
		Worker: config.WorkerConfig{
			Concurrency:       2,
			PollInterval:      20 * time.Millisecond,
			LeaseDuration:     300 * time.Millisecond,
			HeartbeatInterval: 30 * time.Millisecond,
			JobTimeout:        10 * time.Second,
			MaxAttempts:       3,
			BackoffBase:       20 * time.Millisecond,
			BackoffMax:        100 * time.Millisecond,
		},
		Retention: config.RetentionConfig{
			// Off by default; retention_test.go turns it on with its own
			// knobs.
			Enabled:  false,
			MaxAge:   time.Hour,
			Interval: time.Hour,
		},
		Observability: config.ObservabilityConfig{
			LogLevel:       "warn",
			LogFormat:      "text",
			MetricsEnabled: true,
			TracingEnabled: false,
			ServiceName:    "helios-e2e",
		},
	}
	require.NoError(t, cfg.Validate())
	return cfg
}

// ---------------------------------------------------------------------------
// stack construction: every test in this package gets its Stack from
// wire.Build -- the exact same construction path cmd/helios-transcription-api's
// main() calls -- never a parallel imitation of it. See this package's own
// doc.
// ---------------------------------------------------------------------------

// newRawStack truncates the shared table (see truncateAll) and calls
// wire.Build against cfg, substituting transcriber via wire.WithTranscriber
// when non-nil (nil means "whatever cfg.Transcriber.Kind selects", which
// newTestConfig sets to "fake" -- see wire.buildTranscriber). It registers
// st.Shutdown as a t.Cleanup immediately, so a test that never calls
// st.Start() at all still gets its pool/tracer/DB torn down correctly.
//
// It deliberately does not start anything (see wire.Stack.Start's own
// doc): a caller that wants the ordinary "everything running immediately"
// shape calls st.Start() itself (or uses newTestServer/newTestServerWithTranscriber,
// which do); a caller that needs a window to manipulate rows before any
// worker can touch them (crash_recovery_test.go, listing_test.go,
// retention_test.go) gets that window for free.
func newRawStack(t *testing.T, cfg *config.Config, transcriber app.Transcriber) *wire.Stack {
	t.Helper()
	require.NoError(t, cfg.Validate())
	truncateAll(t)

	var opts []wire.Option
	if transcriber != nil {
		opts = append(opts, wire.WithTranscriber(transcriber))
	}

	st, err := wire.Build(context.Background(), cfg, opts...)
	require.NoError(t, err)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = st.Shutdown(ctx)
	})
	return st
}

// serveStack wraps st.Router in an httptest.Server, torn down via
// t.Cleanup (before st's own Shutdown cleanup, registered earlier by
// newRawStack -- t.Cleanup's LIFO order means HTTP stops accepting first,
// exactly the order wire.Stack.Shutdown's own doc describes for main()).
func serveStack(t *testing.T, st *wire.Stack) string {
	t.Helper()
	srv := httptest.NewServer(st.Router)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Config.Shutdown(ctx)
		srv.Close()
	})
	return srv.URL
}

// startSweeperOnly runs st.Retention.Run in its own background goroutine,
// independent of st.Start (which would also start st.JobPool -- see its
// own doc), stopped via t.Cleanup. It requires st was built with retention
// enabled (st.Retention non-nil). See retention_test.go for why that test
// needs retention running while the worker pool never claims anything.
func startSweeperOnly(t *testing.T, st *wire.Stack) {
	t.Helper()
	require.NotNil(t, st.Retention, "startSweeperOnly requires a stack built with Retention.Enabled")

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = st.Retention.Run(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
}

// ---------------------------------------------------------------------------
// testServer: one Stack, built via wire.Build and started via st.Start,
// served over a real loopback listener via httptest. Both are torn down
// via t.Cleanup.
// ---------------------------------------------------------------------------

type testServer struct {
	stack *wire.Stack
	http  *httptest.Server
	base  string
}

// newTestServer builds a stack from newTestConfig with every opt applied,
// using the default transcriber (fake, per newTestConfig's
// Transcriber.Kind), starts it, and serves its router over httptest.
func newTestServer(t *testing.T, opts ...func(*config.Config)) *testServer {
	t.Helper()
	return newTestServerWithTranscriber(t, nil, opts...)
}

// newTestServerWithTranscriber is newTestServer, but with an explicit
// app.Transcriber substituted via wire.WithTranscriber (nil for the
// default) -- for a scenario that needs scripted behaviour
// config.TranscriberConfig has no field to express (e.g.
// TestCancel_RunningWorkerStopsAndSticks's delayed transcriber).
func newTestServerWithTranscriber(t *testing.T, transcriber app.Transcriber, opts ...func(*config.Config)) *testServer {
	t.Helper()

	cfg := newTestConfig(t)
	for _, opt := range opts {
		opt(cfg)
	}

	st := newRawStack(t, cfg, transcriber)
	st.Start()

	srv := httptest.NewServer(st.Router)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Config.Shutdown(ctx)
		srv.Close()
	})

	return &testServer{stack: st, http: srv, base: srv.URL}
}

// withMaxUploadBytes overrides HTTP.MaxUploadBytes -- see
// limits_test.go's payload-too-large scenario.
func withMaxUploadBytes(n int64) func(*config.Config) {
	return func(c *config.Config) { c.HTTP.MaxUploadBytes = n }
}

// ---------------------------------------------------------------------------
// wire-shape DTOs this package decodes JSON responses into.
//
// domain.Transcription (and the domain.Options/domain.Audio/domain.Result/
// domain.Segment/domain.Word/domain.Error types it embeds) are declared,
// per CONTRACT.md section 3, with the exact same JSON tags httpapi's own
// (unexported) transcriptionDTO mirrors them with field for field -- so
// decoding an API response straight into a domain.Transcription is not a
// shortcut that happens to work, it is what the contract guarantees. The
// three fields domain.Transcription carries that are not API surface
// (SourcePath, NextAttemptAt, LeaseOwner, LeaseToken, LeaseExpiresAt) are all
// `json:"-"` on the domain type too, so they simply stay zero on decode --
// exactly as absent from the wire as they are from httpapi's own DTO.
// ---------------------------------------------------------------------------

// listResponseJSON mirrors GET /api/v1/transcriptions' response body.
type listResponseJSON struct {
	Items      []domain.Transcription `json:"items"`
	NextCursor string                 `json:"nextCursor,omitempty"`
}

// errorEnvelopeJSON mirrors the `{"error":{"code","message","details"}}`
// envelope every non-2xx JSON response in this API uses. domain.Error's own
// json tags already match it field for field (Err is `json:"-"`, so it
// stays nil on decode).
type errorEnvelopeJSON struct {
	Error domain.Error `json:"error"`
}

// checkResultJSON mirrors observability.CheckResult's wire shape (as
// httpapi's checkResultDTO renders it).
type checkResultJSON struct {
	OK     bool   `json:"ok"`
	Detail string `json:"detail,omitempty"`
}

// healthReportJSON mirrors /readyz's response body.
type healthReportJSON struct {
	OK     bool                       `json:"ok"`
	Checks map[string]checkResultJSON `json:"checks"`
}

// ---------------------------------------------------------------------------
// HTTP helpers
// ---------------------------------------------------------------------------

// doJSON issues method against url with an optional JSON-encoded body and
// returns the raw response; the caller decodes it and is responsible for
// closing resp.Body.
func doJSON(t *testing.T, method, url string, body any) *http.Response {
	t.Helper()

	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		require.NoError(t, err)
		reader = bytes.NewReader(b)
	}

	req, err := http.NewRequest(method, url, reader)
	require.NoError(t, err)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	return resp
}

// decodeJSON decodes resp's body into T and closes it.
func decodeJSON[T any](t *testing.T, resp *http.Response) T {
	t.Helper()
	defer resp.Body.Close()

	var v T
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&v))
	return v
}

// decodeError decodes resp's body as the error envelope and closes it.
func decodeError(t *testing.T, resp *http.Response) domain.Error {
	t.Helper()
	env := decodeJSON[errorEnvelopeJSON](t, resp)
	return env.Error
}

// buildUpload encodes a multipart/form-data body with an optional `options`
// JSON part (nil means "omit it entirely" -- see the options-defaults
// scenario) followed by the required `file` part, and returns the body
// bytes alongside the Content-Type header (carrying the boundary) the
// caller must set.
func buildUpload(t *testing.T, filename, mediaType string, content []byte, opts *domain.Options) (contentType string, body []byte) {
	t.Helper()

	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)

	if opts != nil {
		optsJSON, err := json.Marshal(opts)
		require.NoError(t, err)
		part, err := w.CreateFormField("options")
		require.NoError(t, err)
		_, err = part.Write(optsJSON)
		require.NoError(t, err)
	}

	fw, err := w.CreateFormFile("file", filename)
	require.NoError(t, err)
	_, err = fw.Write(content)
	require.NoError(t, err)

	require.NoError(t, w.Close())
	return w.FormDataContentType(), buf.Bytes()
}

// submitFile POSTs a multipart upload built from a fixture file on disk
// (path, e.g. fixtureGoodMP3) to base, requiring a 202 with a pending
// transcription body and a Location header.
func submitFile(t *testing.T, base, path, mediaType string, opts *domain.Options) domain.Transcription {
	t.Helper()

	content, err := os.ReadFile(path)
	require.NoError(t, err)

	contentType, body := buildUpload(t, filenameOf(path), mediaType, content, opts)

	req, err := http.NewRequest(http.MethodPost, base+"/api/v1/transcriptions", bytes.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", contentType)

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	require.Equalf(t, http.StatusAccepted, resp.StatusCode, "submit: unexpected status")
	assertLocationHeader(t, resp)

	return decodeJSON[domain.Transcription](t, resp)
}

// assertLocationHeader requires resp to carry a Location header pointing at
// a /api/v1/transcriptions/{id} path.
func assertLocationHeader(t *testing.T, resp *http.Response) {
	t.Helper()
	loc := resp.Header.Get("Location")
	require.NotEmpty(t, loc, "expected a Location header on a 202 submit response")
	require.Contains(t, loc, "/api/v1/transcriptions/")
}

// getTranscription calls GET /api/v1/transcriptions/{id} and requires a
// 200.
func getTranscription(t *testing.T, base, id string) domain.Transcription {
	t.Helper()
	resp := doJSON(t, http.MethodGet, base+"/api/v1/transcriptions/"+id, nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	return decodeJSON[domain.Transcription](t, resp)
}

// terminalStatuses is every domain.Status.Terminal() reports true for.
var terminalStatuses = map[domain.Status]bool{
	domain.StatusSucceeded: true,
	domain.StatusFailed:    true,
	domain.StatusCanceled:  true,
}

// waitForStatus polls GET /api/v1/transcriptions/{id} until it reports
// want, or fails the test -- immediately, if a *different* terminal status
// is reached first (there is no point waiting out the rest of deadline for
// a transcription that can never change again), or once deadline elapses.
func waitForStatus(t *testing.T, base, id string, want domain.Status, deadline time.Duration) domain.Transcription {
	t.Helper()

	end := time.Now().Add(deadline)
	var last domain.Transcription
	for time.Now().Before(end) {
		last = getTranscription(t, base, id)
		if last.Status == want {
			return last
		}
		if terminalStatuses[last.Status] && last.Status != want {
			t.Fatalf("transcription %s reached terminal status %q while waiting for %q (error=%+v)", id, last.Status, want, last.Error)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("transcription %s did not reach status %q within %s (last status %q, error=%+v)", id, want, deadline, last.Status, last.Error)
	return domain.Transcription{}
}

// cancelTranscription calls POST /api/v1/transcriptions/{id}/cancel.
func cancelTranscription(t *testing.T, base, id string) *http.Response {
	t.Helper()
	return doJSON(t, http.MethodPost, base+"/api/v1/transcriptions/"+id+"/cancel", nil)
}

// retryTranscription calls POST /api/v1/transcriptions/{id}/retry.
func retryTranscription(t *testing.T, base, id string) *http.Response {
	t.Helper()
	return doJSON(t, http.MethodPost, base+"/api/v1/transcriptions/"+id+"/retry", nil)
}

// deleteTranscription calls DELETE /api/v1/transcriptions/{id} and requires
// a 204.
func deleteTranscription(t *testing.T, base, id string) {
	t.Helper()
	resp := doJSON(t, http.MethodDelete, base+"/api/v1/transcriptions/"+id, nil)
	defer resp.Body.Close()
	require.Equal(t, http.StatusNoContent, resp.StatusCode)
}

// getTranscript calls GET /api/v1/transcriptions/{id}/transcript?format=...
// and returns the raw response for the caller to inspect Content-Type and
// body.
func getTranscript(t *testing.T, base, id, format string) *http.Response {
	t.Helper()
	url := base + "/api/v1/transcriptions/" + id + "/transcript"
	if format != "" {
		url += "?format=" + format
	}
	return doJSON(t, http.MethodGet, url, nil)
}

// listTranscriptions calls GET /api/v1/transcriptions with the given query
// string (e.g. "status=pending&cursor=...&limit=2") and requires a 200.
func listTranscriptions(t *testing.T, base, query string) listResponseJSON {
	t.Helper()
	url := base + "/api/v1/transcriptions"
	if query != "" {
		url += "?" + query
	}
	resp := doJSON(t, http.MethodGet, url, nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	return decodeJSON[listResponseJSON](t, resp)
}

// filenameOf returns path's base name, for use as a submitted upload's
// filename.
func filenameOf(path string) string {
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '/' {
			return path[i+1:]
		}
	}
	return path
}
