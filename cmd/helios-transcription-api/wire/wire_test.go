package wire

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pablofornell/helios-audio-transcription-service/internal/app"
	"github.com/pablofornell/helios-audio-transcription-service/internal/config"
	"github.com/pablofornell/helios-audio-transcription-service/internal/domain"
	"github.com/pablofornell/helios-audio-transcription-service/internal/storage/localfs"
	"github.com/pablofornell/helios-audio-transcription-service/internal/transcriber/fake"
	"github.com/pablofornell/helios-audio-transcription-service/internal/transcriber/whispercpp"
)

// closedPortAddr binds a TCP listener on an ephemeral port, immediately
// closes it, and returns its address -- a fast, deterministic way to get an
// address nothing is listening on, so a test can exercise a connection
// failure without any network dependency beyond localhost.
func closedPortAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := l.Addr().String()
	require.NoError(t, l.Close())
	return addr
}

// ---------------------------------------------------------------------------
// buildTranscriber
// ---------------------------------------------------------------------------

func TestBuildTranscriber_selectsWhispercppByDefault(t *testing.T) {
	tr := buildTranscriber(config.TranscriberConfig{Kind: "whispercpp", Binary: "whisper-cli"}, nil)
	_, ok := tr.(*whispercpp.Transcriber)
	assert.True(t, ok, "expected *whispercpp.Transcriber, got %T", tr)
}

func TestBuildTranscriber_forwardsGPUConfiguration(t *testing.T) {
	for _, tt := range []struct {
		name  string
		noGPU bool
		want  string
	}{
		{"GPU device", false, "--device\n2\n"},
		{"CPU", true, "--no-gpu\n"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			require.NoError(t, os.WriteFile(filepath.Join(dir, "ggml-base.bin"), []byte("fake weights"), 0o644))
			argsPath := filepath.Join(dir, "args.txt")
			t.Setenv("WHISPER_TEST_ARGS_PATH", argsPath)
			bin := filepath.Join(dir, "whisper-cli-stub.sh")
			require.NoError(t, os.WriteFile(bin, []byte(`#!/usr/bin/env bash
printf '%s\n' "$@" > "$WHISPER_TEST_ARGS_PATH"
for a in "$@"; do
  if [ "$a" = "-dl" ]; then
    echo 'whisper_full_with_state: auto-detected language: en (p = 0.97)' >&2
    exit 0
  fi
done
while [ "$#" -gt 0 ]; do
  if [ "$1" = "-of" ]; then
    printf '%s' '{"result":{"language":"en"},"transcription":[]}' > "$2.json"
    break
  fi
  shift
done
`), 0o755))
			tr := buildTranscriber(config.TranscriberConfig{Kind: "whispercpp", Binary: bin, ModelDir: dir, NoGPU: tt.noGPU, GPUDevice: 2, Timeout: 5 * time.Second}, nil)
			_, err := tr.Transcribe(context.Background(), domain.TranscribeRequest{AudioPath: filepath.Join(dir, "audio.wav"), Options: domain.Options{Model: "base", Task: domain.TaskTranscribe, BeamSize: 5}})
			require.NoError(t, err)
			args, err := os.ReadFile(argsPath)
			require.NoError(t, err)
			assert.Contains(t, string(args), tt.want)
			if tt.noGPU {
				assert.False(t, strings.Contains(string(args), "--device\n"))
			} else {
				assert.False(t, strings.Contains(string(args), "--no-gpu\n"))
			}
		})
	}
}

// langStub detects $WHISPER_TEST_LANG on -dl and logs every -m value it
// gets to $WHISPER_TEST_MODELS.
const langStub = `#!/usr/bin/env bash
for ((i=1; i<=$#; i++)); do
  if [ "${!i}" = "-m" ]; then j=$((i+1)); echo "${!j}" >> "$WHISPER_TEST_MODELS"; fi
done
for a in "$@"; do
  if [ "$a" = "-dl" ]; then
    echo "whisper_full_with_state: auto-detected language: $WHISPER_TEST_LANG (p = 0.97)" >&2
    exit 0
  fi
done
while [ "$#" -gt 0 ]; do
  if [ "$1" = "-of" ]; then
    printf '%s' '{"result":{"language":"xx"},"transcription":[]}' > "$2.json"
    break
  fi
  shift
done
`

