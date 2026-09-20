package e2e

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"time"
)

// fixtureGoodMP3 and fixtureVideoOnlyMP4 are the absolute paths of the
// audio fixtures generateFixtures writes into fixtureDir once, in
// TestMain, for every test in this package to upload. They are real media
// files produced by a real ffmpeg -- this suite exists specifically to
// exercise internal/audio's real ffprobe/ffmpeg pipeline, not a canned
// byte string pretending to be one.
var (
	// fixtureGoodMP3 is a 2-second, 440Hz stereo sine wave: an ordinary,
	// valid upload every happy-path scenario submits.
	fixtureGoodMP3 string
	// fixtureVideoOnlyMP4 is a 2-second silent video with no audio stream
	// at all -- ffprobe demuxes it fine (probe_score 100) but finds zero
	// audio streams, which is exactly what CONTRACT.md's ErrCodeNoAudioStream
	// means ("file decodes but has no audio track"), as opposed to a
	// corrupt file ffprobe cannot even parse (ErrCodeUnsupportedMedia).
	fixtureVideoOnlyMP4 string
)

// fixtureGenTimeout bounds how long any one ffmpeg invocation generating a
// fixture may take -- these are tiny, synthetic clips (lavfi source, no
// real decoding), so a few seconds is already generous.
const fixtureGenTimeout = 30 * time.Second

// generateFixtures shells out to the real ffmpeg on PATH to produce every
// fixture this package's tests upload, writing them into dir (created by
// runTests, removed when the test binary exits). It is called from
// TestMain only when ffmpegAndFFprobeAvailable reported true, so every
// exec.Command here is expected to succeed; a failure is fatal to the
// whole test binary (see runTests), since every test in this package
// depends on these fixtures existing.
func generateFixtures(ctx context.Context, dir string) error {
	fixtureGoodMP3 = filepath.Join(dir, "good.mp3")
	if err := runFFmpeg(ctx,
		"-f", "lavfi", "-i", "sine=frequency=440:duration=2",
		"-ac", "2", "-ar", "44100",
		fixtureGoodMP3,
	); err != nil {
		return fmt.Errorf("generate fixtureGoodMP3: %w", err)
	}

	fixtureVideoOnlyMP4 = filepath.Join(dir, "video-only.mp4")
	if err := runFFmpeg(ctx,
		"-f", "lavfi", "-i", "testsrc=duration=2:size=64x64:rate=5",
		"-an", "-c:v", "mpeg4",
		fixtureVideoOnlyMP4,
	); err != nil {
		return fmt.Errorf("generate fixtureVideoOnlyMP4: %w", err)
	}

	return nil
}

// runFFmpeg runs ffmpeg with args, plus the flags every fixture generation
// call shares: -y to overwrite, -hide_banner/-loglevel error to keep
// ffmpeg's own chatter out of this suite's test output, bounded by
// fixtureGenTimeout.
func runFFmpeg(ctx context.Context, args ...string) error {
	ctx, cancel := context.WithTimeout(ctx, fixtureGenTimeout)
	defer cancel()

	full := append([]string{"-y", "-hide_banner", "-loglevel", "error"}, args...)
	cmd := exec.CommandContext(ctx, "ffmpeg", full...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("ffmpeg %v: %w: %s", full, err, out)
	}
	return nil
}
