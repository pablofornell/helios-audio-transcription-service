package httpapi_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/trace/noop"

	"log/slog"

	"github.com/pablofornell/helios-audio-transcription-service/internal/config"
	"github.com/pablofornell/helios-audio-transcription-service/internal/domain"
	"github.com/pablofornell/helios-audio-transcription-service/internal/httpapi"
	"github.com/pablofornell/helios-audio-transcription-service/internal/observability"
)

// corsDeps builds an httpapi.RouterDeps identical to testDeps (fake_test.go),
// except it also sets CORSAllowedOrigins — the one field testDeps itself
// always leaves at its zero value (disabled), so these tests build their
// own RouterDeps rather than adding a parameter every other testDeps
// caller would have to start passing too.
func corsDeps(svc httpapi.Service, allowedOrigins []string) httpapi.RouterDeps {
	return httpapi.RouterDeps{
		Service:            svc,
		Health:             observability.NewRegistry(),
		Metrics:            observability.NewMetrics(prometheus.NewRegistry()),
		Logger:             slog.New(slog.NewTextHandler(io.Discard, nil)),
		TracerProvider:     noop.NewTracerProvider(),
		MaxUploadBytes:     10 << 20,
		RouteTimeout:       5 * time.Second,
		CORSAllowedOrigins: allowedOrigins,
	}
}

// TestCORS_DisabledByDefault_NeverTouchesHeaders proves HELIOS_CORS_ALLOWED_ORIGINS
// unset (a nil CORSAllowedOrigins — the zero value) behaves exactly as if
// the CORS middleware were never in the stack at all: no Access-Control-*
// or Vary header, even though the request carries an Origin header a
// browser would send for a genuine cross-origin call.
func TestCORS_DisabledByDefault_NeverTouchesHeaders(t *testing.T) {
	r := httpapi.NewRouter(corsDeps(&fakeService{}, nil))

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	req.Header.Set("Origin", "https://ats.example.com")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	resp := w.Result()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Empty(t, resp.Header.Get("Access-Control-Allow-Origin"))
	assert.Empty(t, resp.Header.Get("Vary"))
}

// TestCORS_AllowedOrigin_ActualRequest_AddsHeaders proves an ordinary
// (non-preflight) request from an allowed origin gets
// Access-Control-Allow-Origin echoing that exact origin, plus Vary:
// Origin, and still reaches the handler.
func TestCORS_AllowedOrigin_ActualRequest_AddsHeaders(t *testing.T) {
	r := httpapi.NewRouter(corsDeps(&fakeService{}, []string{"https://ats.example.com"}))

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	req.Header.Set("Origin", "https://ats.example.com")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	resp := w.Result()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "https://ats.example.com", resp.Header.Get("Access-Control-Allow-Origin"))
	assert.Contains(t, resp.Header.Values("Vary"), "Origin")
}

// TestCORS_AllowedOrigin_WorksForReadyz proves the same holds for /readyz —
// the other health endpoint this feature's brief calls out by name.
func TestCORS_AllowedOrigin_WorksForReadyz(t *testing.T) {
	r := httpapi.NewRouter(corsDeps(&fakeService{}, []string{"https://ats.example.com"}))

	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	req.Header.Set("Origin", "https://ats.example.com")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	resp := w.Result()
	assert.Equal(t, "https://ats.example.com", resp.Header.Get("Access-Control-Allow-Origin"))
}

