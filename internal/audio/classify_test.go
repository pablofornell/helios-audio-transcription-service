package audio

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/pablofornell/helios-audio-transcription-service/internal/domain"
)

func TestClassify_table(t *testing.T) {
	cases := []struct {
		name   string
		stderr string
		err    error
		want   domain.ErrorCode
	}{
		{"deadline exceeded", "", context.DeadlineExceeded, domain.ErrCodeTimeout},
		{"canceled", "", context.Canceled, domain.ErrCodeCanceled},
		{"invalid data", "Invalid data found when processing input", nil, domain.ErrCodeUnsupportedMedia},
		{"codec parameters", "could not find codec parameters", nil, domain.ErrCodeUnsupportedMedia},
		{"moov atom", "moov atom not found", nil, domain.ErrCodeUnsupportedMedia},
		{"case insensitive", "INVALID DATA FOUND WHEN PROCESSING INPUT", nil, domain.ErrCodeUnsupportedMedia},
		{"no space left", "No space left on device", nil, domain.ErrCodeDiskFull},
		{"unrecognised", "some other ffmpeg complaint", nil, domain.ErrCodeToolFailure},
		{"no stderr, no err", "", nil, domain.ErrCodeToolFailure},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, classify(tc.stderr, 1, tc.err))
		})
	}
}

func TestClassify_wrappedContextErrors(t *testing.T) {
	wrapped := errors.Join(errors.New("run failed"), context.DeadlineExceeded)
	assert.Equal(t, domain.ErrCodeTimeout, classify("", -1, wrapped))
}

func TestClassifyLocalErr_downgradesUnrecognisedToInternal(t *testing.T) {
	assert.Equal(t, domain.ErrCodeInternal, classifyLocalErr(errors.New("permission denied")))
}

func TestWithStderrTail_nilErrorPassesThrough(t *testing.T) {
	assert.Nil(t, withStderrTail(nil, runResult{}))
}

func TestWithStderrTail_attachesDetails(t *testing.T) {
	base := domain.New(domain.ErrCodeToolFailure, "boom")
	got := withStderrTail(base, runResult{ExitCode: 7, Stderr: "the tail"})
	assert.Equal(t, 7, got.Details["exitCode"])
	assert.Equal(t, "the tail", got.Details["stderr"])
}

func TestRingBuffer_keepsOnlyTail(t *testing.T) {
	rb := newRingBuffer(5)
	_, _ = rb.Write([]byte("hello"))
	_, _ = rb.Write([]byte("world"))
	assert.Equal(t, "world", rb.String())
}
