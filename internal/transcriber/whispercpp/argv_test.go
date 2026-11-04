package whispercpp

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pablofornell/helios-audio-transcription-service/internal/domain"
)

func baseReq() domain.TranscribeRequest {
	return domain.TranscribeRequest{
		AudioPath: "/data/transcriptions/abc/audio.wav",
		Options:   domain.DefaultOptions().Normalize(),
	}
}

func indexOf(args []string, s string) int {
	for i, a := range args {
		if a == s {
			return i
		}
	}
	return -1
}

func TestBuildArgs_fixedFlags(t *testing.T) {
	req := baseReq()
	args := buildArgs(Config{Threads: 4}, req, "en", false, "/models/ggml-large-v3-turbo-q5_0.bin", "/tmp/out/transcript")

	assert.Equal(t, "/models/ggml-large-v3-turbo-q5_0.bin", args[indexOf(args, "-m")+1])
	assert.Equal(t, req.AudioPath, args[indexOf(args, "-f")+1])
	assert.Equal(t, "4", args[indexOf(args, "-t")+1])
	assert.Equal(t, "/tmp/out/transcript", args[indexOf(args, "-of")+1])
	assert.Contains(t, args, "--no-prints")
	assert.Equal(t, "--no-prints", args[len(args)-1], "--no-prints must be last so nothing after it can be mistaken for its argument")
}

func TestBuildArgs_languageIsAlwaysConcrete(t *testing.T) {
	args := buildArgs(Config{Threads: 4}, baseReq(), "fr", true, "m", "o")
	assert.Equal(t, "fr", args[indexOf(args, "-l")+1])
	assert.NotContains(t, args, "auto")
}

func TestBuildArgs_GPUSelection(t *testing.T) {
	for _, tt := range []struct {
		name   string
		cfg    Config
		device string
	}{
		{name: "default", cfg: Config{}, device: "0"},
		{name: "selected device", cfg: Config{GPUDevice: 2}, device: "2"},
		{name: "CPU overrides device", cfg: Config{NoGPU: true, GPUDevice: 2}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			args := buildArgs(tt.cfg, baseReq(), "en", false, "m", "o")
			if tt.cfg.NoGPU {
				assert.Contains(t, args, "--no-gpu")
				assert.NotContains(t, args, "--device")
				return
			}
			assert.NotContains(t, args, "--no-gpu")
			i := indexOf(args, "--device")
			require.GreaterOrEqual(t, i, 0)
			require.Less(t, i+1, len(args))
			assert.Equal(t, tt.device, args[i+1])
		})
	}
}

func TestBuildArgs_translateFlagFollowsTranslateArgument(t *testing.T) {
	args := buildArgs(Config{Threads: 4}, baseReq(), "fr", true, "m", "o")
	assert.Contains(t, args, "-tr")
	assert.Equal(t, "fr", args[indexOf(args, "-l")+1])
}

func TestBuildArgs_noTranslateFlagWhenNotTranslating(t *testing.T) {
	args := buildArgs(Config{Threads: 4}, baseReq(), "es", false, "m", "o")
	assert.NotContains(t, args, "-tr")
	assert.Equal(t, "es", args[indexOf(args, "-l")+1])
}

// Options.Task must not reach whisper-cli: the translate argument, which
// comes from domain.PlanOutput, is the only input to -tr.
func TestBuildArgs_ignoresOptionsTask(t *testing.T) {
	req := baseReq()
	req.Options.Task = domain.TaskTranslate
	assert.NotContains(t, buildArgs(Config{Threads: 4}, req, "es", false, "m", "o"), "-tr")

	req.Options.Task = domain.TaskTranscribe
	assert.Contains(t, buildArgs(Config{Threads: 4}, req, "fr", true, "m", "o"), "-tr")
}

