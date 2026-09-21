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

// corsAllowedMethods and corsAllowedHeaders are this API's fixed preflight
// answer, covering exactly what desktop/frontend/src/lib/browser actually
// sends: GET (list/get/health), POST (submit/retry/cancel), DELETE
// (deleteTranscription) and OPTIONS itself; Content-Type (set whenever a
// request carries a JSON body, and left unset for the multipart upload —
// the browser sets its own boundary-carrying Content-Type there, which is
// still just "Content-Type") and Accept (sent on every request, and
// already CORS-safelisted, but listed anyway so an allowed preflight never
// has to omit a header the browser's actual request will go on to send).
// Range is listed defensively: the <audio> element getAudio streams to
// (features/library/DetailPanel.tsx) has no `crossorigin` attribute, so a
// browser only ever applies CORS to a "cors"-mode request, and a plain
// `<audio src>` is "no-cors" -- its own Range-bearing seek requests never
// go through this middleware's preflight check at all. Listed here purely
// defensively, so a future fetch-based Range request (e.g. a feature that
// needs to inspect the response itself, which would run in "cors" mode)
// does not have to touch this list to start working.
//
// There is deliberately no Access-Control-Expose-Headers here: that only
// matters for a response header client-side JavaScript reads directly (via
// `Response.headers.get`), and desktop/frontend/src/lib/browser does not
// read any — getAudio's Content-Disposition/Content-Range/Accept-Ranges
// are consumed by the browser's own <audio> element, never by this
// module's fetch() calls, and getTranscript's body is read as plain text,
// not its headers. Add one here, naming the specific header, the day a
// browser feature actually needs it.
const (
	corsAllowedMethods = "GET, POST, DELETE, OPTIONS"
	corsAllowedHeaders = "Content-Type, Accept, Range"
	// corsMaxAgeSeconds bounds how long a browser may cache one preflight
	// answer before re-asking — 10 minutes, generous enough that a normal
	// session issues at most a handful of preflights, short enough that a
	// HELIOS_CORS_ALLOWED_ORIGINS change deployed today is not still being
	// worked around by a cached "allowed" (or "disallowed") answer hours
	// later.
	corsMaxAgeSeconds = "600"
)

// newCORSMiddleware returns CORS-handling middleware for allowedOrigins —
// config.CORSConfig.AllowedOrigins, an exact-match allowlist (see that
// field's own doc for why there is no wildcard). Returns next completely
// unwrapped when allowedOrigins is empty (the default): no Origin header
// is ever inspected and no Access-Control-* or Vary header is ever
// written, so every deployment that has not opted in keeps exactly the
// same behaviour it had before this middleware existed.
//
// Once enabled: a request that carries no Origin header at all — same-
// origin fetches, curl, server-to-server calls, and every request this API
// answered before CORS existed — passes straight through untouched, since
// CORS is a browser-enforced, cross-origin-only mechanism with nothing to
// decide for one that was never cross-origin in the first place. A request
// whose Origin is not in allowedOrigins also passes straight through
// unmodified rather than being rejected here (no Access-Control-Allow-
// Origin, so the requesting browser — not this handler — is what actually
// blocks it from reaching the calling page's script; a non-browser caller,
// which CORS was never enforced against to begin with, is never broken by
// a policy it was never subject to).
//
// A preflight — method OPTIONS carrying Access-Control-Request-Method — is
// answered here directly, for an allowed origin, without ever reaching the
// router: NewRouter's middleware stack wraps route dispatch itself (see
// its own doc), so returning here without calling next is enough; no
// route needs to be registered for OPTIONS on any path. A preflight for a
// disallowed origin instead falls through to next exactly like any other
// disallowed-origin request above — chi will answer it however it answers
// any other unregistered-method request, which does not matter: the
// browser already refuses the caller access based on the missing
// Access-Control-Allow-Origin, regardless of what status code comes back.
func newCORSMiddleware(allowedOrigins []string) func(http.Handler) http.Handler {
	if len(allowedOrigins) == 0 {
		return func(next http.Handler) http.Handler { return next }
	}
	allowed := make(map[string]bool, len(allowedOrigins))
	for _, origin := range allowedOrigins {
		allowed[origin] = true
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")
			if origin == "" {
				next.ServeHTTP(w, r)
				return
			}

			// Every response whose CORS headers depend on Origin must say
			// so, or a cache sitting in front of this API (a CDN, or the
			// browser's own HTTP cache) could serve one origin's allowed
			// response to a different, disallowed one.
			w.Header().Add("Vary", "Origin")

			if !allowed[origin] {
				next.ServeHTTP(w, r)
				return
			}

			w.Header().Set("Access-Control-Allow-Origin", origin)

			if r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != "" {
				w.Header().Set("Access-Control-Allow-Methods", corsAllowedMethods)
				w.Header().Set("Access-Control-Allow-Headers", corsAllowedHeaders)
				w.Header().Set("Access-Control-Max-Age", corsMaxAgeSeconds)
				w.WriteHeader(http.StatusNoContent)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
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
