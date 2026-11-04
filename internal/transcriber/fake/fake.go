// Package fake implements the Transcriber shape (see internal/transcriber's
// package doc) deterministically: no subprocess, no model file, no I/O
// beyond reading req.AudioPath's basename. It exists for internal/app's own
// tests (which must never exec a real whisper-cli) and for local
// development/end-to-end runs under HELIOS_TRANSCRIBER_KIND=fake — mirror
// BLUEPRINT's internal/downloader/fake.
package fake

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/pablofornell/helios-audio-transcription-service/internal/domain"
)

// segmentSpan is the fixed length, in seconds, of each of the two fake
// segments Transcribe always produces.
const segmentSpan = 2.0

// Config configures a Transcriber. Every field is optional; the zero value
// is a Transcriber that succeeds immediately with a deterministic
// two-segment result.
type Config struct {
	// FailWith, when non-empty, makes Transcribe fail with a *domain.Error
	// of this code instead of succeeding.
	FailWith domain.ErrorCode
	// FailMessage overrides the default message used when FailWith is set.
	FailMessage string
	// Delay, if positive, is waited (respecting ctx cancellation) before
	// Transcribe proceeds to success or failure — gives a test something to
	// race a cancellation against.
	Delay time.Duration
	// DetectedLanguage is the language the fake "detects" in every audio
	// file. Empty means "en". A test sets it to simulate Spanish audio
	// ("es"), audio in a third language ("fr") and so on. The request's
	// Options.Language hint has no effect, as in the real engine.
	DetectedLanguage string
}

// Transcriber implements the Transcriber shape driven entirely by Config.
type Transcriber struct {
	cfg Config
}

// New constructs a Transcriber from cfg.
func New(cfg Config) *Transcriber {
	return &Transcriber{cfg: cfg}
}

// Available implements the Transcriber shape's Available method. The fake
// has no binary or model directory to check, so it always reports ready.
func (t *Transcriber) Available(ctx context.Context) error {
	return nil
}

// Transcribe implements the Transcriber shape's Transcribe method: a
// stable, two-segment result derived from req.AudioPath's basename, so
// distinct jobs produce distinguishable (but always reproducible) text
// without ever touching whisper.cpp. It honours Options.WordTimestamps
// (per-word timings on each segment, or none at all). It follows the same
// language rule as the real engine (domain.PlanOutput): the language of the
// audio is Config.DetectedLanguage ("en" when empty), Spanish and English
// audio keep their language, and audio in any other language is
// "translated": the text gets a prefix, matching the shape of what a real
// translation would look like without pretending to translate anything.
// Neither Options.Language nor Options.Task plays any part.
func (t *Transcriber) Transcribe(ctx context.Context, req domain.TranscribeRequest) (*domain.Result, error) {
	if err := waitOrCancel(ctx, t.cfg.Delay); err != nil {
		return nil, domain.Wrap(classifyCtxErr(err), "fake: transcribe canceled during delay", err)
	}
	if t.cfg.FailWith != "" {
		return nil, t.failErr()
	}

	base := basenameWithoutExt(req.AudioPath)
	lang := t.cfg.DetectedLanguage
	if lang == "" {
		lang = domain.LanguageEnglish
	}
	plan := domain.PlanOutput(lang)

	text1 := fmt.Sprintf("This is the first fake segment for %s.", base)
	text2 := fmt.Sprintf("This is the second fake segment for %s.", base)
	if plan.Translate {
		text1 = "Translated: " + text1
		text2 = "Translated: " + text2
	}

	seg1 := domain.Segment{ID: 0, Start: 0, End: segmentSpan, Text: text1}
	seg2 := domain.Segment{ID: 1, Start: segmentSpan, End: 2 * segmentSpan, Text: text2}
	if req.Options.WordTimestamps {
		seg1.Words = wordsFor(text1, seg1.Start, seg1.End)
		seg2.Words = wordsFor(text2, seg2.Start, seg2.End)
	}

	return &domain.Result{
		Text:            text1 + " " + text2,
		Language:        plan.SourceLanguage,
		OutputLanguage:  plan.OutputLanguage,
		DurationSeconds: 2 * segmentSpan,
		Model:           req.Options.Model,
		Task:            plan.Task(),
		Segments:        []domain.Segment{seg1, seg2},
	}, nil
}

// basenameWithoutExt strips both the directory and the extension from
// path, e.g. "/data/transcriptions/abc/audio.wav" -> "audio". Used only to
// derive text that visibly varies with the request, not as any kind of
// real identifier.
func basenameWithoutExt(path string) string {
	base := filepath.Base(path)
	return strings.TrimSuffix(base, filepath.Ext(base))
}

// wordsFor splits text on whitespace and spreads the resulting words
// evenly across [start, end), deterministically — the same text and span
// always produce the same words.
func wordsFor(text string, start, end float64) []domain.Word {
	parts := strings.Fields(text)
	n := len(parts)
	if n == 0 {
		return nil
	}
	step := (end - start) / float64(n)
	words := make([]domain.Word, n)
	for i, w := range parts {
		words[i] = domain.Word{
			Word:        w,
			Start:       start + step*float64(i),
			End:         start + step*float64(i+1),
			Probability: 0.99,
		}
	}
	return words
}

// failErr builds the *domain.Error Config.FailWith describes.
func (t *Transcriber) failErr() error {
	msg := t.cfg.FailMessage
	if msg == "" {
		msg = fmt.Sprintf("fake: forced failure (%s)", t.cfg.FailWith)
	}
	return domain.New(t.cfg.FailWith, msg)
}

// waitOrCancel blocks for dur, or until ctx is done, whichever comes
// first. dur<=0 is a non-blocking check of ctx alone.
func waitOrCancel(ctx context.Context, dur time.Duration) error {
	if dur <= 0 {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
			return nil
		}
	}
	timer := time.NewTimer(dur)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// classifyCtxErr maps a context error to the domain.ErrorCode Transcribe
// reports it as.
func classifyCtxErr(err error) domain.ErrorCode {
	if errors.Is(err, context.DeadlineExceeded) {
		return domain.ErrCodeTimeout
	}
	return domain.ErrCodeCanceled
}
