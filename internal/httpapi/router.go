// Package httpapi is the REST layer over the transcription Service: a chi
// router, its DTOs, and the error mapping between domain.ErrorCode and HTTP
// status. CONTRACT.md section 6 is the contract this package implements;
// every type and handler below exists to satisfy it exactly. No business
// logic lives here -- every handler does nothing but translate an HTTP
// request into a call on Service and its result back into an HTTP
// response.
package httpapi

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"
	"go.opentelemetry.io/otel/trace"

	"github.com/pablofornell/helios-audio-transcription-service/internal/app"
	"github.com/pablofornell/helios-audio-transcription-service/internal/domain"
	"github.com/pablofornell/helios-audio-transcription-service/internal/observability"
)

// otelServiceName is the otelhttp span/operation name every request is
// wrapped in. It is fixed rather than a RouterDeps field because
// CONTRACT.md's RouterDeps field list has no slot for it; it matches
// config.ObservabilityConfig's own "helios-ats" default so traces line up
// with the rest of the service without another knob to wire.
const otelServiceName = "helios-ats"

// Service is the eight-method surface httpapi drives the business layer
// through. It is declared locally rather than imported as *app.Service, so
// this package -- and its tests, via the hand-written fake in
// fake_test.go -- builds and runs independently of internal/app's own
// build. CONTRACT.md section 5 fixes this exact method set as
// *app.Service's public API; app.Service satisfies Service structurally.
type Service interface {
	// Submit accepts a new transcription request: validates req.Options,
	// streams req.Body to storage, and returns the created row as
	// StatusPending.
	Submit(ctx context.Context, req app.SubmitRequest) (*domain.Transcription, error)
	// Get returns the transcription named id, or ErrCodeNotFound.
	Get(ctx context.Context, id string) (*domain.Transcription, error)
	// List returns one page of transcriptions matching f.
	List(ctx context.Context, f app.ListFilter) (app.Page, error)
	// Delete removes id's row and its stored files.
	Delete(ctx context.Context, id string) error
	// Cancel requests cancellation of id. ErrCodeConflict if already
	// terminal.
	Cancel(ctx context.Context, id string) (*domain.Transcription, error)
	// Retry requeues a failed or canceled id. ErrCodeConflict otherwise.
	Retry(ctx context.Context, id string) (*domain.Transcription, error)
	// Prioritize puts a pending or failed id first in the queue.
	// ErrCodeConflict otherwise.
	Prioritize(ctx context.Context, id string) (*domain.Transcription, error)
	// OpenSource opens the original uploaded audio for id, for streaming
	// back to a caller with http.ServeContent.
	OpenSource(ctx context.Context, id string) (io.ReadSeekCloser, *domain.Transcription, error)
}

// RouterDeps is everything NewRouter needs to build the REST layer: the
// port it wraps, the observability primitives it wires rather than
// reinvents, and the handful of knobs that shape the HTTP surface.
type RouterDeps struct {
	// Service is the single business-logic implementation every handler
	// calls into. No handler may do anything Service doesn't already do.
	Service Service
	// Storage is the same store used for uploaded audio. Its free-space
	// reading refers to the server's storage volume, not the caller's disk.
	Storage interface{ FreeBytes() (uint64, error) }
	// Health aggregates every registered dependency checker for /readyz.
	// Must not be nil.
	Health *observability.Registry
	// Metrics is served at /metrics (see MetricsEnabled) and fed one
	// ObserveHTTP call per request by the metrics middleware.
	Metrics *observability.Metrics
	// Logger is the base *slog.Logger every request's context is seeded
	// with, before a per-request id is folded in. Must not be nil.
	Logger *slog.Logger
	// TracerProvider backs the otelhttp middleware wrapping every request
	// in a span. A noop.NewTracerProvider() is fine when tracing is
	// disabled -- see observability.NewTracerProvider.
	TracerProvider trace.TracerProvider
	// MaxUploadBytes caps a single POST /transcriptions upload, enforced
	// via http.MaxBytesReader before the multipart body is ever parsed.
	MaxUploadBytes int64
	// RouteTimeout bounds how long any /api/v1 handler may run, applied
	// per-route via chimw.Timeout -- see NewRouter's doc for why it sits
	// innermost in the middleware stack.
	RouteTimeout time.Duration
	// MetricsEnabled toggles whether /metrics is mounted at all.
	MetricsEnabled bool
	// CORSAllowedOrigins feeds newCORSMiddleware -- empty (the default)
	// disables CORS handling entirely, byte-for-byte the same behaviour
	// this router had before HELIOS_CORS_ALLOWED_ORIGINS existed. Read
	// this only after config.Config.Validate has normalised it (see that
	// field's own doc) -- cmd/helios-transcription-api/wire passes
	// cfg.CORS.AllowedOrigins straight through, after Load already ran
	// Validate.
	CORSAllowedOrigins []string
}