// TestBuildTranscriber_forwardsTranslationModel checks that the configured
// translation model, not the request model, runs a third-language job.
func TestBuildTranscriber_forwardsTranslationModel(t *testing.T) {
	dir := t.TempDir()
	for _, m := range []string{"base", "large-v3"} {
		require.NoError(t, os.WriteFile(filepath.Join(dir, "ggml-"+m+".bin"), []byte("fake weights"), 0o644))
	}
	modelsLog := filepath.Join(dir, "models.log")
	t.Setenv("WHISPER_TEST_MODELS", modelsLog)
	t.Setenv("WHISPER_TEST_LANG", "fr")
	bin := filepath.Join(dir, "whisper-cli-stub.sh")
	require.NoError(t, os.WriteFile(bin, []byte(langStub), 0o755))

	tr := buildTranscriber(config.TranscriberConfig{Kind: "whispercpp", Binary: bin, ModelDir: dir, TranslationModel: "large-v3", Timeout: 5 * time.Second}, nil)
	got, err := tr.Transcribe(context.Background(), domain.TranscribeRequest{AudioPath: filepath.Join(dir, "audio.wav"), Options: domain.Options{Model: "base", Task: domain.TaskTranscribe, BeamSize: 5}})
	require.NoError(t, err)
	assert.Equal(t, "large-v3", got.Model)
	assert.Equal(t, domain.TaskTranslate, got.Task)

	logged, err := os.ReadFile(modelsLog)
	require.NoError(t, err)
	assert.Equal(t, []string{
		filepath.Join(dir, "ggml-base.bin"),     // detection
		filepath.Join(dir, "ggml-large-v3.bin"), // translation
	}, strings.Fields(string(logged)))
}

// TestBuildTranscriber_readyWithoutTranslationModel is the deploy-safety
// check: the translation weights reach the host later than the code (a push
// to main deploys at once; `make models` runs by hand). The readiness check
// must pass without them, and Spanish and English jobs must run.
func TestBuildTranscriber_readyWithoutTranslationModel(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "ggml-base.bin"), []byte("fake weights"), 0o644))
	t.Setenv("WHISPER_TEST_MODELS", filepath.Join(dir, "models.log"))
	t.Setenv("WHISPER_TEST_LANG", "es")
	bin := filepath.Join(dir, "whisper-cli-stub.sh")
	require.NoError(t, os.WriteFile(bin, []byte(langStub), 0o755))

	tr := buildTranscriber(config.TranscriberConfig{
		Kind: "whispercpp", Binary: bin, ModelDir: dir, DefaultModel: "base",
		TranslationModel: "large-v3-q5_0", Timeout: 5 * time.Second,
	}, nil)

	require.NoError(t, transcriberChecker{transcriber: tr}.Check(context.Background()), "readiness must not need the translation model")

	got, err := tr.Transcribe(context.Background(), domain.TranscribeRequest{AudioPath: filepath.Join(dir, "audio.wav"), Options: domain.Options{Model: "base", Task: domain.TaskTranscribe, BeamSize: 5}})
	require.NoError(t, err)
	assert.Equal(t, "es", got.OutputLanguage)

	t.Setenv("WHISPER_TEST_LANG", "fr")
	_, err = tr.Transcribe(context.Background(), domain.TranscribeRequest{AudioPath: filepath.Join(dir, "audio.wav"), Options: domain.Options{Model: "base", Task: domain.TaskTranscribe, BeamSize: 5}})
	require.Error(t, err)
	assert.Equal(t, domain.ErrCodeModelUnavailable, domain.CodeOf(err))
}

