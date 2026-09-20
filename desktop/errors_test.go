package main

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/pablofornell/helios-audio-transcription-service/internal/domain"
)

// TestFormatError_DomainError proves the ordinary path: a *domain.Error's
// Code, Message and Details all cross the boundary, and its wrapped
// cause never does.
func TestFormatError_DomainError(t *testing.T) {
	cause := errors.New("dial tcp: connect to host=db user=helios password=hunter2 failed")
	err := domain.Wrap(domain.ErrCodeModelUnavailable, "model weights are missing", cause)

	got := decodeFormattedError(t, formatError(err))
	if got.Code != domain.ErrCodeModelUnavailable {
		t.Errorf("Code = %q, want %q", got.Code, domain.ErrCodeModelUnavailable)
	}
	if got.Message != "model weights are missing" {
		t.Errorf("Message = %q, want %q", got.Message, "model weights are missing")
	}
	if strings.Contains(got.Message, "hunter2") {
		t.Errorf("Message %q leaks the wrapped cause", got.Message)
	}
}

// TestFormatError_UnwrappedErrorIsRedacted proves an error with no
// *domain.Error anywhere in its chain must not reach the renderer
// verbatim: errorDTOFromErr's fallback branch must never ship err.Error()
// unchanged, since a bare fmt.Errorf carrying a connection string (or a
// password, as here) would otherwise leak straight to the frontend.
func TestFormatError_UnwrappedErrorIsRedacted(t *testing.T) {
	err := errors.New("dial tcp: connect to host=db user=helios password=hunter2 failed")

	got := decodeFormattedError(t, formatError(err))
	if got.Code != domain.ErrCodeInternal {
		t.Errorf("Code = %q, want %q", got.Code, domain.ErrCodeInternal)
	}
	if strings.Contains(got.Message, "hunter2") || strings.Contains(got.Message, "password") {
		t.Errorf("Message %q leaks the unwrapped error's detail", got.Message)
	}
}

// TestFormatError_MarshalFailureStillYieldsValidEnvelope proves a
// *domain.Error whose Details cannot be JSON-encoded (here, a channel
// value — the same failure mode as a NaN float or a cyclic map) must
// still produce a valid JSON envelope carrying Code, not a bare, non-JSON
// err.Error() string that would break the frontend's
// JSON.parse(err.message) and lose the code along with it.
func TestFormatError_MarshalFailureStillYieldsValidEnvelope(t *testing.T) {
	err := &domain.Error{
		Code:    domain.ErrCodeModelUnavailable,
		Message: "model weights are missing",
		Details: map[string]any{"unencodable": make(chan int)},
	}

	result := formatError(err)
	encoded, ok := result.(string)
	if !ok {
		t.Fatalf("formatError returned %T, want string", result)
	}
	if !json.Valid([]byte(encoded)) {
		t.Fatalf("formatError returned invalid JSON: %s", encoded)
	}
	got := decodeFormattedError(t, result)
	if got.Code != domain.ErrCodeModelUnavailable {
		t.Errorf("Code = %q, want %q", got.Code, domain.ErrCodeModelUnavailable)
	}
}

// decodeFormattedError asserts result is a string (formatError's own
// contract, per its doc — a struct would stringify to "[object Object]" on
// the frontend) and JSON.parses it into an ErrorDTO.
func decodeFormattedError(t *testing.T, result any) ErrorDTO {
	t.Helper()
	encoded, ok := result.(string)
	if !ok {
		t.Fatalf("formatError returned %T, want string", result)
	}
	var dto ErrorDTO
	if err := json.Unmarshal([]byte(encoded), &dto); err != nil {
		t.Fatalf("json.Unmarshal(%q): %v", encoded, err)
	}
	return dto
}
