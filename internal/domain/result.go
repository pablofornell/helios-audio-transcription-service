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
	// Language is the detected (Options.AutoDetectLanguage true) or forced
	// ISO-639-1 code whisper-cli actually used.
	Language        string  `json:"language"`
	DurationSeconds float64 `json:"durationSeconds"`
	// Model is the whisper model name actually used, resolved from
	// Options.Model or the configured default.
	Model string `json:"model"`
	// Task is transcribe or translate, echoed from the request that
	// produced this Result.
	Task     Task      `json:"task"`
	Segments []Segment `json:"segments"`
}
