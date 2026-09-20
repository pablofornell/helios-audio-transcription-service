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

const sampleProbeJSON = `{
  "streams": [
    {
      "index": 0,
      "codec_name": "pcm_s16le",
      "codec_type": "audio",
      "sample_rate": "44100",
      "channels": 2,
      "duration": "2.000000"
    }
  ],
  "format": {
    "filename": "test.wav",
    "duration": "2.000000",
    "size": "352878"
  }
}`

func TestParseProbeJSON_fillsAudioFromFirstAudioStream(t *testing.T) {
	got, err := parseProbeJSON([]byte(sampleProbeJSON), time.Hour)
	require.NoError(t, err)
	assert.Equal(t, 2.0, got.DurationSeconds)
	assert.Equal(t, 44100, got.SampleRateHz)
	assert.Equal(t, 2, got.Channels)
	assert.Equal(t, "pcm_s16le", got.Codec)
}

func TestParseProbeJSON_skipsLeadingVideoStream(t *testing.T) {
	data := `{
  "streams": [
    {"index": 0, "codec_name": "h264", "codec_type": "video"},
    {"index": 1, "codec_name": "aac", "codec_type": "audio", "sample_rate": "48000", "channels": 1, "duration": "5.5"}
  ],
  "format": {"duration": "5.5"}
}`
	got, err := parseProbeJSON([]byte(data), time.Hour)
	require.NoError(t, err)
	assert.Equal(t, "aac", got.Codec)
	assert.Equal(t, 48000, got.SampleRateHz)
	assert.Equal(t, 1, got.Channels)
}

func TestParseProbeJSON_noAudioStream(t *testing.T) {
	data := `{"streams": [{"index": 0, "codec_name": "h264", "codec_type": "video"}], "format": {"duration": "2.0"}}`
	_, err := parseProbeJSON([]byte(data), time.Hour)
	require.Error(t, err)
	assert.Equal(t, domain.ErrCodeNoAudioStream, domain.CodeOf(err))
}

func TestParseProbeJSON_emptyStreams(t *testing.T) {
	data := `{"streams": [], "format": {}}`
	_, err := parseProbeJSON([]byte(data), time.Hour)
	require.Error(t, err)
	assert.Equal(t, domain.ErrCodeNoAudioStream, domain.CodeOf(err))
}

func TestParseProbeJSON_durationOnStreamOnly(t *testing.T) {
	data := `{"streams": [{"codec_type": "audio", "codec_name": "flac", "sample_rate": "22050", "channels": 1, "duration": "12.5"}], "format": {}}`
	got, err := parseProbeJSON([]byte(data), time.Hour)
	require.NoError(t, err)
	assert.Equal(t, 12.5, got.DurationSeconds)
}

func TestParseProbeJSON_durationOnFormatOnly(t *testing.T) {
	data := `{"streams": [{"codec_type": "audio", "codec_name": "flac", "sample_rate": "22050", "channels": 1}], "format": {"duration": "99.25"}}`
	got, err := parseProbeJSON([]byte(data), time.Hour)
	require.NoError(t, err)
	assert.Equal(t, 99.25, got.DurationSeconds)
}

func TestParseProbeJSON_durationAbsentEverywhere(t *testing.T) {
	data := `{"streams": [{"codec_type": "audio", "codec_name": "flac", "sample_rate": "22050", "channels": 1}], "format": {}}`
	got, err := parseProbeJSON([]byte(data), time.Hour)
	require.NoError(t, err)
	assert.Equal(t, 0.0, got.DurationSeconds)
}

func TestParseProbeJSON_streamDurationPreferredOverFormat(t *testing.T) {
	// A multi-stream container's format duration can legitimately differ
	// from one particular stream's own length; the stream's own duration
	// describes the audio track that will actually be transcribed.
	data := `{"streams": [{"codec_type": "audio", "codec_name": "aac", "sample_rate": "48000", "channels": 2, "duration": "10.0"}], "format": {"duration": "20.0"}}`
	got, err := parseProbeJSON([]byte(data), time.Hour)
	require.NoError(t, err)
	assert.Equal(t, 10.0, got.DurationSeconds)
}