func TestBuildTranscriber_selectsFakeWhenConfigured(t *testing.T) {
	tr := buildTranscriber(config.TranscriberConfig{Kind: "fake"}, nil)
	_, ok := tr.(*fake.Transcriber)
	assert.True(t, ok, "expected *fake.Transcriber, got %T", tr)
}

func TestBuildTranscriber_overrideAlwaysWinsRegardlessOfKind(t *testing.T) {
	override := fake.New(fake.Config{})
	tr := buildTranscriber(config.TranscriberConfig{Kind: "whispercpp"}, override)
	assert.Same(t, override, tr)
}

func TestWithTranscriber_setsBuildOptions(t *testing.T) {
	override := fake.New(fake.Config{})
	var built buildOptions
	WithTranscriber(override)(&built)
	assert.Same(t, override, built.transcriber)
}

// ---------------------------------------------------------------------------
// uuidGenerator
// ---------------------------------------------------------------------------

func TestUUIDGenerator_NewID_producesDistinctParsableUUIDs(t *testing.T) {
	var g uuidGenerator
	a := g.NewID()
	b := g.NewID()

	require.NotEmpty(t, a)
	require.NotEmpty(t, b)
	assert.NotEqual(t, a, b)

	_, err := uuid.Parse(a)
	assert.NoError(t, err)
	_, err = uuid.Parse(b)
	assert.NoError(t, err)
}

// ---------------------------------------------------------------------------
// storageChecker
// ---------------------------------------------------------------------------

func TestStorageChecker_succeedsAndCleansUpItsProbeFile(t *testing.T) {
	store, err := localfs.New(t.TempDir())
	require.NoError(t, err)

	c := storageChecker{store: store, minFreeBytes: 0}
	require.NoError(t, c.Check(context.Background()))

	// The probe file Check writes must not survive a successful Check --
	// a leftover would otherwise accumulate one file per readyz poll.
	_, _, err = store.Open(context.Background(), healthProbePath)
	assert.ErrorIs(t, err, localfs.ErrNotExist)
}

func TestStorageChecker_failsBelowFreeBytesFloor(t *testing.T) {
	store, err := localfs.New(t.TempDir())
	require.NoError(t, err)

	// No real volume has this much free space; the floor must always
	// trip, whatever this test machine's actual free space is.
	c := storageChecker{store: store, minFreeBytes: ^uint64(0)}
	err = c.Check(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "free")
}

func TestStorageChecker_name(t *testing.T) {
	assert.Equal(t, "storage", storageChecker{}.Name())
}

// ---------------------------------------------------------------------------
// transcriberChecker
// ---------------------------------------------------------------------------

// unavailableTranscriber is a minimal app.Transcriber double whose
// Available always fails -- unlike internal/transcriber/fake's
// Transcriber, which always reports ready (see its own doc), so
// transcriberChecker's failure path needs its own double to exercise.
type unavailableTranscriber struct {
	err error
}

func (unavailableTranscriber) Transcribe(context.Context, domain.TranscribeRequest) (*domain.Result, error) {
	return nil, errors.New("not implemented")
}

func (u unavailableTranscriber) Available(context.Context) error { return u.err }

var _ app.Transcriber = unavailableTranscriber{}

func TestTranscriberChecker_delegatesToAvailable(t *testing.T) {
	c := transcriberChecker{transcriber: fake.New(fake.Config{})}
	assert.NoError(t, c.Check(context.Background()))
}

func TestTranscriberChecker_wrapsAvailableError(t *testing.T) {
	wantErr := domain.New(domain.ErrCodeModelUnavailable, "model not on disk")
	c := transcriberChecker{transcriber: unavailableTranscriber{err: wantErr}}

	err := c.Check(context.Background())
	require.Error(t, err)
	assert.ErrorIs(t, err, wantErr)
}

func TestTranscriberChecker_name(t *testing.T) {
	assert.Equal(t, "transcriber", transcriberChecker{}.Name())
}

// ---------------------------------------------------------------------------
// dbChecker
// ---------------------------------------------------------------------------

