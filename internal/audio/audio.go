// Package audio implements the ffprobe/ffmpeg pipeline that runs before a
// job ever reaches whisper.cpp: Probe extracts the source's duration,
// sample rate, channel count and codec (and rejects anything whisper.cpp
// could not usefully transcribe), and Normalize converts the source into
// the 16 kHz mono 16-bit PCM WAV whisper.cpp requires.
//
// Both operations shell out to a subprocess exactly the way BLUEPRINT's
// internal/downloader/ytdlp shells out to yt-dlp: a bounded stderr tail is
// captured for diagnostics, the child (and its whole process group) is
// killed the instant ctx is done, and every failure is classified into a
// *domain.Error before it leaves this package.
package audio

import "time"

// defaultMaxDuration and defaultTimeout are used when the corresponding
// Config field is zero, mirroring BLUEPRINT's ytdlp.New defaulting
// pattern. Real values always come from internal/config in production; the
// zero value only shows up in a test that builds a Config by hand.
const (
	defaultMaxDuration = 4 * time.Hour
	defaultTimeout     = 10 * time.Minute
)

// DefaultFFmpegBin and DefaultFFprobeBin are the executable names used when
// the corresponding Config field is empty, resolved via the process's
// PATH.
const (
	DefaultFFmpegBin  = "ffmpeg"
	DefaultFFprobeBin = "ffprobe"
)

// Config configures a Processor.
type Config struct {
	// FFmpegBin is the ffmpeg executable: an absolute path, or a name
	// resolved via PATH. Defaults to DefaultFFmpegBin.
	FFmpegBin string
	// FFprobeBin is the ffprobe executable: an absolute path, or a name
	// resolved via PATH. Defaults to DefaultFFprobeBin.
	FFprobeBin string
	// MaxDuration rejects any probed audio longer than this, with
	// domain.ErrCodePayloadTooLarge. Defaults to defaultMaxDuration.
	MaxDuration time.Duration
	// Timeout is the hard ceiling on any single Probe or Normalize call,
	// enforced regardless of what the caller's ctx allows — see command in
	// process.go. Defaults to defaultTimeout.
	Timeout time.Duration
	// MinFreeBytes is Normalize's own free-space floor, re-checked
	// immediately before it runs ffmpeg (see Normalize's doc comment) —
	// independent of, but intended to be configured the same as,
	// internal/config's Storage.MinFreeBytes. Zero disables the check.
	MinFreeBytes uint64
}

// Processor implements the ffprobe/ffmpeg pipeline. It is stateless and
// safe for concurrent use: every method starts its own subprocess.
type Processor struct {
	cfg Config
}

// New constructs a Processor from cfg, filling in defaults for any zero
// fields.
func New(cfg Config) *Processor {
	if cfg.FFmpegBin == "" {
		cfg.FFmpegBin = DefaultFFmpegBin
	}
	if cfg.FFprobeBin == "" {
		cfg.FFprobeBin = DefaultFFprobeBin
	}
	if cfg.MaxDuration <= 0 {
		cfg.MaxDuration = defaultMaxDuration
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = defaultTimeout
	}
	return &Processor{cfg: cfg}
}
