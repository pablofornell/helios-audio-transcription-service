package audio

import (
	"context"
	"errors"
	"strings"
	"syscall"

	"github.com/pablofornell/helios-audio-transcription-service/internal/domain"
)

// classifyRules is ffmpeg/ffprobe's stderr wording, as data, so a future
// version that rewords a message is a one-line addition here rather than a
// code change — mirrors BLUEPRINT ytdlp's classify.go. Every row is
// exercised by classify_test.go's table.
//
// Only domain.ErrCodeUnsupportedMedia gets its own patterns: the contract
// draws a hard line between "ffprobe/ffmpeg could not even demux the
// container/codec" (unsupported_media, not retryable — the file will never
// decode on a retry) and everything else, which falls through to the
// default domain.ErrCodeToolFailure (retryable, since ffmpeg/ffprobe
// flakiness under load is otherwise indistinguishable from a real bug).
// domain.ErrCodeNoAudioStream is never produced here: ffprobe exits 0 when
// a container demuxes fine but simply has no audio track, so that
// classification is Probe's own job once it has parsed the stream list,
// not classify's.
var classifyRules = []struct {
	Code     domain.ErrorCode
	Patterns []string
}{
	{
		Code: domain.ErrCodeUnsupportedMedia,
		Patterns: []string{
			"invalid data found when processing input",
			"could not find codec parameters",
			"moov atom not found",
			"unsupported codec",
			"unknown format",
			"invalid nal unit size",
			"could not find corresponding trex",
		},
	},
}

// classify maps one failed ffmpeg/ffprobe invocation to the domain.ErrorCode
// that best explains it. err is the run's own error (see runResult.Err);
// exitCode and stderr are the process's exit status and captured stderr
// tail.
func classify(stderr string, exitCode int, err error) domain.ErrorCode {
	_ = exitCode

	if err != nil {
		switch {
		case errors.Is(err, context.DeadlineExceeded):
			return domain.ErrCodeTimeout
		case errors.Is(err, context.Canceled):
			return domain.ErrCodeCanceled
		case errors.Is(err, syscall.ENOSPC):
			return domain.ErrCodeDiskFull
		}
	}

	lower := strings.ToLower(stderr)
	if strings.Contains(lower, "no space left on device") {
		return domain.ErrCodeDiskFull
	}

	for _, rule := range classifyRules {
		for _, pattern := range rule.Patterns {
			if strings.Contains(lower, pattern) {
				return rule.Code
			}
		}
	}

	return domain.ErrCodeToolFailure
}

// classifyLocalErr wraps a failure from this package's own filesystem
// operations (not ffmpeg/ffprobe's own exit) using the same classify table
// a subprocess I/O failure deserves — most importantly ENOSPC, which can
// just as easily happen creating a temp file as it can happen inside
// ffmpeg itself.
func classifyLocalErr(err error) domain.ErrorCode {
	code := classify("", -1, err)
	if code == domain.ErrCodeToolFailure {
		// A local os.CreateTemp/os.Rename failure that classify did not
		// recognise as timeout/canceled/disk-full is this package's own bug
		// or environment problem, not "the tool failed" — ffmpeg was never
		// even invoked for this operation.
		code = domain.ErrCodeInternal
	}
	return code
}

// withStderrTail attaches res's exit code and captured stderr tail to err's
// Details, so a human (or an API client) reading the resulting
// domain.Error sees why ffmpeg/ffprobe failed, not just that it did. err
// may be nil, in which case withStderrTail returns nil, unchanged.
func withStderrTail(err *domain.Error, res runResult) *domain.Error {
	if err == nil {
		return nil
	}
	err = err.WithDetails("exitCode", res.ExitCode)
	if res.Stderr != "" {
		err = err.WithDetails("stderr", res.Stderr)
	}
	return err
}
