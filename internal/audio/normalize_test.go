package audio

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pablofornell/helios-audio-transcription-service/internal/domain"
)

func TestNormalize_stubSuccessRenamesIntoPlace(t *testing.T) {
	dstDir := t.TempDir()
	dst := filepath.Join(dstDir, "audio.wav")
	// The stub ffmpeg writes to whatever path it was given as its last
	// argument (normalizeArgs always puts outPath last).
	bin := writeScript(t, `#!/usr/bin/env bash
out="${@: -1}"
printf 'fake wav bytes' > "$out"
exit 0
`)
	p := New(Config{FFmpegBin: bin, Timeout: 5 * time.Second})

	err := p.Normalize(context.Background(), "/some/src.mp3", dst)
	require.NoError(t, err)

	content, err := os.ReadFile(dst)
	require.NoError(t, err)
	assert.Equal(t, "fake wav bytes", string(content))

	// No stray temp file left behind in the destination directory.
	entries, err := os.ReadDir(dstDir)
	require.NoError(t, err)
	assert.Len(t, entries, 1)
}

func TestNormalize_stubFailureLeavesNoFile(t *testing.T) {
	dstDir := t.TempDir()
	dst := filepath.Join(dstDir, "audio.wav")
	bin := writeScript(t, `#!/usr/bin/env bash
out="${@: -1}"
printf 'partial garbage' > "$out"
echo 'Invalid data found when processing input' >&2
exit 1
`)
	p := New(Config{FFmpegBin: bin, Timeout: 5 * time.Second})

	err := p.Normalize(context.Background(), "/some/src.mp3", dst)
	require.Error(t, err)
	assert.Equal(t, domain.ErrCodeUnsupportedMedia, domain.CodeOf(err))

	assert.NoFileExists(t, dst)
	entries, err := os.ReadDir(dstDir)
	require.NoError(t, err)
	assert.Empty(t, entries, "a failed Normalize must leave no half-written file behind")
}

func TestNormalize_stubDefaultToolFailure(t *testing.T) {
	dstDir := t.TempDir()
	dst := filepath.Join(dstDir, "audio.wav")
	bin := writeScript(t, "#!/usr/bin/env bash\necho 'boom' >&2\nexit 1\n")
	p := New(Config{FFmpegBin: bin, Timeout: 5 * time.Second})

	err := p.Normalize(context.Background(), "/some/src.mp3", dst)
	require.Error(t, err)
	assert.Equal(t, domain.ErrCodeToolFailure, domain.CodeOf(err))
}

func TestNormalize_stubTimeoutLeavesNoFile(t *testing.T) {
	dstDir := t.TempDir()
	dst := filepath.Join(dstDir, "audio.wav")
	bin := writeScript(t, `#!/usr/bin/env bash
out="${@: -1}"
printf 'partial' > "$out"
sleep 5
`)
	p := New(Config{FFmpegBin: bin, Timeout: 200 * time.Millisecond})

	start := time.Now()
	err := p.Normalize(context.Background(), "/some/src.mp3", dst)
	elapsed := time.Since(start)

	require.Error(t, err)
	assert.Equal(t, domain.ErrCodeTimeout, domain.CodeOf(err))
	assert.Less(t, elapsed, 4*time.Second)
	assert.NoFileExists(t, dst)
	entries, err := os.ReadDir(dstDir)
	require.NoError(t, err)
	assert.Empty(t, entries)
}