// TestCORS_AllowedOrigin_WorksForAudioStreaming proves GET
// /api/v1/transcriptions/{id}/audio — the one route that streams a real
// response body via http.ServeContent, not a plain JSON handler — gets the
// exact same Access-Control-Allow-Origin treatment as any other route,
// since the CORS middleware runs before routing ever distinguishes it (see
// NewRouter's doc). This is the route DetailPanel.tsx's <audio> element
// points at; that element carries no `crossorigin` attribute, so its own
// playback never actually goes through CORS at all (see
// corsAllowedHeaders' own doc) — this test instead covers a plain fetch of
// the same route from a different origin, which still needs
// Access-Control-Allow-Origin to be readable by that origin's own script.
func TestCORS_AllowedOrigin_WorksForAudioStreaming(t *testing.T) {
	svc := &fakeService{
		openSourceFn: func(_ context.Context, id string) (io.ReadSeekCloser, *domain.Transcription, error) {
			return &fakeReadSeekCloser{ReadSeeker: strings.NewReader("audio bytes")},
				&domain.Transcription{ID: id, Audio: domain.Audio{Filename: "clip.mp3", MediaType: "audio/mpeg"}},
				nil
		},
	}
	r := httpapi.NewRouter(corsDeps(svc, []string{"https://ats.example.com"}))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/transcriptions/id-1/audio", nil)
	req.Header.Set("Origin", "https://ats.example.com")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	resp := w.Result()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "https://ats.example.com", resp.Header.Get("Access-Control-Allow-Origin"))
}

// TestCORS_DisallowedOrigin_NoAllowHeader proves an Origin outside the
// allowlist gets no Access-Control-Allow-Origin — leaving the requesting
// browser, not this server, to refuse the calling page access — while the
// request itself still reaches the handler and completes normally (a
// non-browser caller, or a browser request CORS was never meant to police,
// must not be broken by a policy it is not subject to).
func TestCORS_DisallowedOrigin_NoAllowHeader(t *testing.T) {
	r := httpapi.NewRouter(corsDeps(&fakeService{}, []string{"https://ats.example.com"}))

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	req.Header.Set("Origin", "https://evil.example")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	resp := w.Result()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Empty(t, resp.Header.Get("Access-Control-Allow-Origin"))
	// Vary: Origin is still owed — a cache must still know this response
	// would differ for a different Origin.
	assert.Contains(t, resp.Header.Values("Vary"), "Origin")
}

// TestCORS_Preflight_AllowedOrigin_AnswersDirectly proves a preflight
// (OPTIONS + Access-Control-Request-Method) from an allowed origin is
// answered right there in the middleware — a 204 with every header a
// browser needs to then send the real POST/DELETE request — without ever
// reaching a handler (there is no registered OPTIONS route for this path
// at all; see mountAPI).
func TestCORS_Preflight_AllowedOrigin_AnswersDirectly(t *testing.T) {
	r := httpapi.NewRouter(corsDeps(&fakeService{}, []string{"https://ats.example.com"}))

	req := httptest.NewRequest(http.MethodOptions, "/api/v1/transcriptions", nil)
	req.Header.Set("Origin", "https://ats.example.com")
	req.Header.Set("Access-Control-Request-Method", "POST")
	req.Header.Set("Access-Control-Request-Headers", "content-type")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	resp := w.Result()
	require.Equal(t, http.StatusNoContent, resp.StatusCode)
	assert.Equal(t, "https://ats.example.com", resp.Header.Get("Access-Control-Allow-Origin"))
	assert.Contains(t, resp.Header.Get("Access-Control-Allow-Methods"), "POST")
	assert.Contains(t, resp.Header.Get("Access-Control-Allow-Headers"), "Content-Type")
	assert.NotEmpty(t, resp.Header.Get("Access-Control-Max-Age"))
}

// TestCORS_Preflight_DisallowedOrigin_NoAllowHeaders proves a preflight
// from an origin outside the allowlist gets no Access-Control-Allow-*
// header — whatever status chi's own unmatched-OPTIONS handling produces
// is irrelevant, since the browser blocks based on the missing header
// regardless of status code.
func TestCORS_Preflight_DisallowedOrigin_NoAllowHeaders(t *testing.T) {
	r := httpapi.NewRouter(corsDeps(&fakeService{}, []string{"https://ats.example.com"}))

	req := httptest.NewRequest(http.MethodOptions, "/api/v1/transcriptions", nil)
	req.Header.Set("Origin", "https://evil.example")
	req.Header.Set("Access-Control-Request-Method", "POST")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	resp := w.Result()
	assert.Empty(t, resp.Header.Get("Access-Control-Allow-Origin"))
	assert.Empty(t, resp.Header.Get("Access-Control-Allow-Methods"))
}

