package audio

import (
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// generateTestWAV writes a one-second sine-wave WAV under t.TempDir() using
// the real ffmpeg binary, for the handful of tests that exercise this
// package against a real tool rather than a stub script. Callers must
// t.Skip when ffmpeg is not on PATH before calling this.
func generateTestWAV(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "src.wav")
	cmd := exec.Command("ffmpeg", "-y", "-f", "lavfi", "-i", "sine=frequency=440:duration=1",
		"-ar", "44100", "-ac", "1", "-loglevel", "error", path)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "ffmpeg fixture generation failed: %s", out)
	return path
}

// generateTestVideoOnly writes a one-second video-only MP4 (no audio
// stream) under t.TempDir(), for the no-audio-stream Probe test. Callers
// must t.Skip when ffmpeg is not on PATH before calling this.
func generateTestVideoOnly(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "video.mp4")
	cmd := exec.Command("ffmpeg", "-y", "-f", "lavfi", "-i", "color=c=blue:size=64x64:duration=1",
		"-loglevel", "error", path)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "ffmpeg fixture generation failed: %s", out)
	return path
}
