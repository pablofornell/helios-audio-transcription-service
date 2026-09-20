package domain

// Task selects what whisper-cli produces for a job: the source-language
// transcript, or an English translation of it.
type Task string

const (
	// TaskTranscribe produces output in the audio's own (detected or
	// forced) language.
	TaskTranscribe Task = "transcribe"
	// TaskTranslate produces output translated to English, regardless of
	// the source language.
	TaskTranslate Task = "translate"
)

// Valid reports whether t is one of the two defined Task values.
func (t Task) Valid() bool {
	switch t {
	case TaskTranscribe, TaskTranslate:
		return true
	default:
		return false
	}
}