func TestParseProbeJSON_exceedsMaxDuration(t *testing.T) {
	_, err := parseProbeJSON([]byte(sampleProbeJSON), time.Second)
	require.Error(t, err)
	assert.Equal(t, domain.ErrCodePayloadTooLarge, domain.CodeOf(err))
	de := domain.AsError(err)
	assert.Equal(t, 2.0, de.Details["durationSeconds"])
	assert.Equal(t, 1.0, de.Details["maxDurationSeconds"])
}

func TestParseProbeJSON_maxDurationDisabledWhenZero(t *testing.T) {
	got, err := parseProbeJSON([]byte(sampleProbeJSON), 0)
	require.NoError(t, err)
	assert.Equal(t, 2.0, got.DurationSeconds)
}

func TestParseProbeJSON_malformedJSON(t *testing.T) {
	_, err := parseProbeJSON([]byte("not json"), time.Hour)
	require.Error(t, err)
	assert.Equal(t, domain.ErrCodeToolFailure, domain.CodeOf(err))
}

// --- format_name deny list (finding: hostile HLS/playlist upload) ------

func TestParseProbeJSON_deniesHLSPlaylist(t *testing.T) {
	// The exact shape a real ffprobe reports for an .m3u8 pointing at an
	// arbitrary local file: format_name "hls", a self-declared (never
	// decoded) duration far shorter than the referenced file's real one.
	data := `{
  "streams": [{"index": 0, "codec_type": "audio", "codec_name": "pcm_s16le", "sample_rate": "44100", "channels": 2}],
  "format": {"format_name": "hls", "duration": "1.000000"}
}`
	_, err := parseProbeJSON([]byte(data), time.Hour)
	require.Error(t, err)
	assert.Equal(t, domain.ErrCodeUnsupportedMedia, domain.CodeOf(err))
	de := domain.AsError(err)
	assert.Equal(t, "hls", de.Details["format"])
}

func TestParseProbeJSON_deniesEachFormatInDenyList(t *testing.T) {
	for name := range deniedFormats {
		t.Run(name, func(t *testing.T) {
			data := `{"streams": [{"codec_type": "audio", "codec_name": "pcm_s16le", "sample_rate": "8000", "channels": 1}], "format": {"format_name": "` + name + `"}}`
			_, err := parseProbeJSON([]byte(data), time.Hour)
			require.Error(t, err)
			assert.Equal(t, domain.ErrCodeUnsupportedMedia, domain.CodeOf(err))
		})
	}
}

func TestParseProbeJSON_deniesFormatAsAnyElementOfCommaList(t *testing.T) {
	// format_name is a comma-separated list when several demuxers could
	// plausibly match; a denied name buried anywhere in that list must
	// still be caught, not just when it is the whole string.
	data := `{"streams": [{"codec_type": "audio", "codec_name": "pcm_s16le", "sample_rate": "8000", "channels": 1}], "format": {"format_name": "mov,hls,mp4"}}`
	_, err := parseProbeJSON([]byte(data), time.Hour)
	require.Error(t, err)
	assert.Equal(t, domain.ErrCodeUnsupportedMedia, domain.CodeOf(err))
}

// TestParseProbeJSON_ordinaryFormatsPass is the regression check that
// matters as much as the denial itself: the deny list must not catch any
// of ffprobe's real format_name values for ordinary uploads, including the
// multi-element ones (mp4/m4a share one demuxer name, as does
// webm/mkv) — see TestProbe_realFFprobe_ordinaryFormatsAllPass for the
// same check driven by a real ffprobe/ffmpeg run.
func TestParseProbeJSON_ordinaryFormatsPass(t *testing.T) {
	ordinary := []string{
		"mp3", "wav", "flac", "ogg",
		"mov,mp4,m4a,3gp,3g2,mj2",
		"matroska,webm",
	}
	for _, name := range ordinary {
		t.Run(name, func(t *testing.T) {
			data := `{"streams": [{"codec_type": "audio", "codec_name": "pcm_s16le", "sample_rate": "8000", "channels": 1}], "format": {"format_name": "` + name + `"}}`
			_, err := parseProbeJSON([]byte(data), time.Hour)
			assert.NoError(t, err)
		})
	}
}

// --- subprocess-level tests, driven by a stub ffprobe script -----------

