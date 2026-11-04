package domain

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// defaultBeamSize is whisper.cpp's own default beam width, used both by
// DefaultOptions and by Normalize to fill in a caller-omitted BeamSize.
const defaultBeamSize = 5

// maxInitialPromptBytes bounds Options.InitialPrompt: whisper.cpp feeds it
// straight to the decoder as context, and an unbounded prompt is both a
// resource-exhaustion vector and unlikely to help transcription quality
// past a modest length.
const maxInitialPromptBytes = 1024

// modelNamePattern constrains the shape of a non-empty Model, unconditionally
// -- regardless of whether allowedModels is empty. allowedModels is supposed
// to always be non-empty in production (config.Config.Validate enforces
// that), but Validate itself cannot see that guarantee and is called
// directly in tests and by any future caller, and an empty allowedModels
// used to make the allowlist check below a silent no-op. Without this
// pattern, an unvalidated Model reaches
// filepath.Join(ModelDir, "ggml-"+model+".bin") in whispercpp.modelPath,
// where a value like "../../../etc/passwd" escapes ModelDir entirely (a
// file-existence oracle at best, an attempt to mmap arbitrary files as
// model weights at worst). Excluding "/" (and everything else outside
// this charset) makes that escape impossible independent of any allowlist.
var modelNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)

// Options are the caller-supplied knobs for one transcription. Zero values
// for BeamSize (and, via Model/Language trimming, whitespace-only strings)
// are filled in by Normalize before Validate is called — see Normalize.
type Options struct {
	// Model is the whisper model name, "large-v3-turbo-q5_0". Empty means
	// the configured default model.
	Model string `json:"model"`
	// Language is accepted for compatibility and ignored. The service
	// always detects the language spoken in the audio before it
	// transcribes; the detection alone sets the output language (see
	// PlanOutput). A wrong hint used to give a wrong transcript.
	Language string `json:"language"`
	// Task is accepted for compatibility and ignored. The output language
	// follows PlanOutput. See Task.
	Task Task `json:"task"`
	// WordTimestamps, when true, asks whisper-cli to emit per-word timings
	// (its -ojf mode) instead of just per-segment ones.
	WordTimestamps bool `json:"wordTimestamps"`
	// InitialPrompt is a context hint fed to the decoder, e.g. proper nouns
	// or jargon likely to appear. Max 1024 bytes.
	InitialPrompt string `json:"initialPrompt"`
	// BeamSize is whisper.cpp's beam search width, 1..16. Default 5.
	BeamSize int `json:"beamSize"`
	// Temperature is the decoder's sampling temperature, 0.0..1.0. Default
	// 0.0 (deterministic).
	Temperature float64 `json:"temperature"`
}

// DefaultOptions returns the Options a submission gets when the caller
// supplies none at all: transcribe (not translate) at whisper.cpp's own
// default beam size, with everything else left for the configured default
// model and auto-detected language.
func DefaultOptions() Options {
	return Options{
		Task:     TaskTranscribe,
		BeamSize: defaultBeamSize,
	}
}

// Normalize returns a copy of o with caller-facing sloppiness cleaned up
// before Validate ever sees it: Model and Language are trimmed and
// lowercased, a Language of "auto" collapses to "" (auto-detect — see
// AutoDetectLanguage), and a zero BeamSize is filled with whisper.cpp's own
// default. An empty Task likewise becomes TaskTranscribe: omitting "task"
// from the submitted JSON is the overwhelmingly common case and plainly
// means "just transcribe it", so Normalize must not leave it to fail
// Validate. Normalize is total -- Normalize() on a zero Options yields a
// valid request -- which is what lets the HTTP layer decode caller JSON
// into a bare struct without first seeding it from DefaultOptions().
func (o Options) Normalize() Options {
	o.Model = strings.ToLower(strings.TrimSpace(o.Model))
	o.Language = strings.ToLower(strings.TrimSpace(o.Language))
	if o.Language == "auto" {
		o.Language = ""
	}
	if o.Task == "" {
		o.Task = TaskTranscribe
	}
	if o.BeamSize == 0 {
		o.BeamSize = defaultBeamSize
	}
	return o
}

// Validate reports whether o describes a coherent, executable request. It
// is invalid input, not a server error, when it fails: the returned error
// is always an *Error with Code ErrCodeInvalidInput and a Details["field"]
// entry naming the offending field, ready to surface to an API caller. A
// non-empty Model is always checked against modelNamePattern, regardless of
// allowedModels; allowedModels, when non-empty, additionally restricts
// Model to that list, but an empty allowedModels skips only that
// list-membership check (e.g. before configuration is known), never the
// shape check. An empty Model always passes: it means "the configured
// default model", which app.Service resolves after validation, and which
// Validate has no business rejecting since it cannot see that default.
func (o Options) Validate(allowedModels []string) error {
	if !o.Task.Valid() {
		return New(ErrCodeInvalidInput, fmt.Sprintf("invalid task %q", o.Task)).
			WithDetails("field", "task")
	}
	if o.BeamSize < 1 || o.BeamSize > 16 {
		return New(ErrCodeInvalidInput, fmt.Sprintf("beam size must be between 1 and 16, got %d", o.BeamSize)).
			WithDetails("field", "beamSize")
	}
	if o.Temperature < 0 || o.Temperature > 1 {
		return New(ErrCodeInvalidInput, fmt.Sprintf("temperature must be between 0.0 and 1.0, got %g", o.Temperature)).
			WithDetails("field", "temperature")
	}
	if len(o.InitialPrompt) > maxInitialPromptBytes {
		return New(ErrCodeInvalidInput, fmt.Sprintf("initial prompt must be at most %d bytes, got %d", maxInitialPromptBytes, len(o.InitialPrompt))).
			WithDetails("field", "initialPrompt")
	}
	if o.Language != "" && !validLanguageCode(o.Language) {
		return New(ErrCodeInvalidInput, fmt.Sprintf("language must be empty or a 2-3 letter lowercase code, got %q", o.Language)).
			WithDetails("field", "language")
	}
	if o.Model != "" {
		if !modelNamePattern.MatchString(o.Model) {
			return New(ErrCodeInvalidInput, fmt.Sprintf("model %q has an invalid shape", o.Model)).
				WithDetails("field", "model")
		}
		if len(allowedModels) > 0 && !slices.Contains(allowedModels, o.Model) {
			return New(ErrCodeInvalidInput, fmt.Sprintf("model %q is not one of the allowed models", o.Model)).
				WithDetails("field", "model")
		}
	}
	return nil
}

// AutoDetectLanguage reports whether the request carries no language hint.
// True whenever Language is empty — which, after Normalize, is also what a
// caller-supplied "auto" becomes. It does not change what the service does:
// the service ignores the hint and always detects the language.
func (o Options) AutoDetectLanguage() bool {
	return o.Language == ""
}

// validLanguageCode reports whether lang is 2 or 3 lowercase ASCII letters,
// the shape of an ISO-639-1/639-2 code. It does not check against the list
// of codes whisper.cpp actually supports — an unsupported-but-well-formed
// code is whisper-cli's ErrCodeToolFailure to report, not domain's to
// pre-reject.
func validLanguageCode(lang string) bool {
	if len(lang) < 2 || len(lang) > 3 {
		return false
	}
	for _, r := range lang {
		if r < 'a' || r > 'z' {
			return false
		}
	}
	return true
}
