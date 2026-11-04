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

const (
	turbo       = "large-v3-turbo-q5_0"
	translation = "large-v3-q5_0"
)

// langFixture is a fake whisper-cli for the language pipeline. Every call
// appends its argument list as one line to logPath. A detection run (-dl)
// prints the recorded real stderr with the language set to detect and exits
// 0; any other run writes sampleOJ to its -of path.
type langFixture struct {
	t         *testing.T
	modelDir  string
	logPath   string
	binary    string
	audioPath string
}

// newLangFixture builds a model dir with the turbo model, and with the
// translation model only when withTranslation is true. detectBody is the
// shell snippet that answers -dl.
func newLangFixture(t *testing.T, detectBody string, withTranslation bool) *langFixture {
	t.Helper()
	f := &langFixture{t: t, modelDir: t.TempDir(), logPath: filepath.Join(t.TempDir(), "calls.log")}
	newModel(t, f.modelDir, turbo)
	if withTranslation {
		newModel(t, f.modelDir, translation)
	}
	f.audioPath = newAudioPath(t)
	f.binary = writeScript(t, `#!/usr/bin/env bash
echo "$*" >> "`+f.logPath+`"
for a in "$@"; do
  if [ "$a" = "-dl" ]; then
`+detectBody+`
  fi
done
of=""
args=("$@")
for ((i=0; i<${#args[@]}; i++)); do
  if [ "${args[$i]}" = "-of" ]; then of="${args[$((i+1))]}"; fi
done
cat > "${of}.json" <<'WHISPEREOF'
`+sampleOJ+`
WHISPEREOF
exit 0
`)
	return f
}

// detectsAs is the detect snippet for a successful detection of lang, built
// from the recorded real output.
func detectsAs(t *testing.T, lang string) string {
	t.Helper()
	fixture, err := filepath.Abs(detectFixture)
	require.NoError(t, err)
	return `    sed "s/auto-detected language: en /auto-detected language: ` + lang + ` /" "` + fixture + `" >&2
    exit 0`
}

func (f *langFixture) transcriber(timeout time.Duration) *Transcriber {
	return New(Config{Binary: f.binary, ModelDir: f.modelDir, Threads: 2, Timeout: timeout})
}

func (f *langFixture) request(language string, task domain.Task) domain.TranscribeRequest {
	return domain.TranscribeRequest{
		AudioPath: f.audioPath,
		Options:   domain.Options{Model: turbo, Language: language, Task: task, BeamSize: 5},
	}
}

// calls returns the recorded calls, one argument slice each.
func (f *langFixture) calls() [][]string {
	f.t.Helper()
	data, err := os.ReadFile(f.logPath)
	if os.IsNotExist(err) {
		return nil
	}
	require.NoError(f.t, err)
	var out [][]string
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		out = append(out, strings.Fields(line))
	}
	return out
}

// arg returns the value after flag in args, or "" when flag is absent.
func arg(args []string, flag string) string {
	if i := indexOf(args, flag); i >= 0 && i+1 < len(args) {
		return args[i+1]
	}
	return ""
}

// requireOneRun checks that the fixture saw exactly the detection run and
// then the transcription run, and returns both.
func (f *langFixture) requireDetectThenRun() (detect, run []string) {
	f.t.Helper()
	calls := f.calls()
	require.Len(f.t, calls, 2, "expected one detection run and one transcription run, got %v", calls)
	require.Contains(f.t, calls[0], "-dl", "the first run must be the detection run")
	require.NotContains(f.t, calls[1], "-dl")
	return calls[0], calls[1]
}

