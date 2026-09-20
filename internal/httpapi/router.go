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

// Service is the seven-method surface httpapi drives the business layer
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
//  3. otelhttp          -- opens this request's span early, so every
//     downstream log line and metric can be correlated to a trace id, and
//     so the span's duration covers the whole stack below it, not just
//     the handler.
//  4. requestLogger     -- seeds the context with the configured logger
//     and this request's id (observability.WithRequestID), then defers
//     one access-log line for when the response is final.
//  5. recoverer         -- catches a panic from anything below it
//     (metrics middleware, the router, or a handler) and turns it into a
//     clean 500 error envelope. It sits *inside* requestLogger
//     deliberately: by recovering before requestLogger's own deferred log
//     runs, that log line -- which already carries this request's id via
//     step 4 -- is the one line an operator needs to find a panic,
//     without a second, separate "logged from the panic handler" line to
//     correlate by hand.
//  6. metrics           -- records one ObserveHTTP call per request. It
//     sits *inside* recoverer so a panic is still recorded (with a 500,
//     via a recover/re-panic of its own -- see metricsMiddleware) rather
//     than silently missing from /metrics, while recoverer still gets to
//     handle the panic itself.
//  7. chimw.Timeout     -- bounds how long a handler may run. It is
//     innermost (closest to the handler) so requestLogger, recoverer and
//     metrics all still see its 504 like any other response. It is
//     applied per-route below, under /api/v1 only, so /healthz, /readyz
//     and /metrics are never subject to it.
func NewRouter(deps RouterDeps) http.Handler {
	r := chi.NewRouter()

	r.Use(
		chimw.RequestID,
		chimw.RealIP,
		observability.HTTPMiddleware(deps.TracerProvider, otelServiceName),
		newRequestLogger(deps.Logger),
		recoverer,
		metricsMiddleware(deps.Metrics),
	)

	h := &handlers{
		service:            deps.Service,
		health:             deps.Health,
		maxUploadBytes:     deps.MaxUploadBytes,
		uploadWriteTimeout: deps.RouteTimeout,
	}

	mountAPI(r, h, deps.RouteTimeout)

	r.Get("/healthz", h.healthz)
	r.Get("/readyz", h.readyz)
	if deps.MetricsEnabled {
		r.Handle("/metrics", deps.Metrics.Handler())
	}

	return r
}

// mountAPI registers every /api/v1/* route from CONTRACT.md section 6,
// all bounded by chimw.Timeout(routeTimeout) -- unlike BLUEPRINT, this
// service has no long-lived streaming endpoint that needs to opt out of
// it.
func mountAPI(r chi.Router, h *handlers, routeTimeout time.Duration) {
	r.Route("/api/v1", func(api chi.Router) {
		api.Use(chimw.Timeout(routeTimeout))

		api.Post("/transcriptions", h.submitTranscription)
		api.Get("/transcriptions", h.listTranscriptions)
		api.Get("/transcriptions/{id}", h.getTranscription)
		api.Delete("/transcriptions/{id}", h.deleteTranscription)
		api.Post("/transcriptions/{id}/cancel", h.cancelTranscription)
		api.Post("/transcriptions/{id}/retry", h.retryTranscription)
		api.Get("/transcriptions/{id}/transcript", h.getTranscript)
		api.Get("/transcriptions/{id}/audio", h.getAudio)
	})
}
