package domain

import "strings"

// Language codes with a rule of their own. Every other code follows the
// "third language" rule in PlanOutput.
const (
	// LanguageSpanish is the ISO-639-1 code for Spanish.
	LanguageSpanish = "es"
	// LanguageEnglish is the ISO-639-1 code for English.
	LanguageEnglish = "en"
)

// OutputPlan says what language a transcript must have, given the
// language spoken in the audio.
type OutputPlan struct {
	// SourceLanguage is the language spoken in the audio (lowercase code).
	SourceLanguage string
	// OutputLanguage is the language of the transcript text: "es" or "en".
	OutputLanguage string
	// Translate is true when the engine must translate to English. It is
	// never true for Spanish.
	Translate bool
}

// Task returns the Task that matches p.Translate.
func (p OutputPlan) Task() Task {
	if p.Translate {
		return TaskTranslate
	}
	return TaskTranscribe
}

// PlanOutput applies the one language rule of the service:
//
//   - Spanish audio gives a Spanish transcript. Never translate it.
//   - English audio gives an English transcript.
//   - Audio in any other language gives an English transcript, translated.
//
// The rule ignores Options.Task. The caller must pass a real language
// code: PlanOutput does not guess one for an empty source.
func PlanOutput(source string) OutputPlan {
	source = strings.ToLower(strings.TrimSpace(source))
	switch source {
	case LanguageSpanish:
		return OutputPlan{SourceLanguage: source, OutputLanguage: LanguageSpanish}
	case LanguageEnglish:
		return OutputPlan{SourceLanguage: source, OutputLanguage: LanguageEnglish}
	default:
		return OutputPlan{SourceLanguage: source, OutputLanguage: LanguageEnglish, Translate: true}
	}
}
