package whispercpp

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pablofornell/helios-audio-transcription-service/internal/domain"
)

// writeScript writes body verbatim as an executable script under
// t.TempDir() and returns its absolute path, suitable for Config.Binary —
// mirrors BLUEPRINT ytdlp's writeRawScript. No test in this package
// touches a real whisper-cli binary: every exit-code/timeout/output case
// runs a generated shell script instead, driven entirely by Config
// pointing at it.
func writeScript(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "whisper-cli-stub.sh")
	require.NoError(t, os.WriteFile(path, []byte(body), 0o755))
	return path
}

// newModel writes an (empty, content does not matter — this package never
// reads it) model file at dir/ggml-<name>.bin, standing in for a real
// downloaded model so Transcribe's os.Stat check passes.
func newModel(t *testing.T, dir, name string) {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "ggml-"+name+".bin"), []byte("fake weights"), 0o644))
}

// newAudioPath creates an (empty — whisper-cli itself is stubbed out and
// never actually opens it) WAV file inside a fresh job directory,
// mirroring the real "transcriptions/<id>/audio.wav" layout closely
// enough for filepath.Dir(req.AudioPath) (Transcribe's per-job temp dir
// anchor) to be a real, writable directory containing exactly one other
// entry — the file itself — before Transcribe ever runs.
func newAudioPath(t *testing.T) string {
	t.Helper()
	jobDir := filepath.Join(t.TempDir(), "job")
	require.NoError(t, os.MkdirAll(jobDir, 0o755))
	path := filepath.Join(jobDir, "audio.wav")
	require.NoError(t, os.WriteFile(path, []byte("fake wav bytes"), 0o644))
	return path
}

// detectFixture is the recorded real stderr of whisper-cli v1.9.4 run with
// -dl on samples/jfk.wav and the tiny model (see testdata).
const detectFixture = "testdata/detect_language_jfk_tiny.stderr"

// detectPrefix returns the start of a stub script that must also survive
// the detection run. On -dl it prints detectFixture with its language code
// replaced by lang, then exits 0 without more work.
func detectPrefix(lang string) string {
	fixture, err := filepath.Abs(detectFixture)
	if err != nil {
		panic(err)
	}
	return `for a in "$@"; do
  if [ "$a" = "-dl" ]; then
    sed "s/auto-detected language: en /auto-detected language: ` + lang + ` /" "` + fixture + `" >&2
    exit 0
  fi
done
`
}

// stubFFmpeg returns a fake ffmpeg that fails at once, so the detection uses
// the audio as it is. A test with a tight timeout or cancel needs it: the
// real ffmpeg can take longer than the test waits when the machine is
// loaded, and then the cancel fires before the whisper-cli stub starts.
func stubFFmpeg(t *testing.T) string {
	t.Helper()
	return writeScript(t, "#!/usr/bin/env bash\nexit 1\n")
}

// stubThatWritesOutput returns a whisper-cli stub script that locates its
// own "-of <base>" argument and writes json to "<base>.json" before
// exiting 0 — the shape every success-path test needs. It answers a
// detection run (-dl) with English, see detectPrefix.
func stubThatWritesOutput(json string) string {
	return stubThatWritesOutputFor("en", json)
}

// stubThatWritesOutputFor is stubThatWritesOutput with a chosen detected
// language.
func stubThatWritesOutputFor(lang, json string) string {
	return `#!/usr/bin/env bash
` + detectPrefix(lang) + `of=""
args=("$@")
for ((i=0; i<${#args[@]}; i++)); do
  if [ "${args[$i]}" = "-of" ]; then
    of="${args[$((i+1))]}"
  fi
done
cat > "${of}.json" <<'WHISPEREOF'
` + json + `
WHISPEREOF
exit 0
`
}

