package domain

// Task names what whisper-cli does for a job: transcribe in the audio
// language, or translate to English.
//
// A caller can still send Options.Task. The service keeps it for
// compatibility and ignores it: PlanOutput alone sets the output language
// from the detected audio language. Result.Task reports what the engine
// did.
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
