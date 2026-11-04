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

// langFixture is a fake whisper-cli and a fake ffmpeg for the language
// pipeline. Each call appends its argument list as one line to a log. The
// fake ffmpeg writes a clip whose first line is "offset=<seconds>" (the -ss
// value, 0.000 when absent). A detection run (-dl) of the fake whisper-cli
// runs detectBody, which can read the clip path in $f. Any other whisper-cli
// run writes sampleOJ to its -of path.
type langFixture struct {
	t         *testing.T
	modelDir  string
	logPath   string
	ffLogPath string
	binary    string
	ffmpeg    string
	audioPath string
}

// ffmpegWritesClip is the default fake ffmpeg body: a clip of 20000 bytes.
const ffmpegWritesClip = `{ echo "offset=$ss"; head -c 20000 /dev/zero; } > "$out"`

// newLangFixture builds a model dir with the turbo model, and with the
// translation model only when withTranslation is true. detectBody is the
// shell snippet that answers -dl.
func newLangFixture(t *testing.T, detectBody string, withTranslation bool) *langFixture {
	t.Helper()
	f := &langFixture{t: t, modelDir: t.TempDir(), logPath: filepath.Join(t.TempDir(), "calls.log"), ffLogPath: filepath.Join(t.TempDir(), "ffmpeg.log")}
	newModel(t, f.modelDir, turbo)
	if withTranslation {
		newModel(t, f.modelDir, translation)
	}
	f.audioPath = newAudioPath(t)
	f.binary = writeScript(t, `#!/usr/bin/env bash
echo "$*" >> "`+f.logPath+`"
f=""
args=("$@")
for ((i=0; i<${#args[@]}; i++)); do
  if [ "${args[$i]}" = "-f" ]; then f="${args[$((i+1))]}"; fi
done
for a in "$@"; do
  if [ "$a" = "-dl" ]; then
`+detectBody+`
  fi
done
of=""
for ((i=0; i<${#args[@]}; i++)); do
  if [ "${args[$i]}" = "-of" ]; then of="${args[$((i+1))]}"; fi
done
cat > "${of}.json" <<'WHISPEREOF'
`+sampleOJ+`
WHISPEREOF
exit 0
`)
	f.setFFmpeg(ffmpegWritesClip)
	return f
}

// setFFmpeg replaces the fake ffmpeg. body runs with $ss (the -ss value) and
// $out (the output path) set.
func (f *langFixture) setFFmpeg(body string) {
	f.t.Helper()
	f.ffmpeg = writeScript(f.t, `#!/usr/bin/env bash
echo "$*" >> "`+f.ffLogPath+`"
ss=0.000
args=("$@")
n=${#args[@]}
for ((i=0; i<n; i++)); do
  if [ "${args[$i]}" = "-ss" ]; then ss="${args[$((i+1))]}"; fi
done
out="${args[$((n-1))]}"
`+body+`
exit 0
`)
}

// detectsAs is the detect snippet for a successful detection of lang, built
// from the recorded real output (its probability is 0.976673).
func detectsAs(t *testing.T, lang string) string {
	t.Helper()
	fixture, err := filepath.Abs(detectFixture)
	require.NoError(t, err)
	return `    sed "s/auto-detected language: en /auto-detected language: ` + lang + ` /" "` + fixture + `" >&2
    exit 0`
}

// detectsWith is the detect snippet for a detection of lang with
// probability prob.
func detectsWith(lang, prob string) string {
	return `    echo "whisper_full_with_state: auto-detected language: ` + lang + ` (p = ` + prob + `)" >&2
    exit 0`
}

// detectsPerClip answers by the "offset=" line of the clip in $f. results
// maps an offset ("0.000") to "lang prob". A clip with another first line
// (the audio as it is) gets the "other" entry.
func detectsPerClip(results map[string]string, other string) string {
	var cases string
	for off, res := range results {
		cases += "      \"offset=" + off + "\") r=\"" + res + "\" ;;\n"
	}
	return `    first=$(head -n1 "$f" | tr -d '\0')
    r="` + other + `"
    case "$first" in
` + cases + `    esac
    set -- $r
    echo "whisper_full_with_state: auto-detected language: $1 (p = $2)" >&2
    exit 0`
}

