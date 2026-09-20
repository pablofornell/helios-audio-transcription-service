package domain

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestErrorCodeRetryable(t *testing.T) {
	cases := map[ErrorCode]bool{
		ErrCodeInvalidInput:     false,
		ErrCodeUnsupportedMedia: false,
		ErrCodeNoAudioStream:    false,
		ErrCodePayloadTooLarge:  false,
		ErrCodeNotFound:         false,
		ErrCodeConflict:         false,
		ErrCodeDiskFull:         false,
		ErrCodeModelUnavailable: false,
		ErrCodeToolFailure:      true,
		ErrCodeTimeout:          true,
		ErrCodeCanceled:         false,
		ErrCodeInternal:         false,
	}
	for code, want := range cases {
		assert.Equal(t, want, code.Retryable(), "code %q", code)
	}
}

func TestErrorMessage(t *testing.T) {
	plain := New(ErrCodeInvalidInput, "bad beam size")
	assert.Equal(t, "invalid_input: bad beam size", plain.Error())

	cause := errors.New("exit status 1")
	wrapped := Wrap(ErrCodeToolFailure, "whisper-cli failed", cause)
	assert.Equal(t, "tool_failure: whisper-cli failed: exit status 1", wrapped.Error())
	assert.Equal(t, cause, wrapped.Unwrap())
}

func TestErrorWithDetails(t *testing.T) {
	err := New(ErrCodeInvalidInput, "bad language").WithDetails("field", "language")
	require.NotNil(t, err.Details)
	assert.Equal(t, "language", err.Details["field"])

	// Chaining a second call adds to the same map rather than replacing it.
	err.WithDetails("value", "xx-yy")
	assert.Equal(t, "language", err.Details["field"])
	assert.Equal(t, "xx-yy", err.Details["value"])
}

func TestCodeOf(t *testing.T) {
	t.Run("nil error", func(t *testing.T) {
		assert.Equal(t, ErrCodeInternal, CodeOf(nil))
	})

	t.Run("plain error with no *Error in chain", func(t *testing.T) {
		assert.Equal(t, ErrCodeInternal, CodeOf(errors.New("boom")))
	})

	t.Run("direct *Error", func(t *testing.T) {
		err := New(ErrCodeNotFound, "transcription missing")
		assert.Equal(t, ErrCodeNotFound, CodeOf(err))
	})

	t.Run("wrapped *Error", func(t *testing.T) {
		root := New(ErrCodeToolFailure, "ffmpeg exited 1")
		outer := Wrap(ErrCodeInternal, "normalize failed", root)
		// CodeOf finds the outermost *Error in the chain.
		assert.Equal(t, ErrCodeInternal, CodeOf(outer))
	})

	t.Run("context.Canceled maps to ErrCodeCanceled", func(t *testing.T) {
		assert.Equal(t, ErrCodeCanceled, CodeOf(context.Canceled))
	})

	t.Run("context.DeadlineExceeded maps to ErrCodeTimeout", func(t *testing.T) {
		assert.Equal(t, ErrCodeTimeout, CodeOf(context.DeadlineExceeded))
	})

	t.Run("context.Canceled wrapped by a plain error still maps", func(t *testing.T) {
		wrapped := errors.New("exec: " + context.Canceled.Error())
		// Not wrapped via %w, so errors.Is cannot see through it: this must
		// NOT be misclassified as canceled, it must fall back to internal.
		assert.Equal(t, ErrCodeInternal, CodeOf(wrapped))

		properlyWrapped := errorsJoinWrap(context.Canceled)
		assert.Equal(t, ErrCodeCanceled, CodeOf(properlyWrapped))
	})

	t.Run("a *Error takes priority over an underlying context error", func(t *testing.T) {
		// CodeOf must report the outermost *Error's classification, not
		// reclassify based on a wrapped context error underneath it.
		err := Wrap(ErrCodeTimeout, "whisper-cli deadline", context.DeadlineExceeded)
		assert.Equal(t, ErrCodeTimeout, CodeOf(err))
	})
}

// errorsJoinWrap wraps err with %w so errors.Is can see through it, the way
// a real caller (e.g. exec.CommandContext's returned error) would.
func errorsJoinWrap(err error) error {
	return &wrapErr{err}
}

type wrapErr struct{ err error }

func (w *wrapErr) Error() string { return "wrapped: " + w.err.Error() }
func (w *wrapErr) Unwrap() error { return w.err }

func TestAsError(t *testing.T) {
	t.Run("nil is nil", func(t *testing.T) {
		assert.Nil(t, AsError(nil))
	})

	t.Run("already an *Error is returned as-is", func(t *testing.T) {
		orig := New(ErrCodeNotFound, "missing")
		assert.Same(t, orig, AsError(orig))
	})

	t.Run("a plain error is classified and wrapped", func(t *testing.T) {
		cause := errors.New("boom")
		got := AsError(cause)
		require.NotNil(t, got)
		assert.Equal(t, ErrCodeInternal, got.Code)
		assert.Equal(t, cause, got.Unwrap())
	})

	t.Run("context.DeadlineExceeded is classified as timeout", func(t *testing.T) {
		got := AsError(context.DeadlineExceeded)
		require.NotNil(t, got)
		assert.Equal(t, ErrCodeTimeout, got.Code)
	})
}

// TestErrorJSONRoundTrip guards the exact requirement that makes *Error
// storable as the `error` JSONB column: Code/Message/Details round-trip,
// and Err (never JSON-safe, never meant for callers) is excluded entirely
// rather than surfacing as a zero value or an encoding error.
func TestErrorJSONRoundTrip(t *testing.T) {
	original := Wrap(ErrCodeToolFailure, "whisper-cli exited 1", errors.New("root cause")).
		WithDetails("stderr", "segfault")

	data, err := json.Marshal(original)
	require.NoError(t, err)
	assert.NotContains(t, string(data), "root cause", "Err must never be marshaled")
	assert.NotContains(t, string(data), `"err"`)

	var decoded Error
	require.NoError(t, json.Unmarshal(data, &decoded))
	assert.Equal(t, original.Code, decoded.Code)
	assert.Equal(t, original.Message, decoded.Message)
	assert.Equal(t, original.Details["stderr"], decoded.Details["stderr"])
	assert.Nil(t, decoded.Err, "Err has no JSON representation to decode from")
}

func TestErrorJSONRoundTrip_NoDetailsOmitsKey(t *testing.T) {
	data, err := json.Marshal(New(ErrCodeInternal, "unclassified"))
	require.NoError(t, err)
	assert.NotContains(t, string(data), "details")
}