func TestProbe_stubExitNonZeroClassifiesAsUnsupportedMedia(t *testing.T) {
	bin := writeScript(t, "#!/usr/bin/env bash\necho 'Invalid data found when processing input' >&2\nexit 1\n")
	p := New(Config{FFprobeBin: bin, Timeout: 5 * time.Second})

	_, err := p.Probe(context.Background(), "/some/path")
	require.Error(t, err)
	assert.Equal(t, domain.ErrCodeUnsupportedMedia, domain.CodeOf(err))
	de := domain.AsError(err)
	assert.Contains(t, de.Details["stderr"], "Invalid data")
}

func TestProbe_stubExitNonZeroDefaultsToolFailure(t *testing.T) {
	bin := writeScript(t, "#!/usr/bin/env bash\necho 'some other failure' >&2\nexit 1\n")
	p := New(Config{FFprobeBin: bin, Timeout: 5 * time.Second})

	_, err := p.Probe(context.Background(), "/some/path")
	require.Error(t, err)
	assert.Equal(t, domain.ErrCodeToolFailure, domain.CodeOf(err))
	assert.True(t, domain.ErrCodeToolFailure.Retryable())
}

func TestProbe_stubTimeout(t *testing.T) {
	bin := writeScript(t, "#!/usr/bin/env bash\nsleep 5\n")
	p := New(Config{FFprobeBin: bin, Timeout: 200 * time.Millisecond})

	start := time.Now()
	_, err := p.Probe(context.Background(), "/some/path")
	elapsed := time.Since(start)

	require.Error(t, err)
	assert.Equal(t, domain.ErrCodeTimeout, domain.CodeOf(err))
	assert.Less(t, elapsed, 4*time.Second)
}

func TestProbe_ctxCanceled(t *testing.T) {
	bin := writeScript(t, "#!/usr/bin/env bash\nsleep 5\n")
	p := New(Config{FFprobeBin: bin, Timeout: 10 * time.Second})

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	_, err := p.Probe(ctx, "/some/path")
	elapsed := time.Since(start)

	require.Error(t, err)
	assert.Equal(t, domain.ErrCodeCanceled, domain.CodeOf(err))
	assert.Less(t, elapsed, 4*time.Second)
}

// TestProbe_stubStdoutOverCapFailsFastNotAtTimeout guards against the
// deadlock io.ReadAll(io.LimitReader(...)) risks: a child that keeps
// writing past maxProbeJSONBytes has nowhere for the excess to go once
// this package stops reading, so without runCaptureStdout killing it
// immediately, cmd.Wait would block alongside it until Config.Timeout
// fires. Timeout here is set far longer than a correct fix should ever
// take, so a regression shows up as this test being slow, not just wrong.
func TestProbe_stubStdoutOverCapFailsFastNotAtTimeout(t *testing.T) {
	bin := writeScript(t, `#!/usr/bin/env bash
head -c 20000000 /dev/zero
sleep 30
`)
	p := New(Config{FFprobeBin: bin, Timeout: 20 * time.Second})

	start := time.Now()
	_, err := p.Probe(context.Background(), "/some/path")
	elapsed := time.Since(start)

	require.Error(t, err)
	assert.Equal(t, domain.ErrCodeUnsupportedMedia, domain.CodeOf(err))
	assert.Less(t, elapsed, 5*time.Second,
		"Probe must kill an over-cap child immediately, not wait out the full Timeout")
}

func TestProbe_stubSuccess(t *testing.T) {
	bin := writeScript(t, "#!/usr/bin/env bash\ncat <<'EOF'\n"+sampleProbeJSON+"\nEOF\nexit 0\n")
	p := New(Config{FFprobeBin: bin, Timeout: 5 * time.Second})

	got, err := p.Probe(context.Background(), "/some/path")
	require.NoError(t, err)
	assert.Equal(t, 2.0, got.DurationSeconds)
	assert.Equal(t, 44100, got.SampleRateHz)
}

// --- real ffprobe integration test, skipped when the binary is absent --

func TestProbe_realFFprobe(t *testing.T) {
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skip("ffprobe not found on PATH")
	}
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not found on PATH")
	}

	wav := generateTestWAV(t)
	p := New(Config{Timeout: 10 * time.Second})

	got, err := p.Probe(context.Background(), wav)
	require.NoError(t, err)
	assert.InDelta(t, 1.0, got.DurationSeconds, 0.2)
	assert.Equal(t, 44100, got.SampleRateHz)
	assert.Equal(t, 1, got.Channels)
	assert.NotEmpty(t, got.Codec)
}

