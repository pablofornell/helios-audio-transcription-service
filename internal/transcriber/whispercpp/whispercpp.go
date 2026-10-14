// Package whispercpp implements the Transcriber shape (see
// internal/transcriber's package doc) by shelling out to whisper.cpp's
// whisper-cli binary. This package owns every detail of that CLI surface —
// flags, JSON shape, special-token spellings — so the rest of Helios never
// has to know whisper-cli exists.
//
// It was written against, and verified live against, the whisper-cli
// --help output and examples/cli/cli.cpp source on the ggml-org/whisper.cpp
// GitHub repository (master branch, checked 2026-09-20): -m/-f/-t,
// -oj/-ojf, -of, -l, -tr, --prompt, -bs, -tp and --no-prints all exist
// exactly as CONTRACT.md section 8 predicted, and output_json's JSON shape
// (a top-level "result".language, a "transcription" array of segments each
// carrying "offsets"/"text" and, under -ojf, a "tokens" array of
// "text"/"offsets"/"p") also matches. The one detail CONTRACT.md got only
// approximately right: whisper.cpp's own special tokens are spelled
// "[_BEG_]", "[_EOT_]", "[_SOT_]", "[_TT_<n>]", "[_LANG_<code>]" etc. —
// never OpenAI Python whisper's "<|...|>" style. isSpecialToken below still
// recognises both spellings (see its own doc comment) since matching one
// extra, never-actually-emitted pattern costs nothing and keeps this
// package correct if a future whisper.cpp release changes its convention
// or a differently-built vocab file introduces such a token.
package whispercpp

import "time"

// DefaultBinary is the executable name used when Config.Binary is empty,
// resolved via the process's PATH.
const DefaultBinary = "whisper-cli"

// defaultThreads and defaultTimeout are used when the corresponding Config
// field is zero or negative, mirroring BLUEPRINT ytdlp.New's defaulting
// pattern. Real values always come from internal/config in production; the
// zero value only shows up in a test that builds a Config by hand.
const (
	defaultThreads = 4
	defaultTimeout = 30 * time.Minute
)

// Config configures a Transcriber.
type Config struct {
	// Binary is the whisper-cli executable: an absolute path, or a name
	// resolved via PATH. Defaults to DefaultBinary.
	Binary string
	// ModelDir holds the ggml-<model>.bin weight files. See modelPath.
	ModelDir string
	// DefaultModel is the model Available checks actually exists on disk
	// (in addition to ModelDir merely being listable): a deploy whose
	// volume has no weight files at all still reports ModelDir readable
	// (an empty directory is readable), which used to let /readyz go
	// green and then fail every job with model_unavailable. Empty skips
	// that check — a Transcriber built without one (e.g. most unit tests
	// in this package) has no default to verify. In production this comes
	// from config.TranscriberConfig.DefaultModel, which Config.Validate
	// already requires to be non-empty.
	DefaultModel string
	// Threads is the -t value passed to whisper-cli. Defaults to
	// defaultThreads.
	Threads int
	// NoGPU passes --no-gpu to force CPU transcription. By default,
	// whisper-cli prioritizes GPU acceleration when its build and device
	// support it, retaining CPU support for remaining operations/fallback.
	NoGPU bool
	// GPUDevice is the --device index when NoGPU is false. Defaults to 0.
	GPUDevice int
	// Timeout is the hard ceiling on any single Transcribe call, enforced
	// regardless of what the caller's ctx allows — see command in
	// process.go. Defaults to defaultTimeout.
	Timeout time.Duration
}

// Transcriber implements the Transcriber shape by invoking whisper-cli as a
// subprocess. It is stateless and safe for concurrent use: every call to
// Transcribe starts its own process.
type Transcriber struct {
	cfg Config
}

// New constructs a Transcriber from cfg, filling in defaults for any zero
// fields.
func New(cfg Config) *Transcriber {
	if cfg.Binary == "" {
		cfg.Binary = DefaultBinary
	}
	if cfg.Threads <= 0 {
		cfg.Threads = defaultThreads
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = defaultTimeout
	}
	return &Transcriber{cfg: cfg}
}
