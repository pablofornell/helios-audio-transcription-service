package whispercpp

import (
	"context"
	"errors"
	"strings"
	"syscall"

	"github.com/pablofornell/helios-audio-transcription-service/internal/domain"
)

// classify maps one failed whisper-cli invocation to the domain.ErrorCode
// that best explains it. Unlike BLUEPRINT ytdlp's classify (which has a
// rich, documented vocabulary of yt-dlp stderr phrasings to key off), there
// is no such catalogue for whisper-cli's own failure wording: the contract
// calls only for a non-zero exit to become domain.ErrCodeToolFailure
// (retryable — the model, the WAV and the machine are all still
// candidates, and whisper-cli flaking under load is plausible), leaving
// only the context-derived cases as more specific classifications.
func classify(exitCode int, err error) domain.ErrorCode {
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
	return domain.ErrCodeToolFailure
}

// classifyRun is classify plus the one stderr-text case worth checking for:
// an out-of-space condition that surfaced as whisper-cli's own C++ I/O
// layer reporting the OS error as text (writing the output JSON, say)
// rather than the process being killed by a signal, in which case
// classify's errors.Is(err, syscall.ENOSPC) check never sees it.
func classifyRun(res runResult) domain.ErrorCode {
	if stderrContains(res, "no space left on device") {
		return domain.ErrCodeDiskFull
	}
	return classify(res.ExitCode, res.Err)
}

// withStderrTail attaches res's exit code and captured stderr tail to err's
// Details, so a human (or an API client) reading the resulting
// domain.Error sees why whisper-cli failed, not just that it did. err may
// be nil, in which case withStderrTail returns nil, unchanged.
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

// stderrContains reports whether res's captured stderr mentions s,
// case-insensitively — a small helper kept separate from classify (which
// deliberately does not pattern-match on wording, see its own doc comment)
// for the one case that does still need it: distinguishing a genuine
// out-of-space failure from an ordinary tool failure when it surfaces as
// stderr text rather than a wrapped syscall.ENOSPC (e.g. whisper-cli's own
// C++ I/O layer reporting the OS error itself rather than the process
// exiting on a signal).
func stderrContains(res runResult, s string) bool {
	return strings.Contains(strings.ToLower(res.Stderr), s)
}