func TestTranscribe_success(t *testing.T) {
	modelDir := t.TempDir()
	newModel(t, modelDir, "large-v3-turbo-q5_0")
	bin := writeScript(t, stubThatWritesOutput(sampleOJ))

	tr := New(Config{Binary: bin, ModelDir: modelDir, Timeout: 5 * time.Second})
	req := domain.TranscribeRequest{
		AudioPath: newAudioPath(t),
		Options:   domain.Options{Model: "large-v3-turbo-q5_0", Task: domain.TaskTranscribe, BeamSize: 5},
	}

	got, err := tr.Transcribe(context.Background(), req)
	require.NoError(t, err)
	assert.Equal(t, "en", got.Language)
	assert.Equal(t, "large-v3-turbo-q5_0", got.Model)
	assert.Equal(t, domain.TaskTranscribe, got.Task)
	assert.Equal(t, "Hello there. How are you?", got.Text)
}

func TestTranscribe_forwardsGPUConfiguration(t *testing.T) {
	for _, tt := range []struct {
		name   string
		noGPU  bool
		device int
		want   []string
	}{
		{"default GPU", false, 0, []string{"--device", "0"}},
		{"selected GPU", false, 2, []string{"--device", "2"}},
		{"CPU", true, 2, []string{"--no-gpu"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			modelDir := t.TempDir()
			newModel(t, modelDir, "large-v3-turbo-q5_0")
			argsPath := filepath.Join(t.TempDir(), "args.txt")
			t.Setenv("WHISPER_TEST_ARGS_PATH", argsPath)
			script := strings.Replace(stubThatWritesOutput(sampleOJ), "of=\"\"", "printf '%s\\n' \"$@\" > \"$WHISPER_TEST_ARGS_PATH\"\nof=\"\"", 1)
			tr := New(Config{Binary: writeScript(t, script), ModelDir: modelDir, NoGPU: tt.noGPU, GPUDevice: tt.device, Timeout: 5 * time.Second})
			req := domain.TranscribeRequest{AudioPath: newAudioPath(t), Options: domain.Options{Model: "large-v3-turbo-q5_0", Task: domain.TaskTranscribe, BeamSize: 5}}
			_, err := tr.Transcribe(context.Background(), req)
			require.NoError(t, err)
			data, err := os.ReadFile(argsPath)
			require.NoError(t, err)
			args := strings.Split(strings.TrimSpace(string(data)), "\n")
			i := indexOf(args, tt.want[0])
			require.GreaterOrEqual(t, i, 0)
			require.LessOrEqual(t, i+len(tt.want), len(args))
			assert.Equal(t, tt.want, args[i:i+len(tt.want)])
			if tt.noGPU {
				assert.NotContains(t, args, "--device")
			} else {
				assert.NotContains(t, args, "--no-gpu")
			}
		})
	}
}

func TestTranscribe_perJobTempDirIsCleanedUp(t *testing.T) {
	modelDir := t.TempDir()
	newModel(t, modelDir, "large-v3-turbo-q5_0")
	bin := writeScript(t, stubThatWritesOutput(sampleOJ))

	tr := New(Config{Binary: bin, ModelDir: modelDir, Timeout: 5 * time.Second})
	audioPath := newAudioPath(t)
	req := domain.TranscribeRequest{AudioPath: audioPath, Options: domain.Options{Model: "large-v3-turbo-q5_0", Task: domain.TaskTranscribe, BeamSize: 5}}

	_, err := tr.Transcribe(context.Background(), req)
	require.NoError(t, err)

	entries, err := os.ReadDir(filepath.Dir(audioPath))
	require.NoError(t, err)
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	assert.Equal(t, []string{"audio.wav"}, names, "Transcribe's per-job temp output dir must not survive the call")
}

func TestTranscribe_modelMissingFailsBeforeExec(t *testing.T) {
	modelDir := t.TempDir()
	// No model file written — Transcribe must fail before ever invoking the
	// binary. Point Binary at a script that would panic-exit loudly if
	// invoked, so a regression (execing despite the missing model) is
	// caught by the wrong error code, not silently.
	bin := writeScript(t, "#!/usr/bin/env bash\necho 'should never run' >&2\nexit 1\n")

	tr := New(Config{Binary: bin, ModelDir: modelDir, Timeout: 5 * time.Second})
	req := domain.TranscribeRequest{AudioPath: newAudioPath(t), Options: domain.Options{Model: "large-v3-turbo-q5_0", Task: domain.TaskTranscribe, BeamSize: 5}}

	_, err := tr.Transcribe(context.Background(), req)
	require.Error(t, err)
	assert.Equal(t, domain.ErrCodeModelUnavailable, domain.CodeOf(err))
	de := domain.AsError(err)
	assert.Equal(t, "large-v3-turbo-q5_0", de.Details["model"])
	_, hasPath := de.Details["path"]
	assert.False(t, hasPath, "model_unavailable must not echo the resolved absolute path back to the caller")
}

