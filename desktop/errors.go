package main

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/pablofornell/helios-audio-transcription-service/internal/domain"
)

// ErrorDTO is what every error returned from a bound method looks like on
// the wire, mirroring httpapi's own errorDTO / errorResponseDTO.Error --
// the same envelope the REST front door uses, so a domain.ErrorCode like
// domain.ErrCodeModelUnavailable reads identically whichever front door
// produced it.
type ErrorDTO struct {
	Code    domain.ErrorCode `json:"code"`
	Message string           `json:"message"`
	Details map[string]any   `json:"details,omitempty"`
}

// formatError is wired in as options.App.ErrorFormatter (see main.go).
//
// Wails v2's dispatcher rejects a bound method's promise with
// `new Error(message.error)` on the frontend (see
// wailsapp/wails/v2/internal/frontend/dispatcher/calls.go and
// .../runtime/desktop/calls.js): whatever this function returns becomes
// that Error's .message, stringified. A struct or map would stringify to
// the useless "[object Object]", so this returns a JSON string instead --
// the frontend recovers the structured ErrorDTO with
// `JSON.parse(err.message)`. That is the only way to carry
// domain.ErrorCode across this particular binding boundary without
// flattening it into prose the UI cannot key model_unavailable (etc.) off
// of.
func formatError(err error) any {
	dto := errorDTOFromErr(err)
	if encoded, marshalErr := json.Marshal(dto); marshalErr == nil {
		return string(encoded)
	}
	// dto failed to encode — in practice this means Details carries
	// something json.Marshal refuses (NaN, a channel, a cycle), since
	// Code and Message are always plain strings. The frontend's
	// `JSON.parse(err.message)` must still succeed and Code must still
	// survive the trip, so retry without Details rather than falling back
	// to err.Error(), a bare, non-JSON string that would break
	// JSON.parse outright and lose the code along with it.
	safe := ErrorDTO{Code: dto.Code, Message: dto.Message}
	if encoded, marshalErr := json.Marshal(safe); marshalErr == nil {
		return string(encoded)
	}
	// dto.Code and dto.Message are both plain strings; encoding just those
	// two should never itself fail. If it somehow still does, this
	// hand-built literal keeps the envelope valid JSON and the code
	// intact — the one thing this function must never give up on.
	return fmt.Sprintf(`{"code":%q,"message":%q}`, dto.Code, "an internal error occurred")
}

// errorDTOFromErr classifies err via domain.CodeOf, which already returns
// domain.ErrCodeInternal for a nil or unclassified error, so every call
// into a bound method that fails ends up with a definite Code the
// frontend can switch on.
func errorDTOFromErr(err error) ErrorDTO {
	var derr *domain.Error
	if errors.As(err, &derr) {
		return ErrorDTO{Code: derr.Code, Message: derr.Message, Details: derr.Details}
	}
	// err carries no *domain.Error anywhere in its chain. Every path
	// through transcriptionAPI that can fail is expected to wrap its own
	// cause in one (see internal/httpclient) — reaching here means either
	// a bug (a stray fmt.Errorf/errors.New slipping past that discipline)
	// or a lower-level error type escaping unwrapped. Either way,
	// err.Error() is not safe to hand the renderer verbatim: an arbitrary
	// unwrapped error can carry a path, a hostname, or other detail this
	// boundary has no business leaking. Only domain.CodeOf(err)'s
	// classification crosses this boundary; the detail goes to the log
	// instead, via logf's own nil-ctx fallback (this function has no
	// Wails runtime context to hand it — formatError is called from
	// outside any bound method).
	logf(nil, "desktop: unclassified error reached the error formatter: %s", err.Error())
	return ErrorDTO{Code: domain.CodeOf(err), Message: "an internal error occurred"}
}