func TestTranscribe_spanishAudioStaysSpanishWithTurbo(t *testing.T) {
	// No translation model on disk: Spanish must not need it.
	f := newLangFixture(t, detectsAs(t, "es"), false)

	// A client that sends task=translate must still get Spanish.
	got, err := f.transcriber(5*time.Second).Transcribe(context.Background(), f.request("", domain.TaskTranslate))
	require.NoError(t, err)

	detect, run := f.requireDetectThenRun()
	assert.Equal(t, filepath.Join(f.modelDir, "ggml-"+turbo+".bin"), arg(detect, "-m"), "detection uses the turbo model")
	assert.Equal(t, f.audioPath, arg(detect, "-f"), "detection reads the same audio as transcription")
	assert.Equal(t, "es", arg(run, "-l"))
	assert.NotContains(t, run, "-tr", "Spanish audio must never be translated")
	assert.Equal(t, filepath.Join(f.modelDir, "ggml-"+turbo+".bin"), arg(run, "-m"))

	assert.Equal(t, "es", got.Language)
	assert.Equal(t, "es", got.OutputLanguage)
	assert.Equal(t, domain.TaskTranscribe, got.Task)
	assert.Equal(t, turbo, got.Model)
}

func TestTranscribe_englishAudioStaysEnglishWithTurbo(t *testing.T) {
	f := newLangFixture(t, detectsAs(t, "en"), false)

	got, err := f.transcriber(5*time.Second).Transcribe(context.Background(), f.request("", domain.TaskTranscribe))
	require.NoError(t, err)

	_, run := f.requireDetectThenRun()
	assert.Equal(t, "en", arg(run, "-l"))
	assert.NotContains(t, run, "-tr")
	assert.Equal(t, filepath.Join(f.modelDir, "ggml-"+turbo+".bin"), arg(run, "-m"))

	assert.Equal(t, "en", got.Language)
	assert.Equal(t, "en", got.OutputLanguage)
	assert.Equal(t, domain.TaskTranscribe, got.Task)
	assert.Equal(t, turbo, got.Model)
}

func TestTranscribe_thirdLanguageIsTranslatedWithTranslationModel(t *testing.T) {
	f := newLangFixture(t, detectsAs(t, "fr"), true)

	// Task=transcribe from the client does not stop the translation.
	got, err := f.transcriber(5*time.Second).Transcribe(context.Background(), f.request("", domain.TaskTranscribe))
	require.NoError(t, err)

	detect, run := f.requireDetectThenRun()
	assert.Equal(t, filepath.Join(f.modelDir, "ggml-"+turbo+".bin"), arg(detect, "-m"), "detection still uses the turbo model")
	assert.Equal(t, "fr", arg(run, "-l"))
	assert.Contains(t, run, "-tr")
	assert.Equal(t, filepath.Join(f.modelDir, "ggml-"+translation+".bin"), arg(run, "-m"))

	assert.Equal(t, "fr", got.Language)
	assert.Equal(t, "en", got.OutputLanguage)
	assert.Equal(t, domain.TaskTranslate, got.Task)
	assert.Equal(t, translation, got.Model)
}

func TestTranscribe_explicitHintSkipsDetection(t *testing.T) {
	for _, tt := range []struct {
		name          string
		hint          string
		task          domain.Task
		wantTr        bool
		wantModel     string
		wantOutputLng string
		wantTask      domain.Task
	}{
		{"fr hint translates", "fr", domain.TaskTranscribe, true, translation, "en", domain.TaskTranslate},
		{"es hint stays Spanish", "es", domain.TaskTranscribe, false, turbo, "es", domain.TaskTranscribe},
		{"es hint ignores task=translate", "es", domain.TaskTranslate, false, turbo, "es", domain.TaskTranscribe},
		{"en hint stays English", "en", domain.TaskTranslate, false, turbo, "en", domain.TaskTranscribe},
	} {
		t.Run(tt.name, func(t *testing.T) {
			// The detect snippet would report a wrong language: it must not run.
			f := newLangFixture(t, detectsAs(t, "de"), true)

			got, err := f.transcriber(5*time.Second).Transcribe(context.Background(), f.request(tt.hint, tt.task))
			require.NoError(t, err)

			calls := f.calls()
			require.Len(t, calls, 1, "a hint must skip detection")
			run := calls[0]
			assert.NotContains(t, run, "-dl")
			assert.Equal(t, tt.hint, arg(run, "-l"))
			assert.Equal(t, tt.wantTr, indexOf(run, "-tr") >= 0)
			assert.Equal(t, filepath.Join(f.modelDir, "ggml-"+tt.wantModel+".bin"), arg(run, "-m"))

			assert.Equal(t, tt.hint, got.Language)
			assert.Equal(t, tt.wantOutputLng, got.OutputLanguage)
			assert.Equal(t, tt.wantTask, got.Task)
			assert.Equal(t, tt.wantModel, got.Model)
		})
	}
}