// TestTranscribe_traversingModelNameRejectedBeforeExec is modelPath's own
// gate, independent of domain.Options.Validate's modelNamePattern: this
// package must not rely on every caller having gone through Validate
// first. Binary is pointed at a script that would panic-exit loudly if
// invoked, so a regression (execing despite the escape) is caught by the
// wrong error, not silently.
func TestTranscribe_traversingModelNameRejectedBeforeExec(t *testing.T) {
	modelDir := t.TempDir()
	bin := writeScript(t, "#!/usr/bin/env bash\necho 'should never run' >&2\nexit 1\n")

	tr := New(Config{Binary: bin, ModelDir: modelDir, Timeout: 5 * time.Second})
	req := domain.TranscribeRequest{
		AudioPath: newAudioPath(t),
		Options:   domain.Options{Model: "../../../etc/passwd", Task: domain.TaskTranscribe, BeamSize: 5},
	}

	_, err := tr.Transcribe(context.Background(), req)
	require.Error(t, err)
	assert.Equal(t, domain.ErrCodeInvalidInput, domain.CodeOf(err))
}

func TestTranscribe_emptyModelIsRejected(t *testing.T) {
	tr := New(Config{Binary: "unused", ModelDir: t.TempDir(), Timeout: 5 * time.Second})
	req := domain.TranscribeRequest{AudioPath: newAudioPath(t), Options: domain.Options{Model: "", Task: domain.TaskTranscribe, BeamSize: 5}}

	_, err := tr.Transcribe(context.Background(), req)
	require.Error(t, err)
	assert.Equal(t, domain.ErrCodeInvalidInput, domain.CodeOf(err))
}

func TestTranscribe_nonZeroExitClassifiesToolFailure(t *testing.T) {
	modelDir := t.TempDir()
	newModel(t, modelDir, "large-v3-turbo-q5_0")
	bin := writeScript(t, "#!/usr/bin/env bash\necho 'whisper_init: failed to load model' >&2\nexit 1\n")

	tr := New(Config{Binary: bin, ModelDir: modelDir, Timeout: 5 * time.Second})
	req := domain.TranscribeRequest{AudioPath: newAudioPath(t), Options: domain.Options{Model: "large-v3-turbo-q5_0", Task: domain.TaskTranscribe, BeamSize: 5}}

	_, err := tr.Transcribe(context.Background(), req)
	require.Error(t, err)
	assert.Equal(t, domain.ErrCodeToolFailure, domain.CodeOf(err))
	de := domain.AsError(err)
	assert.Contains(t, de.Details["stderr"], "failed to load model")
}

func TestTranscribe_missingOutputFileIsToolFailure(t *testing.T) {
	modelDir := t.TempDir()
	newModel(t, modelDir, "large-v3-turbo-q5_0")
	// Exits 0 (also for the detection run) but never writes <outBase>.json.
	bin := writeScript(t, "#!/usr/bin/env bash\n"+detectPrefix("en")+"exit 0\n")

	tr := New(Config{Binary: bin, ModelDir: modelDir, Timeout: 5 * time.Second})
	req := domain.TranscribeRequest{AudioPath: newAudioPath(t), Options: domain.Options{Model: "large-v3-turbo-q5_0", Task: domain.TaskTranscribe, BeamSize: 5}}

	_, err := tr.Transcribe(context.Background(), req)
	require.Error(t, err)
	assert.Equal(t, domain.ErrCodeToolFailure, domain.CodeOf(err))
}

