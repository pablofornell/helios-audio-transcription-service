package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/pablofornell/helios-audio-transcription-service/internal/domain"
	"github.com/pablofornell/helios-audio-transcription-service/internal/observability"
)

// statusForCode is CONTRACT.md section 6's error code -> HTTP status table,
// made executable. It is the single place that mapping lives: every
// handler funnels a failed operation through writeError instead of picking
// a status itself, so this table can never drift out from under a handler
// that "knows" a code always means some other status.
var statusForCode = map[domain.ErrorCode]int{
	domain.ErrCodeInvalidInput:     http.StatusBadRequest,
	domain.ErrCodeUnsupportedMedia: http.StatusUnsupportedMediaType,
	domain.ErrCodeNoAudioStream:    http.StatusUnprocessableEntity,
	domain.ErrCodePayloadTooLarge:  http.StatusRequestEntityTooLarge,
	domain.ErrCodeNotFound:         http.StatusNotFound,
	domain.ErrCodeConflict:         http.StatusConflict,
	domain.ErrCodeDiskFull:         http.StatusInsufficientStorage,
	domain.ErrCodeModelUnavailable: http.StatusServiceUnavailable,
	domain.ErrCodeToolFailure:      http.StatusBadGateway,
	domain.ErrCodeTimeout:          http.StatusGatewayTimeout,
	// statusClientClosedRequest (499) is not one of net/http's named
	// constants -- nginx's "client closed request" convention, which is
	// what CONTRACT.md's error table borrows for a canceled request.
	domain.ErrCodeCanceled: statusClientClosedRequest,
	domain.ErrCodeInternal: http.StatusInternalServerError,
}

// statusClientClosedRequest is the (non-standard, but widely recognised)
// status CONTRACT.md maps domain.ErrCodeCanceled to.
const statusClientClosedRequest = 499

// writeError is the single place a handler turns a failed operation into
// an HTTP response. It consults domain.CodeOf(err) against statusForCode,
// writes the errorResponseDTO envelope, and -- for any 5xx -- logs the
// request with the underlying cause via observability.FromContext(r.Context()),
// which already carries this request's request_id.
//
// The response body's message comes from *domain.Error.Message -- which
// domain.Error documents as always safe to return to a caller, whatever
// its Code -- never from err.Error(), which for a *domain.Error would also
// stringify the wrapped, unsafe Err (a driver error, a path, ...). When
// err is not a *domain.Error at all (a bug: every Service method is
// documented to return one), a fixed placeholder is used instead of
// trusting the bare error's text.
func writeError(w http.ResponseWriter, r *http.Request, err error) {
	code := domain.CodeOf(err)
	status, ok := statusForCode[code]
	if !ok {
		// Not in the table at all (should be unreachable: CodeOf always
		// returns a known ErrorCode) -- fail closed as an internal error
		// rather than default to 200.
		code = domain.ErrCodeInternal
		status = http.StatusInternalServerError
	}

	message := "an internal error occurred"
	var details map[string]any
	var derr *domain.Error
	if errors.As(err, &derr) {
		message = derr.Message
		details = derr.Details
	}

	if status >= 500 {
		observability.FromContext(r.Context()).Error("request failed",
			"method", r.Method,
			"path", r.URL.Path,
			"status", status,
			"code", string(code),
			"error", err,
		)
	}

	writeJSON(w, status, errorResponseDTO{Error: errorDTO{
		Code:    code,
		Message: message,
		Details: details,
	}})
}

// writeJSON encodes v as status's JSON body. It is used for every
// response, success or error, so Content-Type is set consistently.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