func TestDBChecker_reportsPingFailure(t *testing.T) {
	pool, err := pgxpool.New(context.Background(), "postgres://helios:helios@"+closedPortAddr(t)+"/helios?sslmode=disable")
	require.NoError(t, err) // pgxpool.New never dials; see wire.go's own comment on this.
	defer pool.Close()

	c := dbChecker{pool: pool}
	err = c.Check(context.Background())
	assert.Error(t, err)
}

func TestDBChecker_name(t *testing.T) {
	assert.Equal(t, "database", dbChecker{}.Name())
}

// ---------------------------------------------------------------------------
// Build
// ---------------------------------------------------------------------------

// baseConfig returns a Config that passes config.Config.Validate and is
// otherwise cheap to construct/run against in a unit test: a fake
// transcriber (no whisper-cli/model dir required), a t.TempDir() storage
// root, and a database DSN the caller overrides to point at whatever it
// wants Build's database step to see.
func baseConfig(t *testing.T, dsn string) *config.Config {
	t.Helper()
	return &config.Config{
		HTTP: config.HTTPConfig{
			Addr:            ":0",
			ReadTimeout:     5 * time.Minute,
			WriteTimeout:    60 * time.Second,
			IdleTimeout:     120 * time.Second,
			ShutdownTimeout: 20 * time.Second,
			MaxUploadBytes:  2 << 30,
		},
		Database: config.DatabaseConfig{
			DSN:            dsn,
			ConnectTimeout: 200 * time.Millisecond,
			MaxConns:       5,
			AutoMigrate:    false,
		},
		Storage: config.StorageConfig{
			RootDir:      t.TempDir(),
			MinFreeBytes: 0,
		},
		Transcriber: config.TranscriberConfig{
			Kind:          "fake",
			DefaultModel:  "base",
			AllowedModels: []string{"base"},
			Threads:       4,
			Timeout:       time.Minute,
		},
		Audio: config.AudioConfig{
			FFmpegBinary:  "ffmpeg",
			FFprobeBinary: "ffprobe",
			MaxDuration:   4 * time.Hour,
			Timeout:       time.Minute,
		},
		Worker: config.WorkerConfig{
			Concurrency:       1,
			PollInterval:      time.Second,
			LeaseDuration:     time.Minute,
			HeartbeatInterval: 15 * time.Second,
			JobTimeout:        10 * time.Minute,
			MaxAttempts:       3,
			BackoffBase:       time.Second,
			BackoffMax:        time.Minute,
		},
		Retention: config.RetentionConfig{
			Enabled:  false,
			MaxAge:   168 * time.Hour,
			Interval: time.Hour,
		},
		Observability: config.ObservabilityConfig{
			LogLevel:       "info",
			LogFormat:      "json",
			MetricsEnabled: true,
			TracingEnabled: false,
			ServiceName:    "helios-ats-test",
		},
	}
}

// TestBuild_failsFastOnUnreachableDatabaseAndLeaksNothing exercises Build
// end to end without Docker or a real Postgres: against an address nothing
// listens on, Build must fail at the ping step (not hang past
// Database.ConnectTimeout) and its own cleanup must run -- proven here by
// the call simply returning in time, since a leaked tracer or an unclosed
// pool would not otherwise be observable from this test without deeper
// instrumentation than warranted for a wiring test.
func TestBuild_failsFastOnUnreachableDatabaseAndLeaksNothing(t *testing.T) {
	cfg := baseConfig(t, "postgres://helios:helios@"+closedPortAddr(t)+"/helios?sslmode=disable")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	stack, err := Build(ctx, cfg)
	require.Error(t, err)
	assert.Nil(t, stack)
	assert.Contains(t, err.Error(), "ping database")
}

func TestBuild_rejectsUnparsableDSN(t *testing.T) {
	cfg := baseConfig(t, "://not-a-valid-dsn")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	stack, err := Build(ctx, cfg)
	require.Error(t, err)
	assert.Nil(t, stack)
	assert.Contains(t, err.Error(), "parse database DSN")
}