func TestTranscribe_malformedOutputJSONIsToolFailure(t *testing.T) {
	modelDir := t.TempDir()
	newModel(t, modelDir, "large-v3-turbo-q5_0")
	bin := writeScript(t, stubThatWritesOutput("not valid json"))

	tr := New(Config{Binary: bin, ModelDir: modelDir, Timeout: 5 * time.Second})
	req := domain.TranscribeRequest{AudioPath: newAudioPath(t), Options: domain.Options{Model: "large-v3-turbo-q5_0", Task: domain.TaskTranscribe, BeamSize: 5}}

	_, err := tr.Transcribe(context.Background(), req)
	require.Error(t, err)
	assert.Equal(t, domain.ErrCodeToolFailure, domain.CodeOf(err))
}

func TestTranscribe_timeout(t *testing.T) {
	modelDir := t.TempDir()
	newModel(t, modelDir, "large-v3-turbo-q5_0")
	bin := writeScript(t, "#!/usr/bin/env bash\nsleep 5\n")

	tr := New(Config{Binary: bin, FFmpegBinary: stubFFmpeg(t), ModelDir: modelDir, Timeout: 200 * time.Millisecond})
	req := domain.TranscribeRequest{AudioPath: newAudioPath(t), Options: domain.Options{Model: "large-v3-turbo-q5_0", Task: domain.TaskTranscribe, BeamSize: 5}}

	start := time.Now()
	_, err := tr.Transcribe(context.Background(), req)
	elapsed := time.Since(start)

	require.Error(t, err)
	assert.Equal(t, domain.ErrCodeTimeout, domain.CodeOf(err))
	assert.Less(t, elapsed, 4*time.Second)
}

func TestTranscribe_ctxCanceledKillsProcessGroup(t *testing.T) {
	modelDir := t.TempDir()
	newModel(t, modelDir, "large-v3-turbo-q5_0")
	childPIDFile := filepath.Join(t.TempDir(), "child.pid")
	bin := writeScript(t, `#!/usr/bin/env bash
sleep 100 &
echo "$!" > `+childPIDFile+`
sleep 5
`)

	tr := New(Config{Binary: bin, FFmpegBinary: stubFFmpeg(t), ModelDir: modelDir, Timeout: 10 * time.Second})
	req := domain.TranscribeRequest{AudioPath: newAudioPath(t), Options: domain.Options{Model: "large-v3-turbo-q5_0", Task: domain.TaskTranscribe, BeamSize: 5}}

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(200 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	_, err := tr.Transcribe(ctx, req)
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

func TestTranscribe_wordTimestampsUsesOJFAndPopulatesWords(t *testing.T) {
	modelDir := t.TempDir()
	newModel(t, modelDir, "large-v3-turbo-q5_0")

	// The stub asserts -ojf (not -oj) was actually passed, by only
	// producing valid output when it sees -ojf — proving Transcribe's
	// argv, not just parseWhisperJSON, switches on WordTimestamps.
	bin := writeScript(t, `#!/usr/bin/env bash
`+detectPrefix("en")+`of=""
sawOjf=false
args=("$@")
for ((i=0; i<${#args[@]}; i++)); do
  case "${args[$i]}" in
    -of) of="${args[$((i+1))]}" ;;
    -ojf) sawOjf=true ;;
  esac
done
if ! $sawOjf; then
  echo "expected -ojf" >&2
  exit 1
fi
cat > "${of}.json" <<'WHISPEREOF'
`+sampleOJF+`
WHISPEREOF
exit 0
`)

	tr := New(Config{Binary: bin, ModelDir: modelDir, Timeout: 5 * time.Second})
	req := domain.TranscribeRequest{
		AudioPath: newAudioPath(t),
		Options:   domain.Options{Model: "large-v3-turbo-q5_0", Task: domain.TaskTranscribe, BeamSize: 5, WordTimestamps: true},
	}

	got, err := tr.Transcribe(context.Background(), req)
	require.NoError(t, err)
	require.Len(t, got.Segments, 1)
	require.Len(t, got.Segments[0].Words, 2)
	assert.Equal(t, "Hello", got.Segments[0].Words[0].Word)
}
