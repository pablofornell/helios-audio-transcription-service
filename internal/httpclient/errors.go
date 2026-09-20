package httpclient

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strings"

	"github.com/pablofornell/helios-audio-transcription-service/internal/domain"
)

// fallbackCodeForStatus is the reverse of internal/httpapi/errors.go's
// statusForCode table (see docs/openapi.yaml's info.description for the
// table itself), used ONLY when a response cannot be parsed as the
// ErrorResponse envelope -- a malformed body, or a response from something
// that is not this API at all (a proxy's HTML error page, a load
// balancer's 502). When the envelope IS present, classifyError reads its
// "error.code" field directly instead of consulting this table. Unlike
// some sibling services, every ErrorCode here maps to exactly one HTTP
// status and vice versa (see statusForCode's own doc), so this table's
// entries are never a guess.
var fallbackCodeForStatus = map[int]domain.ErrorCode{
	http.StatusBadRequest:            domain.ErrCodeInvalidInput,
	http.StatusUnsupportedMediaType:  domain.ErrCodeUnsupportedMedia,
	http.StatusUnprocessableEntity:   domain.ErrCodeNoAudioStream,
	http.StatusRequestEntityTooLarge: domain.ErrCodePayloadTooLarge,
	http.StatusNotFound:              domain.ErrCodeNotFound,
	http.StatusConflict:              domain.ErrCodeConflict,
	http.StatusInsufficientStorage:   domain.ErrCodeDiskFull,
	http.StatusServiceUnavailable:    domain.ErrCodeModelUnavailable,
	http.StatusBadGateway:            domain.ErrCodeToolFailure,
	http.StatusGatewayTimeout:        domain.ErrCodeTimeout,
	statusClientClosedRequest:        domain.ErrCodeCanceled,
	http.StatusInternalServerError:   domain.ErrCodeInternal,
}

// statusClientClosedRequest mirrors internal/httpapi/errors.go's constant
// of the same name: the non-standard 499 this API uses for
// domain.ErrCodeCanceled.
const statusClientClosedRequest = 499

// errorEnvelope mirrors the `{"error":{"code","message","details"}}`
// envelope every non-2xx JSON response in this API uses. domain.Error's
// own json tags already match it field for field (Err is `json:"-"`, so it
// stays nil on decode) -- see internal/httpapi/dto.go's errorDTO and this
// package's doc comment.
type errorEnvelope struct {
	Error domain.Error `json:"error"`
}

// classifyError turns a non-2xx response into a *domain.Error, never a bare
// string and never a panic.
//
// The happy path is a straight read of docs/openapi.yaml's ErrorResponse
// envelope: the server already classified this failure once (see
// internal/httpapi/errors.go's writeError), and body's "error.code" field
// IS that domain.ErrorCode, verbatim. Reconstructing *domain.Error from it
// directly -- rather than mapping the HTTP status back to a code -- is what
// guarantees a caller sees the exact code the server assigned even for a
// status this table cannot otherwise disambiguate.
//
// Only when body is not that envelope at all (empty, truncated, HTML, or
// JSON with no recognisable "error.code") does this fall back to
// fallbackCodeForStatus, best-effort classifying by status alone and
// including a bounded, human-readable snippet of the actual body so the
// failure is still diagnosable.
func classifyError(status int, body []byte) *domain.Error {
	var env errorEnvelope
	if err := json.Unmarshal(body, &env); err == nil && env.Error.Code != "" {
		e := env.Error
		return &e
	}

	code, ok := fallbackCodeForStatus[status]
	if !ok {
		code = domain.ErrCodeInternal
	}
	snippet := snippetOf(body)
	if snippet == "" {
		return domain.New(code, "helios-ats server returned status "+http.StatusText(status)+" with no error body")
	}
	return domain.New(code, "helios-ats server returned status "+http.StatusText(status)+": "+snippet)
}

// snippetOf returns a short, single-line, human-readable prefix of body
// suitable for embedding in an error message -- bounded so a hostile or
// merely large non-JSON response (an HTML error page, a truncated proxy
// body) can never make an error message unbounded.
func snippetOf(body []byte) string {
	if len(body) > maxErrorBodyBytes {
		body = body[:maxErrorBodyBytes]
	}
	s := strings.TrimSpace(string(body))
	s = strings.Join(strings.Fields(s), " ")
	const maxLen = 200
	if len(s) > maxLen {
		s = s[:maxLen] + "..."
	}
	return s
}

// classifyTransportErr turns a failure to even complete the HTTP
// round-trip -- a dial failure, a connection reset, a read error, ctx
// cancellation or expiry -- into a *domain.Error. It never returns nil: a
// transport failure is always classified as something a caller can branch
// on, rather than surfaced as err.Error()'s raw (and potentially
// implementation-revealing, e.g. a DSN-shaped dial target) text.
//
// Unlike some sibling services' httpclient, internal/domain's ErrorCode
// taxonomy has no dedicated "network" code (see internal/domain/errors.go):
// every code here describes a failure mode specific to a transcription job
// or the local resources running it. A ctx deadline or an underlying
// net.Error's own Timeout() maps to ErrCodeTimeout, and ctx cancellation
// maps to ErrCodeCanceled -- both exact, existing fits. Anything else (a
// dial failure, connection reset, DNS failure -- the server was simply
// unreachable) maps to ErrCodeInternal: the least wrong of the remaining
// codes, since none of them mean "this API was unreachable" and
// ErrCodeInternal is the only one that does not imply a specific,
// job-shaped cause the caller could act on differently. The original error
// is always preserved via Wrap so a caller or log can still see the real
// cause.
func classifyTransportErr(ctx context.Context, err error) *domain.Error {
	if err == nil {
		return nil
	}

	// Prefer ctx's own state over inspecting err: net/http wraps context
	// cancellation/expiry inconsistently across Go versions and transport
	// internals, but ctx.Err() is always authoritative for why this
	// request's context ended.
	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		return domain.Wrap(domain.ErrCodeTimeout, "request to helios-ats server timed out", err)
	case errors.Is(ctx.Err(), context.Canceled):
		return domain.Wrap(domain.ErrCodeCanceled, "request to helios-ats server was canceled", err)
	}

	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return domain.Wrap(domain.ErrCodeTimeout, "request to helios-ats server timed out", err)
	}

	// Anything left is a genuine transport failure -- connection refused,
	// DNS, reset mid-flight. That is ErrCodeNetwork, deliberately NOT
	// ErrCodeInternal: a caller showing "the server is unreachable" and a
	// caller showing "the server hit a bug" want different words, different
	// retry behaviour, and in the desktop client a different screen.
	return domain.Wrap(domain.ErrCodeNetwork, "cannot reach the helios-ats server", err)
}