func TestTranscribe_detectionFailureIsRetryableToolFailure(t *testing.T) {
	for _, tt := range []struct {
		name   string
		detect string
	}{
		{"non-zero exit", "    echo 'whisper_full_with_state: failed to auto-detect language' >&2\n    exit 3"},
		{"no language line", "    echo 'nothing useful' >&2\n    exit 0"},
		{"empty code", "    echo 'whisper_full_with_state: auto-detected language:  (p = 0.1)' >&2\n    exit 0"},
		{"malformed code", "    echo 'whisper_full_with_state: auto-detected language: (null) (p = 0.1)' >&2\n    exit 0"},
		{"upper case code", "    echo 'whisper_full_with_state: auto-detected language: ES (p = 0.1)' >&2\n    exit 0"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f := newLangFixture(t, tt.detect, true)

			_, err := f.transcriber(5*time.Second).Transcribe(context.Background(), f.request("", domain.TaskTranscribe))
			require.Error(t, err)
			assert.Equal(t, domain.ErrCodeToolFailure, domain.CodeOf(err))
			assert.True(t, domain.CodeOf(err).Retryable(), "detection failure keeps the existing retry rule")

			calls := f.calls()
			require.Len(t, calls, 1, "no transcription may run after a failed detection")
			assert.Contains(t, calls[0], "-dl")
		})
	}
}

func TestTranscribe_missingTranslationModelIsNonRetryableModelUnavailable(t *testing.T) {
	f := newLangFixture(t, detectsAs(t, "fr"), false)

	_, err := f.transcriber(5*time.Second).Transcribe(context.Background(), f.request("", domain.TaskTranscribe))
	require.Error(t, err)
	assert.Equal(t, domain.ErrCodeModelUnavailable, domain.CodeOf(err))
	assert.False(t, domain.CodeOf(err).Retryable(), "a missing file does not appear on its own")

	de := domain.AsError(err)
	assert.Contains(t, de.Message, translation, "the message must name the model")
	assert.Contains(t, de.Message, "make models", "the message must name the fix")
	assert.Equal(t, translation, de.Details["model"])
	_, hasPath := de.Details["path"]
	assert.False(t, hasPath)

	calls := f.calls()
	require.Len(t, calls, 1, "only detection may run: no silent fallback to turbo")
	assert.Contains(t, calls[0], "-dl")
	entries, err := os.ReadDir(filepath.Dir(f.audioPath))
	require.NoError(t, err)
	assert.Len(t, entries, 1, "no temp dir may be left behind")
}

func TestTranscribe_missingTranslationModelWithHintRunsNothing(t *testing.T) {
	f := newLangFixture(t, detectsAs(t, "fr"), false)

	_, err := f.transcriber(5*time.Second).Transcribe(context.Background(), f.request("fr", domain.TaskTranscribe))
	require.Error(t, err)
	assert.Equal(t, domain.ErrCodeModelUnavailable, domain.CodeOf(err))
	assert.Empty(t, f.calls(), "the check must come before any process starts")
}

func TestTranscribe_configuredTranslationModelIsUsed(t *testing.T) {
	f := newLangFixture(t, detectsAs(t, "de"), false)
	newModel(t, f.modelDir, "large-v3")
	tr := New(Config{Binary: f.binary, ModelDir: f.modelDir, TranslationModel: "large-v3", Timeout: 5 * time.Second})

	got, err := tr.Transcribe(context.Background(), f.request("", domain.TaskTranscribe))
	require.NoError(t, err)
	_, run := f.requireDetectThenRun()
	assert.Equal(t, filepath.Join(f.modelDir, "ggml-large-v3.bin"), arg(run, "-m"))
	assert.Equal(t, "large-v3", got.Model)
}