func TestNormalize_ctxCanceledKillsProcessGroup(t *testing.T) {
	childPIDFile := filepath.Join(t.TempDir(), "child.pid")
	dstDir := t.TempDir()
	dst := filepath.Join(dstDir, "audio.wav")
	bin := writeScript(t, `#!/usr/bin/env bash
sleep 100 &
echo "$!" > `+childPIDFile+`
sleep 5
`)
	p := New(Config{FFmpegBin: bin, Timeout: 10 * time.Second})

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(200 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	err := p.Normalize(ctx, "/some/src.mp3", dst)
	elapsed := time.Since(start)

	require.Error(t, err)
	assert.Equal(t, domain.ErrCodeCanceled, domain.CodeOf(err))
	assert.Less(t, elapsed, 4*time.Second)

	deadline := time.Now().Add(2 * time.Second)
	var pidBytes []byte
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(childPIDFile); err == nil {
			pidBytes = b
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	require.NotEmpty(t, pidBytes, "child pid file was never created")
}

// --- real ffmpeg integration test, skipped when the binary is absent ---

func TestNormalize_realFFmpeg(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not found on PATH")
	}

	src := generateTestWAV(t)
	dst := filepath.Join(t.TempDir(), "normalized.wav")
	p := New(Config{Timeout: 10 * time.Second})

	err := p.Normalize(context.Background(), src, dst)
	require.NoError(t, err)
	assert.FileExists(t, dst)

	if _, err := exec.LookPath("ffprobe"); err == nil {
		got, err := p.Probe(context.Background(), dst)
		require.NoError(t, err)
		assert.Equal(t, 1, got.Channels)
		assert.Equal(t, 16000, got.SampleRateHz)
	}
}

// TestNormalize_realFFmpeg_hardCeilingBoundsOutputRegardlessOfSourceLength
// is the amplification half of the hostile-playlist finding: even handed a
// source that is much longer than MaxDuration (standing in for one whose
// self-reported duration lied to Probe), Normalize's own -t/-fs must cap
// the output at MaxDuration's worth of audio -- it must not trust anything
// about the input's claimed length.
func TestNormalize_realFFmpeg_hardCeilingBoundsOutputRegardlessOfSourceLength(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not found on PATH")
	}
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skip("ffprobe not found on PATH")
	}

	src := filepath.Join(t.TempDir(), "long.wav")
	out, err := exec.Command("ffmpeg", "-y", "-f", "lavfi", "-i", "sine=frequency=440:duration=10",
		"-ar", "44100", "-ac", "1", "-loglevel", "error", src).CombinedOutput()
	require.NoError(t, err, "ffmpeg fixture generation failed: %s", out)

	dst := filepath.Join(t.TempDir(), "normalized.wav")
	p := New(Config{Timeout: 10 * time.Second, MaxDuration: 2 * time.Second})

	require.NoError(t, p.Normalize(context.Background(), src, dst))

	got, err := p.Probe(context.Background(), dst)
	require.NoError(t, err)
	assert.LessOrEqual(t, got.DurationSeconds, 2.5,
		"Normalize must not emit more than ~MaxDuration of audio no matter how long the source is")
}

// TestNormalize_stubFreeSpaceFloorRejectsBeforeRunningFfmpeg proves the
// free-space recheck happens before ffmpeg is even started: MinFreeBytes
// is set impossibly high, and the stub ffmpeg would fail this test loudly
// (via the "should never run" stderr) if Normalize invoked it anyway.
func TestNormalize_stubFreeSpaceFloorRejectsBeforeRunningFfmpeg(t *testing.T) {
	dstDir := t.TempDir()
	dst := filepath.Join(dstDir, "audio.wav")
	bin := writeScript(t, "#!/usr/bin/env bash\necho 'should never run' >&2\nexit 1\n")
	p := New(Config{FFmpegBin: bin, Timeout: 5 * time.Second, MinFreeBytes: 1 << 62})

	err := p.Normalize(context.Background(), "/some/src.mp3", dst)
	require.Error(t, err)
	assert.Equal(t, domain.ErrCodeDiskFull, domain.CodeOf(err))
	assert.NoFileExists(t, dst)
}

// TestNormalize_stubFreeSpaceFloorDisabledWhenZero is the regression check
// for the above: MinFreeBytes' zero value (what every other test in this
// package, and any Processor built without explicitly setting it, gets)
// must not reject a normal Normalize call.
func TestNormalize_stubFreeSpaceFloorDisabledWhenZero(t *testing.T) {
	dstDir := t.TempDir()
	dst := filepath.Join(dstDir, "audio.wav")
	bin := writeScript(t, `#!/usr/bin/env bash
out="${@: -1}"
printf 'fake wav bytes' > "$out"
exit 0
`)
	p := New(Config{FFmpegBin: bin, Timeout: 5 * time.Second})

	require.NoError(t, p.Normalize(context.Background(), "/some/src.mp3", dst))
	assert.FileExists(t, dst)
}