// TestCORS_NoOriginHeader_PassesThroughUntouched proves a same-origin (or
// non-browser) request — one with no Origin header at all — is left
// completely alone even when CORS is enabled: no Access-Control-* header,
// no Vary, since there was never a cross-origin decision to make.
func TestCORS_NoOriginHeader_PassesThroughUntouched(t *testing.T) {
	r := httpapi.NewRouter(corsDeps(&fakeService{}, []string{"https://ats.example.com"}))

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	resp := w.Result()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Empty(t, resp.Header.Get("Access-Control-Allow-Origin"))
	assert.Empty(t, resp.Header.Get("Vary"))
}

// TestCORS_ConfiguredOriginWithDefaultPortAndMixedCase_MatchesRealBrowserOrigin
// proves the full pipeline a real deployment depends on: an operator is
// free to type "https://HOST:443" into HELIOS_CORS_ALLOWED_ORIGINS (a
// perfectly valid, equivalent way to spell an origin) and it still matches
// the "https://host" a real browser's Origin header actually sends —
// because config.Config.Validate normalises AllowedOrigins in place (see
// its own doc) before anything ever reaches newCORSMiddleware's exact-match
// lookup. Built through a real config.Config + Validate() call, not a
// pre-normalised literal handed to corsDeps, so this test would have
// caught the exact mismatch this middleware would ship with otherwise.
func TestCORS_ConfiguredOriginWithDefaultPortAndMixedCase_MatchesRealBrowserOrigin(t *testing.T) {
	cfg := minimalValidConfig()
	cfg.CORS.AllowedOrigins = []string{"https://HEL-ATS.home.lan:443"}
	require.NoError(t, cfg.Validate())

	deps := corsDeps(&fakeService{}, cfg.CORS.AllowedOrigins)
	r := httpapi.NewRouter(deps)

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	req.Header.Set("Origin", "https://hel-ats.home.lan")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	resp := w.Result()
	assert.Equal(t, "https://hel-ats.home.lan", resp.Header.Get("Access-Control-Allow-Origin"))
}

// minimalValidConfig returns a config.Config that satisfies every
// config.Validate rule besides CORS — the same minimal shape
// internal/config/config_test.go's own validConfig builds, duplicated here
// (rather than exported from that package just for this one test) since
// only Validate's CORS-normalisation side effect, not the rest of that
// package's surface, is what this test needs.
func minimalValidConfig() config.Config {
	return config.Config{
		HTTP: config.HTTPConfig{
			ReadTimeout:     5 * time.Minute,
			WriteTimeout:    60 * time.Second,
			IdleTimeout:     120 * time.Second,
			ShutdownTimeout: 20 * time.Second,
			MaxUploadBytes:  2 << 30,
		},
		Database: config.DatabaseConfig{
			DSN:            "postgres://helios:helios@localhost:5432/helios_ats?sslmode=disable",
			ConnectTimeout: 10 * time.Second,
			MaxConns:       10,
		},
		Storage: config.StorageConfig{
			RootDir:      "/data",
			MinFreeBytes: 1 << 30,
		},
		Transcriber: config.TranscriberConfig{
			Kind:          "fake",
			DefaultModel:  "base",
			AllowedModels: []string{"base"},
			Timeout:       30 * time.Minute,
		},
		Audio: config.AudioConfig{
			MaxDuration: 4 * time.Hour,
			Timeout:     10 * time.Minute,
		},
		Worker: config.WorkerConfig{
			Concurrency:       2,
			PollInterval:      2 * time.Second,
			LeaseDuration:     time.Minute,
			HeartbeatInterval: 15 * time.Second,
			JobTimeout:        45 * time.Minute,
			MaxAttempts:       3,
			BackoffBase:       10 * time.Second,
			BackoffMax:        10 * time.Minute,
		},
		Retention: config.RetentionConfig{
			MaxAge:   168 * time.Hour,
			Interval: time.Hour,
		},
		Observability: config.ObservabilityConfig{
			LogLevel:    "info",
			LogFormat:   "json",
			ServiceName: "helios-ats",
		},
	}
}
