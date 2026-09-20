package whispercpp

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/pablofornell/helios-audio-transcription-service/internal/domain"
)

func TestClassify_table(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want domain.ErrorCode
	}{
		{"deadline exceeded", context.DeadlineExceeded, domain.ErrCodeTimeout},
		{"canceled", context.Canceled, domain.ErrCodeCanceled},
		{"nil err, nonzero exit", nil, domain.ErrCodeToolFailure},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, classify(1, tc.err))
		})
	}
}

func TestClassify_toolFailureIsRetryable(t *testing.T) {
	assert.True(t, domain.ErrCodeToolFailure.Retryable())
	assert.True(t, domain.ErrCodeTimeout.Retryable())
	assert.False(t, domain.ErrCodeCanceled.Retryable())
}

func TestClassifyRun_stderrDiskFullOverridesDefault(t *testing.T) {
	res := runResult{ExitCode: 1, Stderr: "fatal error writing output: No space left on device"}
	assert.Equal(t, domain.ErrCodeDiskFull, classifyRun(res))
}

func TestClassifyRun_defaultsToolFailure(t *testing.T) {
	res := runResult{ExitCode: 1, Stderr: "whisper_init_from_file_with_params_no_state: failed to load model"}
	assert.Equal(t, domain.ErrCodeToolFailure, classifyRun(res))
}

func TestWithStderrTail_nilPassesThrough(t *testing.T) {
	assert.Nil(t, withStderrTail(nil, runResult{}))
}

func TestWithStderrTail_attachesDetails(t *testing.T) {
	base := domain.New(domain.ErrCodeToolFailure, "boom")
	got := withStderrTail(base, runResult{ExitCode: 3, Stderr: "the tail"})
	assert.Equal(t, 3, got.Details["exitCode"])
	assert.Equal(t, "the tail", got.Details["stderr"])
}

func TestRingBuffer_keepsOnlyTail(t *testing.T) {
	rb := newRingBuffer(5)
	_, _ = rb.Write([]byte("hello"))
	_, _ = rb.Write([]byte("world"))
	assert.Equal(t, "world", rb.String())
}
