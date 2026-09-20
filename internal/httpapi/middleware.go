package httpapi

import (
	"log/slog"
	"net/http"
	"runtime/debug"
	"time"

	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"

	"github.com/pablofornell/helios-audio-transcription-service/internal/domain"
	"github.com/pablofornell/helios-audio-transcription-service/internal/observability"
)

// newRequestLogger returns middleware that seeds the request context with
// base (so every FromContext(ctx) call downstream gets the service's
// configured logger, not observability's bare internal default), attaches
// this request's id via observability.WithRequestID (reading the id
// chimw.RequestID already generated or forwarded), then logs one line per
// request on the way out: method, path, matched route pattern, status and
// duration.
//
// It sits outside recoverer (see NewRouter's middleware stack comment) so
// that by the time its deferred log line runs, recoverer has already
// turned any panic into a final status code -- the access log always
// reflects what the client actually received, panic or not.
func newRequestLogger(base *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id := chimw.GetReqID(r.Context())
			ctx := observability.ContextWithLogger(r.Context(), base)
			ctx = observability.WithRequestID(ctx, id)
			r = r.WithContext(ctx)

			ww := chimw.NewWrapResponseWriter(w, r.ProtoMajor)
			start := time.Now()
			defer func() {
				route := chi.RouteContext(r.Context()).RoutePattern()
				observability.FromContext(r.Context()).Info("http request",
					"method", r.Method,
					"path", r.URL.Path,
					"route", route,
					"status", ww.Status(),
					"bytes", ww.BytesWritten(),
					"duration_ms", time.Since(start).Milliseconds(),
				)
			}()

			next.ServeHTTP(ww, r)
		})
	}
}

// recoverer catches a panic from any inner handler or middleware, logs it
// (with the request id already attached by requestLogger, since recoverer
// sits inside it) at Error level with a stack trace, and completes the
// response as a 500 Internal Server Error using the same error envelope
// every other failure uses -- a client never sees a bare panic trace or a
// broken connection where a JSON error belongs.
//
// It deliberately does not recover http.ErrAbortHandler, matching
// chimw.Recoverer: that sentinel means the handler wants the connection
// aborted without a response, and must keep propagating.
func recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			rvr := recover()
			if rvr == nil {
				return
			}
			if rvr == http.ErrAbortHandler {
				panic(rvr)
			}

			observability.FromContext(r.Context()).Error("panic recovered",
				"method", r.Method,
				"path", r.URL.Path,
				"panic", rvr,
				"stack", string(debug.Stack()),
			)

			if ww, ok := w.(chimw.WrapResponseWriter); ok && ww.Status() != 0 {
				// Headers (and maybe a partial body) were already sent;
				// writing another status/body now would just error out
				// silently on the wire. Nothing more we can do.
				return
			}
			writeJSON(w, http.StatusInternalServerError, errorResponseDTO{
				Error: errorDTO{
					Code:    domain.ErrCodeInternal,
					Message: "an internal error occurred",
				},
			})
		}()

		next.ServeHTTP(w, r)
	})
}

// metricsMiddleware records one observability.Metrics.ObserveHTTP call per
// request, labelled with chi's matched route PATTERN
// (chi.RouteContext(r.Context()).RoutePattern()) rather than r.URL.Path --
// see Metrics.ObserveHTTP's doc for why a raw path is an unbounded-
// cardinality outage waiting to happen (raw transcription paths contain
// UUIDs). The pattern is only populated once chi has finished routing,
// which happens inside next.ServeHTTP, so it is read from a deferred
// closure rather than up front.
//
// The deferred read-and-record is wrapped in its own recover/re-panic: if
// the inner handler panics, this still records a representative status
// (500, since that is what recoverer -- outside this middleware in the
// stack -- is about to write) instead of silently recording nothing, and
// then re-panics so recoverer still gets to run.
func metricsMiddleware(m *observability.Metrics) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ww := chimw.NewWrapResponseWriter(w, r.ProtoMajor)
			start := time.Now()

			defer func() {
				status := ww.Status()
				rvr := recover()
				if rvr != nil && status == 0 {
					status = http.StatusInternalServerError
				}

				route := chi.RouteContext(r.Context()).RoutePattern()
				if route == "" {
					route = "unmatched"
				}
				m.ObserveHTTP(metricsMethod(r.Method), route, status, time.Since(start))

				if rvr != nil {
					panic(rvr)
				}
			}()

			next.ServeHTTP(ww, r)
		})
	}
}

// standardHTTPMethods is every method the router actually dispatches on
// (see mountAPI/NewRouter) plus the handful of others a well-behaved
// client or proxy might legitimately send. It exists solely to bound
// metricsMethod's output.
var standardHTTPMethods = map[string]bool{
	http.MethodGet:     true,
	http.MethodHead:    true,
	http.MethodPost:    true,
	http.MethodPut:     true,
	http.MethodPatch:   true,
	http.MethodDelete:  true,
	http.MethodOptions: true,
	http.MethodConnect: true,
	http.MethodTrace:   true,
}

// metricsMethod maps method to itself when it is one of the standard
// HTTP/1.1 methods above, and to "other" otherwise. Go's server accepts
// any RFC 7230 token as a method, so an unauthenticated caller sending
// `curl -X RANDOM1`, `curl -X RANDOM2`, ... would otherwise mint a new
// Prometheus counter/histogram child series per distinct token -- an
// unbounded-cardinality memory leak matching the one Metrics.ObserveHTTP's
// own "route" handling already guards against (see the "unmatched"
// fallback above). Collapsing to one shared label value closes the same
// hole for "method".
func metricsMethod(method string) string {
	if standardHTTPMethods[method] {
		return method
	}
	return "other"
}