func TestBuildDetectArgs(t *testing.T) {
	args := buildDetectArgs(Config{Threads: 6, GPUDevice: 1}, "/data/transcriptions/abc/audio.wav", "/models/ggml-x.bin")
	assert.Equal(t, "/models/ggml-x.bin", args[indexOf(args, "-m")+1])
	assert.Equal(t, "/data/transcriptions/abc/audio.wav", args[indexOf(args, "-f")+1], "detection must read the same audio as transcription")
	assert.Equal(t, "6", args[indexOf(args, "-t")+1])
	assert.Equal(t, "1", args[indexOf(args, "--device")+1])
	assert.Contains(t, args, "-dl")
	// --no-prints would hide the "auto-detected language" log line.
	assert.NotContains(t, args, "--no-prints")
	assert.NotContains(t, args, "-tr")
	assert.NotContains(t, args, "-oj")
	assert.NotContains(t, args, "-of")
}

func TestBuildDetectArgs_CPU(t *testing.T) {
	args := buildDetectArgs(Config{NoGPU: true, GPUDevice: 3}, "a", "m")
	assert.Contains(t, args, "--no-gpu")
	assert.NotContains(t, args, "--device")
}

func TestBuildArgs_wordTimestampsSwitchesJSONFlag(t *testing.T) {
	req := baseReq()
	req.Options.WordTimestamps = true
	args := buildArgs(Config{Threads: 4}, req, "en", false, "m", "o")
	assert.Contains(t, args, "-ojf")
	assert.NotContains(t, args, "-oj")
}

func TestBuildArgs_withoutWordTimestampsUsesPlainJSONFlag(t *testing.T) {
	req := baseReq()
	req.Options.WordTimestamps = false
	args := buildArgs(Config{Threads: 4}, req, "en", false, "m", "o")
	assert.Contains(t, args, "-oj")
	assert.NotContains(t, args, "-ojf")
}

func TestBuildArgs_promptPresentWhenNonEmpty(t *testing.T) {
	req := baseReq()
	req.Options.InitialPrompt = "proper nouns: Xyzzy"
	args := buildArgs(Config{Threads: 4}, req, "en", false, "m", "o")
	require.GreaterOrEqual(t, indexOf(args, "--prompt"), 0)
	assert.Equal(t, "proper nouns: Xyzzy", args[indexOf(args, "--prompt")+1])
}

func TestBuildArgs_promptAbsentWhenEmpty(t *testing.T) {
	req := baseReq()
	req.Options.InitialPrompt = ""
	args := buildArgs(Config{Threads: 4}, req, "en", false, "m", "o")
	assert.NotContains(t, args, "--prompt")
}

func TestBuildArgs_beamSizePresentWhenNonDefault(t *testing.T) {
	req := baseReq()
	req.Options.BeamSize = 8
	args := buildArgs(Config{Threads: 4}, req, "en", false, "m", "o")
	require.GreaterOrEqual(t, indexOf(args, "-bs"), 0)
	assert.Equal(t, "8", args[indexOf(args, "-bs")+1])
}

func TestBuildArgs_beamSizeAbsentWhenDefault(t *testing.T) {
	req := baseReq()
	req.Options.BeamSize = whisperDefaultBeamSize
	args := buildArgs(Config{Threads: 4}, req, "en", false, "m", "o")
	assert.NotContains(t, args, "-bs")
}

func TestBuildArgs_temperaturePresentWhenNonDefault(t *testing.T) {
	req := baseReq()
	req.Options.Temperature = 0.4
	args := buildArgs(Config{Threads: 4}, req, "en", false, "m", "o")
	require.GreaterOrEqual(t, indexOf(args, "-tp"), 0)
	assert.Equal(t, "0.4", args[indexOf(args, "-tp")+1])
}

func TestBuildArgs_temperatureAbsentWhenDefault(t *testing.T) {
	req := baseReq()
	req.Options.Temperature = whisperDefaultTemperature
	args := buildArgs(Config{Threads: 4}, req, "en", false, "m", "o")
	assert.NotContains(t, args, "-tp")
}