func (f *langFixture) transcriber(timeout time.Duration) *Transcriber {
	return New(Config{Binary: f.binary, FFmpegBinary: f.ffmpeg, ModelDir: f.modelDir, Threads: 2, Timeout: timeout})
}

// request builds a request. language is the (ignored) hint.
func (f *langFixture) request(language string, task domain.Task) domain.TranscribeRequest {
	return domain.TranscribeRequest{
		AudioPath: f.audioPath,
		Options:   domain.Options{Model: turbo, Language: language, Task: task, BeamSize: 5},
	}
}

// useLongAudio replaces the audio with a silent WAV-sized file of the given
// length: 44 header bytes and 32000 bytes per second.
func (f *langFixture) useLongAudio(seconds int) {
	f.t.Helper()
	require.NoError(f.t, os.WriteFile(f.audioPath, make([]byte, wavHeaderBytes+seconds*pcmBytesPerSecond), 0o644))
}

func readLines(t *testing.T, path string) [][]string {
	t.Helper()
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	require.NoError(t, err)
	var out [][]string
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		out = append(out, strings.Fields(line))
	}
	return out
}

// calls returns the recorded whisper-cli calls, one argument slice each.
func (f *langFixture) calls() [][]string {
	f.t.Helper()
	return readLines(f.t, f.logPath)
}

// ffmpegCalls returns the recorded ffmpeg calls.
func (f *langFixture) ffmpegCalls() [][]string {
	f.t.Helper()
	return readLines(f.t, f.ffLogPath)
}

// arg returns the value after flag in args, or "" when flag is absent.
func arg(args []string, flag string) string {
	if i := indexOf(args, flag); i >= 0 && i+1 < len(args) {
		return args[i+1]
	}
	return ""
}

// requireDetectThenRun checks that the fixture saw exactly one detection run
// and then the transcription run, and returns both.
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
	assert.Equal(t, "es", arg(run, "-l"))
	assert.NotContains(t, run, "-tr", "Spanish audio must never be translated")
	assert.Equal(t, filepath.Join(f.modelDir, "ggml-"+turbo+".bin"), arg(run, "-m"))
	assert.Equal(t, f.audioPath, arg(run, "-f"))

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

