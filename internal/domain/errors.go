// Package domain holds the types, status machine and error taxonomy shared
// by every other package in Helios. It is a leaf package: it imports nothing
// internal, so anything importing it (postgres, localfs, whispercpp, audio,
// app, httpapi, jobs, retention) can share one vocabulary for a
// transcription's shape and its failure modes without risking an import
// cycle.
package domain

import (
	"context"
	"errors"
	"fmt"
)

// ErrorCode classifies why an operation failed. It is the single source of
// truth for both the HTTP status a failure maps to (internal/httpapi) and
// whether a failed job is worth retrying — see Retryable.
type ErrorCode string

const (
	// ErrCodeInvalidInput means the caller supplied a malformed or
	// self-contradictory request (e.g. invalid Options). Not retryable.
	ErrCodeInvalidInput ErrorCode = "invalid_input"
	// ErrCodeUnsupportedMedia means ffprobe cannot decode the uploaded
	// container or codec. Not retryable.
	ErrCodeUnsupportedMedia ErrorCode = "unsupported_media"
	// ErrCodeNoAudioStream means the file decodes but carries no audio
	// track. Not retryable.
	ErrCodeNoAudioStream ErrorCode = "no_audio_stream"
	// ErrCodePayloadTooLarge means the upload exceeded the configured max
	// byte count, or the probed audio exceeded the configured max
	// duration. Not retryable.
	ErrCodePayloadTooLarge ErrorCode = "payload_too_large"
	// ErrCodeNotFound means the referenced transcription does not exist.
	// Not retryable.
	ErrCodeNotFound ErrorCode = "not_found"
	// ErrCodeConflict means the request conflicts with the transcription's
	// current state (e.g. canceling an already-terminal job, or fetching
	// the transcript of one that has not succeeded). Not retryable.
	ErrCodeConflict ErrorCode = "conflict"
	// ErrCodeDiskFull means local storage rejected a write because free
	// space is exhausted. Not retryable until an operator frees space.
	ErrCodeDiskFull ErrorCode = "disk_full"
	// ErrCodeModelUnavailable means the requested whisper.cpp model weights
	// are not present on disk. Not retryable until an operator installs
	// them.
	ErrCodeModelUnavailable ErrorCode = "model_unavailable"
	// ErrCodeToolFailure means a subprocess (ffmpeg, ffprobe, whisper-cli)
	// exited non-zero or produced unparsable output for a reason not
	// otherwise classified. Transient — retryable, since tool flakiness is
	// common.
	ErrCodeToolFailure ErrorCode = "tool_failure"
	// ErrCodeTimeout means an operation exceeded its deadline. Transient —
	// retryable.
	ErrCodeTimeout ErrorCode = "timeout"
	// ErrCodeCanceled means the operation was canceled by the caller (e.g.
	// via the cancel endpoint or context cancellation). Not retryable: it
	// was intentional.
	ErrCodeCanceled ErrorCode = "canceled"
	// ErrCodeInternal means an unexpected, unclassified failure occurred.
	// NOT retryable: an unclassified error is a bug until proven otherwise,
	// and silently retrying it MaxAttempts times per job hides the bug
	// instead of surfacing it. A genuinely transient cause should be
	// classified as ToolFailure or Timeout at the point it is detected.
	// Operators can always retry by hand.
	ErrCodeInternal ErrorCode = "internal"
	// ErrCodeNetwork means a client could not reach the server at all --
	// connection refused, DNS failure, connection reset. It is the one code
	// in this taxonomy the SERVER never produces: it is classified by
	// internal/httpclient on the caller's side, and it exists here because
	// the taxonomy is shared by both halves and a caller genuinely needs to
	// tell "the server is not there" apart from "the server has a bug".
	// The desktop client's connection indicator turns on exactly this
	// distinction. Transient -- retryable.
	ErrCodeNetwork ErrorCode = "network"
)