func TestTranscribe_detectionHonoursCancellation(t *testing.T) {
	f := newLangFixture(t, "    sleep 30", true)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(200 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	_, err := f.transcriber(20*time.Second).Transcribe(ctx, f.request("", domain.TaskTranscribe))
	require.Error(t, err)
	assert.Equal(t, domain.ErrCodeCanceled, domain.CodeOf(err))
	assert.Less(t, time.Since(start), 5*time.Second)
	assert.Len(t, f.calls(), 1, "no transcription may start after a cancel")
}

// Detection time counts in Config.Timeout: each of the two runs below is
// shorter than the timeout, and together they are longer.
func TestTranscribe_timeoutCoversDetectionAndTranscription(t *testing.T) {
	f := newLangFixture(t, "    sleep 0.4\n"+detectsAs(t, "es"), false)
	// Slow the transcription run too.
	script, err := os.ReadFile(f.binary)
	require.NoError(t, err)
	slowed := strings.Replace(string(script), `of=""`, "sleep 0.4\nof=\"\"", 1)
	require.NoError(t, os.WriteFile(f.binary, []byte(slowed), 0o755))

	_, err = f.transcriber(600*time.Millisecond).Transcribe(context.Background(), f.request("", domain.TaskTranscribe))
	require.Error(t, err)
	assert.Equal(t, domain.ErrCodeTimeout, domain.CodeOf(err))
}

func TestTranscribe_detectionLeavesNoTempFiles(t *testing.T) {
	f := newLangFixture(t, detectsAs(t, "en"), false)

	_, err := f.transcriber(5*time.Second).Transcribe(context.Background(), f.request("", domain.TaskTranscribe))
	require.NoError(t, err)
	entries, err := os.ReadDir(filepath.Dir(f.audioPath))
	require.NoError(t, err)
	assert.Len(t, entries, 1)
}

func TestLanguageScanner_realFixture(t *testing.T) {
	data, err := os.ReadFile(detectFixture)
	require.NoError(t, err)
	require.Greater(t, len(data), stderrTailCap, "the real log is longer than the stderr tail")

	s := &languageScanner{}
	// Feed it in odd chunks, so a line crosses a Write boundary.
	for i := 0; i < len(data); i += 37 {
		end := min(i+37, len(data))
		_, err := s.Write(data[i:end])
		require.NoError(t, err)
	}
	assert.Equal(t, "en", s.result())
}

// With --no-prints the line is gone. This is why buildDetectArgs never
// passes it.
func TestLanguageScanner_noPrintsFixtureHasNoLanguage(t *testing.T) {
	data, err := os.ReadFile("testdata/detect_language_jfk_tiny_no_prints.stderr")
	require.NoError(t, err)

	s := &languageScanner{}
	_, err = s.Write(data)
	require.NoError(t, err)
	assert.Equal(t, "", s.result())
}

func TestLanguageScanner_lastLineWithoutNewline(t *testing.T) {
	s := &languageScanner{}
	_, err := s.Write([]byte("whisper_full_with_state: auto-detected language: haw (p = 0.5)"))
	require.NoError(t, err)
	assert.Equal(t, "haw", s.result())
}

func TestAvailable_doesNotRequireTranslationModel(t *testing.T) {
	modelDir := t.TempDir()
	newModel(t, modelDir, turbo) // translation weights absent
	bin := writeScript(t, "#!/usr/bin/env bash\nexit 0\n")

	tr := New(Config{Binary: bin, ModelDir: modelDir, DefaultModel: turbo, TranslationModel: translation, Timeout: 5 * time.Second})
	require.NoError(t, tr.Available(context.Background()))
}