// The hint plays no part. A wrong hint gave a wrong transcript before: the
// detection always runs and decides. The cases include old-client codes
// ("spa", an unknown "xx").
func TestTranscribe_languageHintIsIgnored(t *testing.T) {
	for _, tt := range []struct {
		name      string
		hint      string
		detected  string
		task      domain.Task
		wantTr    bool
		wantModel string
		wantOut   string
	}{
		{"fr hint on Spanish audio", "fr", "es", domain.TaskTranscribe, false, turbo, "es"},
		{"es hint on French audio", "es", "fr", domain.TaskTranscribe, true, translation, "en"},
		{"en hint on Spanish audio", "en", "es", domain.TaskTranscribe, false, turbo, "es"},
		{"es hint, task=translate, on Spanish audio", "es", "es", domain.TaskTranslate, false, turbo, "es"},
		{"three-letter spa hint on Spanish audio", "spa", "es", domain.TaskTranscribe, false, turbo, "es"},
		{"unknown xx hint on English audio", "xx", "en", domain.TaskTranscribe, false, turbo, "en"},
		{"unknown xx hint on French audio", "xx", "fr", domain.TaskTranscribe, true, translation, "en"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f := newLangFixture(t, detectsAs(t, tt.detected), true)

			got, err := f.transcriber(5*time.Second).Transcribe(context.Background(), f.request(tt.hint, tt.task))
			require.NoError(t, err)

			detect, run := f.requireDetectThenRun()
			assert.Contains(t, detect, "-dl", "detection runs even with a hint")
			assert.Equal(t, tt.detected, arg(run, "-l"), "-l follows the detection, not the hint")
			assert.Equal(t, tt.wantTr, indexOf(run, "-tr") >= 0)
			assert.Equal(t, filepath.Join(f.modelDir, "ggml-"+tt.wantModel+".bin"), arg(run, "-m"))

			assert.Equal(t, tt.detected, got.Language)
			assert.Equal(t, tt.wantOut, got.OutputLanguage)
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

// A hint of "fr" does not make a Spanish job need the translation model.
func TestTranscribe_frenchHintOnSpanishAudioNeedsNoTranslationModel(t *testing.T) {
	f := newLangFixture(t, detectsAs(t, "es"), false)

	got, err := f.transcriber(5*time.Second).Transcribe(context.Background(), f.request("fr", domain.TaskTranscribe))
	require.NoError(t, err)
	assert.Equal(t, "es", got.OutputLanguage)
}

func TestTranscribe_configuredTranslationModelIsUsed(t *testing.T) {
	f := newLangFixture(t, detectsAs(t, "de"), false)
	newModel(t, f.modelDir, "large-v3")
	tr := New(Config{Binary: f.binary, FFmpegBinary: f.ffmpeg, ModelDir: f.modelDir, TranslationModel: "large-v3", Timeout: 5 * time.Second})

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

func TestTranscribe_clipCuttingHonoursCancellation(t *testing.T) {
	f := newLangFixture(t, detectsAs(t, "en"), true)
	f.setFFmpeg("sleep 30")

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
	assert.Empty(t, f.calls(), "whisper-cli must not start after a cancel")
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

// --- leading silence and low confidence ---

func TestTranscribe_detectionRunsOnClipWithLeadingSilenceRemoved(t *testing.T) {
	f := newLangFixture(t, detectsWith("es", "0.99"), false)

	_, err := f.transcriber(5*time.Second).Transcribe(context.Background(), f.request("", domain.TaskTranscribe))
	require.NoError(t, err)

	ff := f.ffmpegCalls()
	require.Len(t, ff, 1, "a confident first window needs one clip")
	assert.Equal(t, f.audioPath, arg(ff[0], "-i"))
	assert.Equal(t, "file", arg(ff[0], "-protocol_whitelist"))
	assert.Contains(t, arg(ff[0], "-af"), "silenceremove=start_periods=1")
	assert.Equal(t, "30.000", arg(ff[0], "-t"), "a clip is at most the 30 s whisper.cpp reads")
	assert.NotContains(t, ff[0], "-ss", "the first clip starts at the start")

	detect, run := f.requireDetectThenRun()
	assert.NotEqual(t, f.audioPath, arg(detect, "-f"), "detection reads the clip")
	assert.Equal(t, f.audioPath, arg(run, "-f"), "transcription reads the whole audio")
	assert.Equal(t, 1, strings.Count(strings.Join(ff[0], " "), "-i "))
}

func TestTranscribe_confidentFirstWindowStopsAtOneDetection(t *testing.T) {
	f := newLangFixture(t, detectsWith("fr", "0.969"), true)
	f.useLongAudio(90)

	got, err := f.transcriber(5*time.Second).Transcribe(context.Background(), f.request("", domain.TaskTranscribe))
	require.NoError(t, err)
	assert.Equal(t, "fr", got.Language)
	f.requireDetectThenRun()
	assert.Len(t, f.ffmpegCalls(), 1)
}

// 35 s of silence before French gave "en" with p = 0.354 on the real binary.
// A weak first window makes the service look at one third and two thirds of
// the audio and keep the most probable answer.
func TestTranscribe_lowConfidenceLooksAtLaterWindowsAndKeepsBest(t *testing.T) {
	f := newLangFixture(t, detectsPerClip(map[string]string{
		"0.000":  "en 0.354",
		"30.000": "fr 0.981",
		"60.000": "es 0.700",
	}, "en 0.1"), true)
	f.useLongAudio(90)

	got, err := f.transcriber(5*time.Second).Transcribe(context.Background(), f.request("", domain.TaskTranscribe))
	require.NoError(t, err)

	ff := f.ffmpegCalls()
	require.Len(t, ff, 3)
	assert.Equal(t, "30.000", arg(ff[1], "-ss"), "one third of 90 s")
	assert.Equal(t, "60.000", arg(ff[2], "-ss"), "two thirds of 90 s")

	calls := f.calls()
	require.Len(t, calls, 4, "three detections and one transcription")
	run := calls[3]
	assert.Equal(t, "fr", arg(run, "-l"))
	assert.Contains(t, run, "-tr")
	assert.Equal(t, "fr", got.Language)
	assert.Equal(t, "en", got.OutputLanguage)
}

func TestTranscribe_lowConfidenceNeverFailsTheJob(t *testing.T) {
	f := newLangFixture(t, detectsPerClip(map[string]string{
		"0.000":  "en 0.200",
		"30.000": "es 0.400",
		"60.000": "en 0.300",
	}, "en 0.1"), false)
	f.useLongAudio(90)

	got, err := f.transcriber(5*time.Second).Transcribe(context.Background(), f.request("", domain.TaskTranscribe))
	require.NoError(t, err, "speechless audio is valid")
	assert.Equal(t, "es", got.Language, "the best of the weak results stays")
}

func TestTranscribe_lowConfidenceOnShortAudioHasNoLaterWindows(t *testing.T) {
	f := newLangFixture(t, detectsWith("en", "0.2"), false)
	f.useLongAudio(4)

	got, err := f.transcriber(5*time.Second).Transcribe(context.Background(), f.request("", domain.TaskTranscribe))
	require.NoError(t, err)
	assert.Equal(t, "en", got.Language)
	assert.Len(t, f.ffmpegCalls(), 1)
	f.requireDetectThenRun()
}

func TestTranscribe_failedClipFallsBackToTheAudioAsItIs(t *testing.T) {
	for _, tt := range []struct{ name, body string }{
		{"ffmpeg exits non-zero", "exit 1"},
		{"all silence: header-only clip", `printf 'RIFF' > "$out"`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f := newLangFixture(t, detectsWith("es", "0.95"), false)
			f.setFFmpeg(tt.body)

			got, err := f.transcriber(5*time.Second).Transcribe(context.Background(), f.request("", domain.TaskTranscribe))
			require.NoError(t, err)
			assert.Equal(t, "es", got.Language)
			detect, _ := f.requireDetectThenRun()
			assert.Equal(t, f.audioPath, arg(detect, "-f"), "detection falls back to the audio")
		})
	}
}

func TestTranscribe_missingFFmpegFallsBackToTheAudioAsItIs(t *testing.T) {
	f := newLangFixture(t, detectsWith("en", "0.95"), false)
	f.ffmpeg = filepath.Join(t.TempDir(), "no-such-ffmpeg")

	got, err := f.transcriber(5*time.Second).Transcribe(context.Background(), f.request("", domain.TaskTranscribe))
	require.NoError(t, err)
	assert.Equal(t, "en", got.Language)
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
	code, prob := s.result()
	assert.Equal(t, "en", code)
	assert.InDelta(t, 0.976673, prob, 1e-9)
}

// With --no-prints the line is gone. This is why buildDetectArgs never
// passes it.
func TestLanguageScanner_noPrintsFixtureHasNoLanguage(t *testing.T) {
	data, err := os.ReadFile("testdata/detect_language_jfk_tiny_no_prints.stderr")
	require.NoError(t, err)

	s := &languageScanner{}
	_, err = s.Write(data)
	require.NoError(t, err)
	code, _ := s.result()
	assert.Equal(t, "", code)
}

func TestLanguageScanner_lastLineWithoutNewline(t *testing.T) {
	s := &languageScanner{}
	_, err := s.Write([]byte("whisper_full_with_state: auto-detected language: haw (p = 0.5)"))
	require.NoError(t, err)
	code, prob := s.result()
	assert.Equal(t, "haw", code)
	assert.InDelta(t, 0.5, prob, 1e-9)
}

func TestLanguageScanner_missingProbabilityIsZero(t *testing.T) {
	s := &languageScanner{}
	_, err := s.Write([]byte("whisper_full_with_state: auto-detected language: fr\n"))
	require.NoError(t, err)
	code, prob := s.result()
	assert.Equal(t, "fr", code)
	assert.Equal(t, 0.0, prob)
}

func TestAvailable_doesNotRequireTranslationModel(t *testing.T) {
	modelDir := t.TempDir()
	newModel(t, modelDir, turbo) // translation weights absent
	bin := writeScript(t, "#!/usr/bin/env bash\nexit 0\n")

	tr := New(Config{Binary: bin, ModelDir: modelDir, DefaultModel: turbo, TranslationModel: translation, Timeout: 5 * time.Second})
	require.NoError(t, tr.Available(context.Background()))
}