// Retryable reports whether a failure of this ErrorCode is worth another
// attempt. It is true ONLY for ErrCodeToolFailure and ErrCodeTimeout — every
// other code represents a condition that will not change on retry (bad
// input, missing resource, permanent state conflict, exhausted disk,
// missing model, intentional cancellation, or an unclassified internal
// bug).
func (c ErrorCode) Retryable() bool {
	switch c {
	case ErrCodeToolFailure, ErrCodeTimeout, ErrCodeNetwork:
		return true
	default:
		return false
	}
}

// Error is the concrete error type used across all of Helios. It carries a
// classification (Code), a human-readable Message, optional structured
// Details for API responses, and an optional wrapped Err for root-cause
// diagnostics that should not be shown to callers.
//
// Error is stored verbatim as the `error` JSONB column on the
// transcriptions table (see internal/postgres), so its exported fields
// carry json tags and Err is excluded via `json:"-"` — a wrapped error is
// rarely itself marshalable and must never reach an API response anyway.
type Error struct {
	// Code classifies the failure. See ErrorCode.
	Code ErrorCode `json:"code"`
	// Message is a human-readable description safe to return to API
	// callers.
	Message string `json:"message"`
	// Details carries structured, machine-readable context (e.g. which
	// field was invalid). It is safe to return to API callers.
	Details map[string]any `json:"details,omitempty"`
	// Err is the underlying cause, if any. It is not shown to API callers,
	// not persisted, but preserved for logs and errors.As/errors.Is chains
	// via Unwrap.
	Err error `json:"-"`
}

// Error implements the error interface, formatting as "code: message" and
// appending ": <cause>" when a wrapped error is present.
func (e *Error) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("%s: %s: %v", e.Code, e.Message, e.Err)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

// Unwrap exposes the wrapped cause, if any, so errors.Is/errors.As and
// CodeOf can walk the chain.
func (e *Error) Unwrap() error {
	return e.Err
}

// WithDetails sets Details[k] = v, allocating Details if necessary, and
// returns e so callers can chain it onto New/Wrap in one expression, e.g.
// New(ErrCodeInvalidInput, "bad beam size").WithDetails("field", "beamSize").
func (e *Error) WithDetails(k string, v any) *Error {
	if e.Details == nil {
		e.Details = make(map[string]any)
	}
	e.Details[k] = v
	return e
}

// New builds an *Error with the given Code and Message and no wrapped
// cause.
func New(code ErrorCode, msg string) *Error {
	return &Error{Code: code, Message: msg}
}

// Wrap builds an *Error with the given Code and Message, wrapping err as
// its cause so it survives in logs and errors.As/errors.Is chains without
// ever being shown to callers.
func Wrap(code ErrorCode, msg string, err error) *Error {
	return &Error{Code: code, Message: msg, Err: err}
}

// CodeOf classifies err. It walks err's chain with errors.As looking for an
// *Error and returns its Code first; failing that, it recognises the two
// context package sentinels a subprocess deadline or a caller cancellation
// surface as — context.Canceled maps to ErrCodeCanceled and
// context.DeadlineExceeded maps to ErrCodeTimeout — since those are the
// only failures Helios that never gets wrapped in an *Error at the point of
// detection (they come straight out of ctx.Err()). Anything else, including
// nil, is ErrCodeInternal: callers can always treat the result as a
// definite classification.
func CodeOf(err error) ErrorCode {
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	switch {
	case errors.Is(err, context.Canceled):
		return ErrCodeCanceled
	case errors.Is(err, context.DeadlineExceeded):
		return ErrCodeTimeout
	default:
		return ErrCodeInternal
	}
}

// AsError returns err as an *Error: err itself when it already is (or
// wraps) one, a fresh *Error classified via CodeOf and wrapping err
// otherwise, or nil when err is nil. It lets callers that only have a plain
// error (e.g. from a context deadline) still populate FinishRequest.Err or
// an HTTP error envelope uniformly.
func AsError(err error) *Error {
	if err == nil {
		return nil
	}
	var e *Error
	if errors.As(err, &e) {
		return e
	}
	return Wrap(CodeOf(err), err.Error(), err)
}
