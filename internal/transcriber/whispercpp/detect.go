package whispercpp

import (
	"context"
	"regexp"

	"github.com/pablofornell/helios-audio-transcription-service/internal/domain"
)

// detectedLanguageRE matches the log line whisper.cpp prints when it
// detects the language. Real output of whisper-cli v1.9.4 with -dl:
//
//	whisper_full_with_state: auto-detected language: en (p = 0.976673)
//
// See testdata/detect_language_jfk_tiny.stderr. The code is the first
// field after "language:".
var detectedLanguageRE = regexp.MustCompile(`auto-detected language:\s+(\S+)`)

// languageCodeRE is the shape of a code Transcribe accepts from detection:
// 2 or 3 lowercase letters, like domain.Options.Language.
var languageCodeRE = regexp.MustCompile(`^[a-z]{2,3}$`)

// maxScanLine bounds the part of one stderr line the scanner keeps.
const maxScanLine = 4 * 1024

// languageScanner is an io.Writer that finds the "auto-detected language"
// line in a stream of stderr bytes. The 2 KiB stderr tail in runResult is
// too small for this job: whisper.cpp prints the line in the middle of its
// log, and timing lines follow it.
type languageScanner struct {
	line  []byte
	found bool
	code  string
}

func (s *languageScanner) Write(p []byte) (int, error) {
	for _, b := range p {
		if b == '\n' {
			s.endLine()
			continue
		}
		if len(s.line) < maxScanLine {
			s.line = append(s.line, b)
		}
	}
	return len(p), nil
}

// endLine checks the finished line. The first match wins.
func (s *languageScanner) endLine() {
	if !s.found {
		if m := detectedLanguageRE.FindSubmatch(s.line); m != nil {
			s.found = true
			s.code = string(m[1])
		}
	}
	s.line = s.line[:0]
}

// result returns the detected code, or "" when stderr had no such line.
func (s *languageScanner) result() string {
	s.endLine() // the last line may have no newline
	return s.code
}

// detectLanguage runs whisper-cli with -dl on the audio file of req and
// returns the language code it reports. The run uses the same WAV as the
// transcription run and honours ctx, so a cancel or a timeout kills the
// process group. It writes no file.
//
// It never guesses. A non-zero exit gets the same error code as a failed
// transcription run (classifyRun). A zero exit with no usable code is a
// tool_failure, which is retryable like any other tool failure.
func (t *Transcriber) detectLanguage(ctx context.Context, req domain.TranscribeRequest, modelPath string) (string, error) {
	scan := &languageScanner{}
	res := t.runTee(ctx, buildDetectArgs(t.cfg, req.AudioPath, modelPath), scan)
	if res.Err != nil || res.ExitCode != 0 {
		code := classifyRun(res)
		return "", withStderrTail(domain.Wrap(code, "whispercpp: whisper-cli language detection failed", res.Err), res)
	}

	lang := scan.result()
	if !languageCodeRE.MatchString(lang) {
		return "", withStderrTail(domain.New(domain.ErrCodeToolFailure,
			"whispercpp: whisper-cli did not report a usable language code"), res).
			WithDetails("detectedLanguage", lang)
	}
	return lang, nil
}
