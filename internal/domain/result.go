package domain

// Word is one whisper.cpp token merged into a whole word with its timing
// and confidence, produced only when Options.WordTimestamps was set (see
// internal/transcriber/whispercpp's -ojf handling).
type Word struct {
	Word string `json:"word"`
	// Start and End are seconds from the start of the audio.
	Start float64 `json:"start"`
	End   float64 `json:"end"`
	// Probability is whisper.cpp's confidence, 0..1. 0 when the engine
	// gives none.
	Probability float64 `json:"probability"`
}

// Segment is one contiguous span of transcript text as whisper.cpp
// produced it.
type Segment struct {
	// ID is the segment's 0-based position in Result.Segments.
	ID    int     `json:"id"`
	Start float64 `json:"start"`
	End   float64 `json:"end"`
	Text  string  `json:"text"`
	// Words is populated only when Options.WordTimestamps was set on the
	// request that produced this Result.
	Words []Word `json:"words,omitempty"`
}

// Result is the finished transcript of a succeeded job, persisted as the
// `result` JSONB column and rendered by internal/transcript into
// txt/srt/vtt on demand.
type Result struct {
	// Text is the full transcript: every Segment's Text, space-normalized
	// and joined.
	Text string `json:"text"`
	// Language is the language spoken in the audio: the detected code
	// (Options.AutoDetectLanguage true) or the code the caller gave as a
	// hint.
	Language string `json:"language"`
	// OutputLanguage is the language of Text: "es" for Spanish audio, "en"
	// for all other audio (see PlanOutput). Empty on a Result stored
	// before this field existed.
	OutputLanguage  string  `json:"outputLanguage,omitempty"`
	DurationSeconds float64 `json:"durationSeconds"`
	// Model is the whisper model name actually used to produce Text. It is
	// the translation model when the engine translated.
	Model string `json:"model"`
	// Task is what the engine did: translate when it translated to
	// English, otherwise transcribe. It follows PlanOutput, not the
	// Options.Task of the request.
	Task     Task      `json:"task"`
	Segments []Segment `json:"segments"`
}