// NewRouter builds the chi.Router serving CONTRACT.md section 6's route
// table: every /api/v1/* route, plus /healthz, /readyz and /metrics at the
// root (unprefixed, matching deploy/Dockerfile's HEALTHCHECK).
//
// Middleware stack, applied in this order (chimw.RequestID outermost, down
// to chimw.Timeout innermost, right before routing/handlers):
//
//  1. chimw.RequestID   -- assigns (or forwards) this request's id first,
//     so every middleware after it, and every handler, can read one.
//  2. chimw.RealIP      -- trusts X-Forwarded-For/X-Real-IP to fix up
//     r.RemoteAddr before anything logs or traces it.
//  3. CORS              -- newCORSMiddleware(deps.CORSAllowedOrigins),
//     disabled (a no-op passthrough) unless HELIOS_CORS_ALLOWED_ORIGINS is
//     set. Sits this early so an allowed preflight is answered, and every
//     other response gets its Access-Control-* headers, before anything
//     below even runs -- see that middleware's own doc for why a preflight
//     never needs a route of its own.
//  4. otelhttp          -- opens this request's span early, so every
//     downstream log line and metric can be correlated to a trace id, and
//     so the span's duration covers the whole stack below it, not just
//     the handler.
//  5. requestLogger     -- seeds the context with the configured logger
//     and this request's id (observability.WithRequestID), then defers
//     one access-log line for when the response is final.
//  6. recoverer         -- catches a panic from anything below it
//     (metrics middleware, the router, or a handler) and turns it into a
//     clean 500 error envelope. It sits *inside* requestLogger
//     deliberately: by recovering before requestLogger's own deferred log
//     runs, that log line -- which already carries this request's id via
//     step 5 -- is the one line an operator needs to find a panic,
//     without a second, separate "logged from the panic handler" line to
//     correlate by hand.
//  7. metrics           -- records one ObserveHTTP call per request. It
//     sits *inside* recoverer so a panic is still recorded (with a 500,
//     via a recover/re-panic of its own -- see metricsMiddleware) rather
//     than silently missing from /metrics, while recoverer still gets to
//     handle the panic itself.
//  8. chimw.Timeout     -- bounds how long a handler may run. It is
//     innermost (closest to the handler) so requestLogger, recoverer and
//     metrics all still see its 504 like any other response. It is
//     applied per-route below, under /api/v1 only, so /healthz, /readyz
//     and /metrics are never subject to it -- and, within /api/v1, so is
//     GET .../audio: see mountAPI's own doc for why that one route opts
//     out.
func NewRouter(deps RouterDeps) http.Handler {
	r := chi.NewRouter()

	r.Use(
		chimw.RequestID,
		chimw.RealIP,
		newCORSMiddleware(deps.CORSAllowedOrigins),
		observability.HTTPMiddleware(deps.TracerProvider, otelServiceName),
		newRequestLogger(deps.Logger),
		recoverer,
		metricsMiddleware(deps.Metrics),
	)

	h := &handlers{
		service:            deps.Service,
		storage:            deps.Storage,
		health:             deps.Health,
		maxUploadBytes:     deps.MaxUploadBytes,
		uploadWriteTimeout: deps.RouteTimeout,
		audioOpenTimeout:   deps.RouteTimeout,
	}

	mountAPI(r, h, deps.RouteTimeout)

	r.Get("/healthz", h.healthz)
	r.Get("/readyz", h.readyz)
	if deps.MetricsEnabled {
		r.Handle("/metrics", deps.Metrics.Handler())
	}

	return r
}

// mountAPI registers every /api/v1/* route from CONTRACT.md section 6.
// Every route except GET .../audio is bounded by chimw.Timeout(routeTimeout),
// applied via api.Group so its own, isolated middleware stack does not leak
// onto anything mounted outside it -- unlike BLUEPRINT, this service has no
// *other* long-lived streaming endpoint that needs to opt out of it.
//
// GET .../audio is that one exception: it streams a potentially large file
// (the original upload) back to a potentially slow client via
// http.ServeContent, and chimw.Timeout would cancel the request's context
// -- and, on top of that, unconditionally write its own 504 Gateway Timeout
// through a wrapped ResponseWriter -- partway through an otherwise
// perfectly healthy, merely slow, download the instant routeTimeout
// elapses. It is mounted directly on api instead, so it still gets every
// middleware in NewRouter's top-level stack (CORS, tracing, logging,
// recovery, metrics) but never chimw.Timeout. getAudio does not simply go
// unbounded in chimw.Timeout's place, though: its own doc covers the two
// replacement protections this loses by opting out (one for the streaming
// phase's total duration, one for its staleness) plus a third,
// h.audioOpenTimeout, that keeps the phase BEFORE streaming even starts
// (Service.OpenSource, which reaches the database) bounded the same
// routeTimeout would have bounded it by, since removing chimw.Timeout
// removed that bound too, not just the one on the streaming that follows.
// GET .../transcript is NOT given the same exemption: it renders straight
// from the already-fetched domain.Result into memory (transcript.Render)
// rather than streaming a stored file of arbitrary size, so it stays
// bounded like every other route below.
func mountAPI(r chi.Router, h *handlers, routeTimeout time.Duration) {
	r.Route("/api/v1", func(api chi.Router) {
		api.Group(func(bounded chi.Router) {
			bounded.Use(chimw.Timeout(routeTimeout))

			bounded.Post("/transcriptions", h.submitTranscription)
			bounded.Get("/transcriptions", h.listTranscriptions)
			bounded.Get("/storage", h.getStorage)
			bounded.Get("/transcriptions/{id}", h.getTranscription)
			bounded.Delete("/transcriptions/{id}", h.deleteTranscription)
			bounded.Post("/transcriptions/{id}/cancel", h.cancelTranscription)
			bounded.Post("/transcriptions/{id}/retry", h.retryTranscription)
			bounded.Post("/transcriptions/{id}/prioritize", h.prioritizeTranscription)
			bounded.Get("/transcriptions/{id}/transcript", h.getTranscript)
		})

		// Not part of the Group above -- see this func's own doc.
		api.Get("/transcriptions/{id}/audio", h.getAudio)
	})
}