func TestProbe_realFFprobe_noAudioStream(t *testing.T) {
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skip("ffprobe not found on PATH")
	}
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not found on PATH")
	}

	video := generateTestVideoOnly(t)
	p := New(Config{Timeout: 10 * time.Second})

	_, err := p.Probe(context.Background(), video)
	require.Error(t, err)
	assert.Equal(t, domain.ErrCodeNoAudioStream, domain.CodeOf(err))
}

// TestProbe_realFFprobe_refusesHostilePlaylist is the hostile-media
// reproduction of the finding this package exists to close: a tiny
// uploaded .m3u8 playlist whose EXTINF claims a 1-second duration while
// actually pointing at an arbitrary local file (here, a real 5-second WAV
// fixture) must never have that lie believed. Before the format_name deny
// list, ffprobe happily reported format_name "hls" and duration "1.0" for
// this file without ever decoding victim.wav, which would let
// HELIOS_AUDIO_MAX_DURATION be bypassed and let a later Normalize decode
// victim.wav's real, unrelated content into the WAV that gets transcribed
// and served back to the uploader.
func TestProbe_realFFprobe_refusesHostilePlaylist(t *testing.T) {
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skip("ffprobe not found on PATH")
	}
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not found on PATH")
	}

	dir := t.TempDir()
	victim := filepath.Join(dir, "victim.wav")
	cmd := exec.Command("ffmpeg", "-y", "-f", "lavfi", "-i", "sine=frequency=440:duration=5",
		"-ar", "44100", "-ac", "1", "-loglevel", "error", victim)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "ffmpeg fixture generation failed: %s", out)

	playlist := filepath.Join(dir, "evil.m3u8")
	body := "#EXTM3U\n#EXT-X-TARGETDURATION:1\n#EXTINF:1.0,\n" + victim + "\n#EXT-X-ENDLIST\n"
	require.NoError(t, os.WriteFile(playlist, []byte(body), 0o644))

	p := New(Config{Timeout: 10 * time.Second})
	_, err = p.Probe(context.Background(), playlist)
	require.Error(t, err, "a playlist pointing at an arbitrary local file must be refused, not probed as if it were that file")
	assert.Equal(t, domain.ErrCodeUnsupportedMedia, domain.CodeOf(err))
	de := domain.AsError(err)
	assert.Contains(t, de.Details["format"], "hls")
}

// TestProbe_realFFprobe_ordinaryFormatsAllPass is the regression check the
// hostile-playlist fix above demands: every everyday container this
// service is meant to accept must still Probe cleanly through the same
// format_name deny list.
func TestProbe_realFFprobe_ordinaryFormatsAllPass(t *testing.T) {
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skip("ffprobe not found on PATH")
	}
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not found on PATH")
	}

	cases := []struct {
		name string
		args []string
	}{
		{"mp3", []string{"-c:a", "libmp3lame"}},
		{"wav", nil},
		{"flac", nil},
		{"ogg", []string{"-c:a", "libvorbis"}},
		{"m4a", []string{"-c:a", "aac"}},
		{"mp4", []string{"-c:a", "aac"}},
		{"webm", []string{"-ar", "48000", "-c:a", "libopus"}},
		{"mkv", []string{"-c:a", "aac"}},
	}
	p := New(Config{Timeout: 10 * time.Second})
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "clip."+tc.name)
			args := append([]string{"-y", "-f", "lavfi", "-i", "sine=frequency=440:duration=1",
				"-ar", "44100", "-ac", "1"}, tc.args...)
			args = append(args, "-loglevel", "error", path)
			out, err := exec.Command("ffmpeg", args...).CombinedOutput()
			require.NoError(t, err, "ffmpeg fixture generation for %s failed: %s", tc.name, out)

			got, err := p.Probe(context.Background(), path)
			require.NoError(t, err, "an ordinary %s upload must not be rejected by the playlist deny list", tc.name)
			assert.InDelta(t, 1.0, got.DurationSeconds, 0.25)
		})
	}
}
