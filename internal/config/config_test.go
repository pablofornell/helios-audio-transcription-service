package config

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// validConfig returns a Config that satisfies every Validate rule, so
// tests can mutate a single field and assert exactly that rule fires.
func validConfig() *Config {
	return &Config{
		HTTP: HTTPConfig{
			Addr:            ":7462",
			ReadTimeout:     5 * time.Minute,
			WriteTimeout:    60 * time.Second,
			IdleTimeout:     120 * time.Second,
			ShutdownTimeout: 20 * time.Second,
			MaxUploadBytes:  2 * giB,
		},
		Database: DatabaseConfig{
			DSN:            "postgres://helios:helios@localhost:5432/helios_ats?sslmode=disable",
			ConnectTimeout: 10 * time.Second,
			MaxConns:       10,
			AutoMigrate:    true,
		},
		Storage: StorageConfig{
			RootDir:      "/data",
			MinFreeBytes: giB,
		},
		Transcriber: TranscriberConfig{
			Kind:          "whispercpp",
			Binary:        "whisper-cli",
			ModelDir:      "/models",
			DefaultModel:  SupportedModel,
			AllowedModels: []string{SupportedModel},
			Threads:       4,
			Timeout:       30 * time.Minute,
		},
		Audio: AudioConfig{
			FFmpegBinary:  "ffmpeg",
			FFprobeBinary: "ffprobe",
			MaxDuration:   4 * time.Hour,
			Timeout:       10 * time.Minute,
		},
		Worker: WorkerConfig{
			Concurrency:       2,
			PollInterval:      2 * time.Second,
			LeaseDuration:     time.Minute,
			HeartbeatInterval: 15 * time.Second,
			JobTimeout:        45 * time.Minute,
			MaxAttempts:       3,
			BackoffBase:       10 * time.Second,
			BackoffMax:        10 * time.Minute,
		},
		Retention: RetentionConfig{
			Enabled:  true,
			MaxAge:   168 * time.Hour,
			Interval: time.Hour,
		},
		Observability: ObservabilityConfig{
			LogLevel:       "info",
			LogFormat:      "json",
			MetricsEnabled: true,
			TracingEnabled: false,
			ServiceName:    "helios-ats",
		},
	}
}

func TestValidate_BaselineIsValid(t *testing.T) {
	assert.NoError(t, validConfig().Validate())
}

func TestValidate_DatabaseDSNRequired(t *testing.T) {
	cfg := validConfig()
	cfg.Database.DSN = ""
	assert.Error(t, cfg.Validate())
}

func TestValidate_DatabaseConnectTimeoutMustBePositive(t *testing.T) {
	for _, d := range []time.Duration{0, -time.Second} {
		cfg := validConfig()
		cfg.Database.ConnectTimeout = d
		assert.Error(t, cfg.Validate(), "ConnectTimeout=%s must be rejected", d)
	}
}

func TestValidate_DatabaseMaxConnsMustBePositive(t *testing.T) {
	for _, n := range []int{0, -1} {
		cfg := validConfig()
		cfg.Database.MaxConns = n
		assert.Error(t, cfg.Validate(), "MaxConns=%d must be rejected", n)
	}
}

func TestValidate_MaxUploadBytesMustBePositive(t *testing.T) {
	for _, n := range []int64{0, -1} {
		cfg := validConfig()
		cfg.HTTP.MaxUploadBytes = n
		assert.Error(t, cfg.Validate(), "MaxUploadBytes=%d must be rejected", n)
	}
}

func TestValidate_StorageRootDirRequired(t *testing.T) {
	cfg := validConfig()
	cfg.Storage.RootDir = ""
	assert.Error(t, cfg.Validate())
}

// TestValidate_StorageMinFreeBytesRejectsNegative is the review-confirmed
// finding: a negative value used to be silently clamped to 0 downstream
// (e.g. in wire's `if cfg.Storage.MinFreeBytes > 0 { ... }`), which
// disables the free-space floor entirely instead of surfacing the
// operator's mistake.
func TestValidate_StorageMinFreeBytesRejectsNegative(t *testing.T) {
	cfg := validConfig()
	cfg.Storage.MinFreeBytes = -1
	assert.Error(t, cfg.Validate())
}

func TestValidate_StorageMinFreeBytesZeroIsAllowed(t *testing.T) {
	cfg := validConfig()
	cfg.Storage.MinFreeBytes = 0
	assert.NoError(t, cfg.Validate())
}

// TestValidate_EveryDurationKnobMustBePositive is the review-confirmed
// finding: HELIOS_WORKER_HEARTBEAT_INTERVAL=0s, HELIOS_WORKER_POLL_INTERVAL
// =0s and HELIOS_RETENTION_INTERVAL=0s each reach a time.NewTicker in a
// background goroutine, which panics on a non-positive duration -- the
// heartbeat one crashes the whole process on the very first upload, well
// after a clean startup and a green /readyz. Validate must reject a
// non-positive value for every time.Duration knob, not just those three.
func TestValidate_EveryDurationKnobMustBePositive(t *testing.T) {
	setters := map[string]func(cfg *Config, d time.Duration){
		"HTTP.ReadTimeout":         func(cfg *Config, d time.Duration) { cfg.HTTP.ReadTimeout = d },
		"HTTP.WriteTimeout":        func(cfg *Config, d time.Duration) { cfg.HTTP.WriteTimeout = d },
		"HTTP.IdleTimeout":         func(cfg *Config, d time.Duration) { cfg.HTTP.IdleTimeout = d },
		"HTTP.ShutdownTimeout":     func(cfg *Config, d time.Duration) { cfg.HTTP.ShutdownTimeout = d },
		"Database.ConnectTimeout":  func(cfg *Config, d time.Duration) { cfg.Database.ConnectTimeout = d },
		"Transcriber.Timeout":      func(cfg *Config, d time.Duration) { cfg.Transcriber.Timeout = d },
		"Audio.MaxDuration":        func(cfg *Config, d time.Duration) { cfg.Audio.MaxDuration = d },
		"Audio.Timeout":            func(cfg *Config, d time.Duration) { cfg.Audio.Timeout = d },
		"Worker.PollInterval":      func(cfg *Config, d time.Duration) { cfg.Worker.PollInterval = d },
		"Worker.LeaseDuration":     func(cfg *Config, d time.Duration) { cfg.Worker.LeaseDuration = d },
		"Worker.HeartbeatInterval": func(cfg *Config, d time.Duration) { cfg.Worker.HeartbeatInterval = d },
		"Worker.JobTimeout":        func(cfg *Config, d time.Duration) { cfg.Worker.JobTimeout = d },
		"Worker.BackoffBase":       func(cfg *Config, d time.Duration) { cfg.Worker.BackoffBase = d },
		"Worker.BackoffMax":        func(cfg *Config, d time.Duration) { cfg.Worker.BackoffMax = d },
		"Retention.MaxAge":         func(cfg *Config, d time.Duration) { cfg.Retention.MaxAge = d },
		"Retention.Interval":       func(cfg *Config, d time.Duration) { cfg.Retention.Interval = d },
	}
	for name, set := range setters {
		for _, d := range []time.Duration{0, -time.Second} {
			t.Run(fmt.Sprintf("%s=%s", name, d), func(t *testing.T) {
				cfg := validConfig()
				set(cfg, d)
				assert.Error(t, cfg.Validate(), "%s=%s must be rejected", name, d)
			})
		}
	}
}

func TestValidate_UnknownTranscriberKind(t *testing.T) {
	cfg := validConfig()
	cfg.Transcriber.Kind = "bogus"
	assert.Error(t, cfg.Validate())
}

func TestValidate_TranscriberKindFake(t *testing.T) {
	cfg := validConfig()
	cfg.Transcriber.Kind = "fake"
	assert.NoError(t, cfg.Validate())
}

func TestValidate_DefaultModelMustBeInAllowedModels(t *testing.T) {
	t.Run("not in a non-empty allowlist is rejected", func(t *testing.T) {
		cfg := validConfig()
		cfg.Transcriber.DefaultModel = "huge"
		assert.Error(t, cfg.Validate())
	})

	// An empty allow-list is now itself rejected (see
	// TestValidate_AllowedModelsMustNotBeEmpty): it used to silently skip
	// this check, which was half of a path traversal finding once
	// Options.Validate's own model check was also skipped for the same
	// reason.
	t.Run("empty allowlist is rejected, not skipped", func(t *testing.T) {
		cfg := validConfig()
		cfg.Transcriber.DefaultModel = "huge"
		cfg.Transcriber.AllowedModels = nil
		assert.Error(t, cfg.Validate())
	})
}

// TestValidate_AllowedModelsMustNotBeEmpty is the review-confirmed finding:
// HELIOS_TRANSCRIBER_ALLOWED_MODELS=" " parses to an empty (non-nil vs nil
// makes no difference here) slice, which used to make this check a no-op
// and, combined with Options.Validate skipping its own check the same way,
// let an unvalidated model name reach filepath.Join and escape ModelDir.
func TestValidate_AllowedModelsMustNotBeEmpty(t *testing.T) {
	cfg := validConfig()
	cfg.Transcriber.AllowedModels = nil
	assert.Error(t, cfg.Validate())

	cfg.Transcriber.AllowedModels = []string{}
	assert.Error(t, cfg.Validate())
}

// Even an explicit environment allowlist cannot re-enable retired models.
func TestLoad_RejectsUnsupportedModels(t *testing.T) {
	for _, model := range []string{"tiny", "base", "small", "medium", "large-v3", "large-v3-turbo", "not-a-real-model"} {
		t.Run(model, func(t *testing.T) {
			t.Setenv("HELIOS_TRANSCRIBER_DEFAULT_MODEL", model)
			t.Setenv("HELIOS_TRANSCRIBER_ALLOWED_MODELS", model)
			_, err := Load()
			require.ErrorContains(t, err, "unsupported")
		})
	}
	t.Run("retired model alongside supported default", func(t *testing.T) {
		t.Setenv("HELIOS_TRANSCRIBER_DEFAULT_MODEL", SupportedModel)
		t.Setenv("HELIOS_TRANSCRIBER_ALLOWED_MODELS", SupportedModel+",base")
		_, err := Load()
		require.ErrorContains(t, err, "unsupported")
	})
}

func TestValidate_TimeoutsMustLeaveHeadroomUnderJobTimeout(t *testing.T) {
	t.Run("sum equal to job timeout is rejected", func(t *testing.T) {
		cfg := validConfig()
		cfg.Audio.Timeout = 20 * time.Minute
		cfg.Transcriber.Timeout = 25 * time.Minute
		cfg.Worker.JobTimeout = 45 * time.Minute
		assert.Error(t, cfg.Validate())
	})

	t.Run("sum greater than job timeout is rejected", func(t *testing.T) {
		cfg := validConfig()
		cfg.Audio.Timeout = 30 * time.Minute
		cfg.Transcriber.Timeout = 30 * time.Minute
		cfg.Worker.JobTimeout = 45 * time.Minute
		assert.Error(t, cfg.Validate())
	})

	t.Run("sum strictly less than job timeout is accepted", func(t *testing.T) {
		cfg := validConfig()
		cfg.Audio.Timeout = 10 * time.Minute
		cfg.Transcriber.Timeout = 30 * time.Minute
		cfg.Worker.JobTimeout = 45 * time.Minute
		assert.NoError(t, cfg.Validate())
	})
}

func TestValidate_WorkerConcurrencyMustBeAtLeastOne(t *testing.T) {
	for _, n := range []int{0, -1} {
		cfg := validConfig()
		cfg.Worker.Concurrency = n
		assert.Error(t, cfg.Validate(), "Concurrency=%d must be rejected", n)
	}
}

func TestValidate_WorkerMaxAttemptsMustBeAtLeastOne(t *testing.T) {
	for _, n := range []int{0, -1} {
		cfg := validConfig()
		cfg.Worker.MaxAttempts = n
		assert.Error(t, cfg.Validate(), "MaxAttempts=%d must be rejected", n)
	}
}

func TestValidate_HeartbeatIntervalMustBeLessThanLeaseDuration(t *testing.T) {
	t.Run("equal is rejected", func(t *testing.T) {
		cfg := validConfig()
		cfg.Worker.HeartbeatInterval = cfg.Worker.LeaseDuration
		assert.Error(t, cfg.Validate())
	})

	t.Run("greater is rejected", func(t *testing.T) {
		cfg := validConfig()
		cfg.Worker.HeartbeatInterval = cfg.Worker.LeaseDuration + time.Second
		assert.Error(t, cfg.Validate())
	})

	t.Run("strictly less is accepted", func(t *testing.T) {
		cfg := validConfig()
		cfg.Worker.LeaseDuration = time.Minute
		cfg.Worker.HeartbeatInterval = 15 * time.Second
		assert.NoError(t, cfg.Validate())
	})
}

func TestValidate_TracingRequiresOTLPEndpoint(t *testing.T) {
	t.Run("enabled without endpoint is rejected", func(t *testing.T) {
		cfg := validConfig()
		cfg.Observability.TracingEnabled = true
		cfg.Observability.OTLPEndpoint = ""
		assert.Error(t, cfg.Validate())
	})

	t.Run("enabled with endpoint is accepted", func(t *testing.T) {
		cfg := validConfig()
		cfg.Observability.TracingEnabled = true
		cfg.Observability.OTLPEndpoint = "otel-collector:4317"
		assert.NoError(t, cfg.Validate())
	})

	t.Run("disabled without endpoint is accepted", func(t *testing.T) {
		cfg := validConfig()
		cfg.Observability.TracingEnabled = false
		cfg.Observability.OTLPEndpoint = ""
		assert.NoError(t, cfg.Validate())
	})
}

func TestValidate_CORSAllowedOrigins(t *testing.T) {
	t.Run("empty (disabled) is accepted", func(t *testing.T) {
		cfg := validConfig()
		cfg.CORS.AllowedOrigins = nil
		assert.NoError(t, cfg.Validate())
	})

	t.Run("a well-formed origin is accepted", func(t *testing.T) {
		cfg := validConfig()
		cfg.CORS.AllowedOrigins = []string{"https://ats.example.com", "http://localhost:5173"}
		assert.NoError(t, cfg.Validate())
	})

	t.Run("wildcard is rejected", func(t *testing.T) {
		cfg := validConfig()
		cfg.CORS.AllowedOrigins = []string{"*"}
		assert.Error(t, cfg.Validate())
	})

	t.Run("a scheme other than http/https is rejected", func(t *testing.T) {
		cfg := validConfig()
		cfg.CORS.AllowedOrigins = []string{"ftp://example.com"}
		assert.Error(t, cfg.Validate())
	})

	t.Run("a path is rejected", func(t *testing.T) {
		cfg := validConfig()
		cfg.CORS.AllowedOrigins = []string{"https://example.com/app"}
		assert.Error(t, cfg.Validate())
	})

	t.Run("a bare host with no scheme is rejected", func(t *testing.T) {
		cfg := validConfig()
		cfg.CORS.AllowedOrigins = []string{"example.com"}
		assert.Error(t, cfg.Validate())
	})
}

// TestValidate_CORSAllowedOrigins_Normalizes guards against the exact
// mismatch a real deployment hit: a perfectly well-formed origin an
// operator typed with an explicit default port or mixed-case host/scheme
// must still match the lowercase, default-port-stripped form a real
// browser's Origin header always arrives in — see normalizeCORSOrigin's
// own doc. Validate must rewrite c.CORS.AllowedOrigins in place so
// httpapi's exact-match lookup actually sees the normalised form.
func TestValidate_CORSAllowedOrigins_Normalizes(t *testing.T) {
	t.Run("an explicit default port is stripped", func(t *testing.T) {
		cfg := validConfig()
		cfg.CORS.AllowedOrigins = []string{"https://ats.home.lan:443"}
		require.NoError(t, cfg.Validate())
		assert.Equal(t, []string{"https://ats.home.lan"}, cfg.CORS.AllowedOrigins)
	})

	t.Run("an explicit http default port (80) is stripped", func(t *testing.T) {
		cfg := validConfig()
		cfg.CORS.AllowedOrigins = []string{"http://localhost:80"}
		require.NoError(t, cfg.Validate())
		assert.Equal(t, []string{"http://localhost"}, cfg.CORS.AllowedOrigins)
	})

	t.Run("scheme and host are lowercased", func(t *testing.T) {
		cfg := validConfig()
		cfg.CORS.AllowedOrigins = []string{"HTTPS://ATS.home.lan"}
		require.NoError(t, cfg.Validate())
		assert.Equal(t, []string{"https://ats.home.lan"}, cfg.CORS.AllowedOrigins)
	})

	t.Run("an IPv6 literal keeps its brackets", func(t *testing.T) {
		cfg := validConfig()
		cfg.CORS.AllowedOrigins = []string{"http://[::1]:8080", "https://[FE80::1]:443"}
		require.NoError(t, cfg.Validate())
		assert.Equal(t, []string{"http://[::1]:8080", "https://[fe80::1]"}, cfg.CORS.AllowedOrigins)
		// Idempotent: a second pass must not mangle the normalised form.
		require.NoError(t, cfg.Validate())
		assert.Equal(t, []string{"http://[::1]:8080", "https://[fe80::1]"}, cfg.CORS.AllowedOrigins)
	})

	t.Run("a zero-padded default port is still stripped", func(t *testing.T) {
		cfg := validConfig()
		cfg.CORS.AllowedOrigins = []string{"https://ats.home.lan:0443"}
		require.NoError(t, cfg.Validate())
		assert.Equal(t, []string{"https://ats.home.lan"}, cfg.CORS.AllowedOrigins)
	})

	t.Run("a non-default port is preserved", func(t *testing.T) {
		cfg := validConfig()
		cfg.CORS.AllowedOrigins = []string{"https://example.com:8443"}
		require.NoError(t, cfg.Validate())
		assert.Equal(t, []string{"https://example.com:8443"}, cfg.CORS.AllowedOrigins)
	})

	t.Run("an http origin's non-default port is preserved", func(t *testing.T) {
		cfg := validConfig()
		cfg.CORS.AllowedOrigins = []string{"http://localhost:5173"}
		require.NoError(t, cfg.Validate())
		assert.Equal(t, []string{"http://localhost:5173"}, cfg.CORS.AllowedOrigins)
	})

	t.Run("https default port and http default port are not cross-applied", func(t *testing.T) {
		cfg := validConfig()
		cfg.CORS.AllowedOrigins = []string{"https://example.com:80", "http://example.com:443"}
		require.NoError(t, cfg.Validate())
		assert.Equal(t, []string{"https://example.com:80", "http://example.com:443"}, cfg.CORS.AllowedOrigins)
	})

	t.Run("already-normalised origins are left untouched", func(t *testing.T) {
		cfg := validConfig()
		cfg.CORS.AllowedOrigins = []string{"https://ats.example.com", "http://localhost:5173"}
		require.NoError(t, cfg.Validate())
		assert.Equal(t, []string{"https://ats.example.com", "http://localhost:5173"}, cfg.CORS.AllowedOrigins)
	})
}

// TestLoad_AllDefaults sets only the one variable with no shippable
// default (the database DSN has a working local default, so nothing is
// strictly required, but every other variable is asserted unset here) and
// checks every default from the contract's table lands on the right
// field.
func TestLoad_AllDefaults(t *testing.T) {
	cfg, err := Load()
	require.NoError(t, err)

	assert.Equal(t, ":7462", cfg.HTTP.Addr)
	assert.Equal(t, 5*time.Minute, cfg.HTTP.ReadTimeout)
	assert.Equal(t, 60*time.Second, cfg.HTTP.WriteTimeout)
	assert.Equal(t, 120*time.Second, cfg.HTTP.IdleTimeout)
	assert.Equal(t, 20*time.Second, cfg.HTTP.ShutdownTimeout)
	assert.Equal(t, int64(2147483648), cfg.HTTP.MaxUploadBytes)

	assert.Equal(t, "postgres://helios:helios@localhost:5432/helios_ats?sslmode=disable", cfg.Database.DSN)
	assert.Equal(t, 10*time.Second, cfg.Database.ConnectTimeout)
	assert.Equal(t, 10, cfg.Database.MaxConns)
	assert.True(t, cfg.Database.AutoMigrate)

	assert.Equal(t, "/data", cfg.Storage.RootDir)
	assert.Equal(t, int64(1073741824), cfg.Storage.MinFreeBytes)

	assert.Equal(t, "whispercpp", cfg.Transcriber.Kind)
	assert.Equal(t, "whisper-cli", cfg.Transcriber.Binary)
	assert.Equal(t, "/models", cfg.Transcriber.ModelDir)
	assert.Equal(t, SupportedModel, cfg.Transcriber.DefaultModel)
	assert.Equal(t, []string{SupportedModel}, cfg.Transcriber.AllowedModels)
	assert.Equal(t, 4, cfg.Transcriber.Threads)
	assert.False(t, cfg.Transcriber.NoGPU)
	assert.Equal(t, 0, cfg.Transcriber.GPUDevice)
	assert.Equal(t, 30*time.Minute, cfg.Transcriber.Timeout)

	assert.Equal(t, "ffmpeg", cfg.Audio.FFmpegBinary)
	assert.Equal(t, "ffprobe", cfg.Audio.FFprobeBinary)
	assert.Equal(t, 4*time.Hour, cfg.Audio.MaxDuration)
	assert.Equal(t, 10*time.Minute, cfg.Audio.Timeout)

	assert.Equal(t, 2, cfg.Worker.Concurrency)
	assert.Equal(t, 2*time.Second, cfg.Worker.PollInterval)
	assert.Equal(t, time.Minute, cfg.Worker.LeaseDuration)
	assert.Equal(t, 15*time.Second, cfg.Worker.HeartbeatInterval)
	assert.Equal(t, 45*time.Minute, cfg.Worker.JobTimeout)
	assert.Equal(t, 3, cfg.Worker.MaxAttempts)
	assert.Equal(t, 10*time.Second, cfg.Worker.BackoffBase)
	assert.Equal(t, 10*time.Minute, cfg.Worker.BackoffMax)

	assert.True(t, cfg.Retention.Enabled)
	assert.Equal(t, 168*time.Hour, cfg.Retention.MaxAge)
	assert.Equal(t, time.Hour, cfg.Retention.Interval)

	assert.Equal(t, "info", cfg.Observability.LogLevel)
	assert.Equal(t, "json", cfg.Observability.LogFormat)
	assert.True(t, cfg.Observability.MetricsEnabled)
	assert.False(t, cfg.Observability.TracingEnabled)
	assert.Equal(t, "", cfg.Observability.OTLPEndpoint)
	assert.Equal(t, "helios-ats", cfg.Observability.ServiceName)

	assert.Empty(t, cfg.CORS.AllowedOrigins)

	// The defaults themselves must satisfy Validate.
	assert.NoError(t, cfg.Validate())
}

func TestLoad_MalformedDuration_NoUnit(t *testing.T) {
	t.Setenv("HELIOS_WORKER_LEASE_DURATION", "60")

	cfg, err := Load()
	assert.Nil(t, cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "HELIOS_WORKER_LEASE_DURATION")
	assert.Contains(t, err.Error(), "60")
}

func TestLoad_MalformedInt(t *testing.T) {
	t.Setenv("HELIOS_WORKER_CONCURRENCY", "four")

	cfg, err := Load()
	assert.Nil(t, cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "HELIOS_WORKER_CONCURRENCY")
	assert.Contains(t, err.Error(), "four")
}

func TestLoad_MalformedInt64(t *testing.T) {
	t.Setenv("HELIOS_STORAGE_MIN_FREE_BYTES", "lots")

	cfg, err := Load()
	assert.Nil(t, cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "HELIOS_STORAGE_MIN_FREE_BYTES")
	assert.Contains(t, err.Error(), "lots")
}

func TestLoad_MalformedBool(t *testing.T) {
	t.Setenv("HELIOS_DATABASE_AUTO_MIGRATE", "sure")

	cfg, err := Load()
	assert.Nil(t, cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "HELIOS_DATABASE_AUTO_MIGRATE")
	assert.Contains(t, err.Error(), "sure")
}

func TestLoad_GPUConfigurationInvalidValues(t *testing.T) {
	for _, tt := range []struct {
		name, envVar, value, wantError string
	}{
		{"malformed no GPU", "HELIOS_TRANSCRIBER_NO_GPU", "maybe", "HELIOS_TRANSCRIBER_NO_GPU"},
		{"malformed device", "HELIOS_TRANSCRIBER_GPU_DEVICE", "first", "HELIOS_TRANSCRIBER_GPU_DEVICE"},
		{"negative device", "HELIOS_TRANSCRIBER_GPU_DEVICE", "-1", "GPU device must be >= 0"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(tt.envVar, tt.value)
			cfg, err := Load()
			assert.Nil(t, cfg)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantError)
		})
	}
}

func TestLoad_SeveralMalformedValuesAllReported(t *testing.T) {
	t.Setenv("HELIOS_WORKER_LEASE_DURATION", "60")
	t.Setenv("HELIOS_WORKER_CONCURRENCY", "four")
	t.Setenv("HELIOS_OBSERVABILITY_METRICS_ENABLED", "maybe")

	cfg, err := Load()
	assert.Nil(t, cfg)
	require.Error(t, err)

	msg := err.Error()
	for _, want := range []string{
		"HELIOS_WORKER_LEASE_DURATION",
		"HELIOS_WORKER_CONCURRENCY",
		"HELIOS_OBSERVABILITY_METRICS_ENABLED",
	} {
		assert.Contains(t, msg, want, "missing report for %s", want)
	}
}

func TestLoad_BadValueIsNotSilentlyReplacedByDefault(t *testing.T) {
	// Guards against a malformed value surfacing as an error rather than
	// silently falling back to the default as if nothing was ever set.
	t.Setenv("HELIOS_WORKER_CONCURRENCY", "four")

	_, err := Load()
	require.Error(t, err)
}

func TestLoad_ValidatesAfterParsing(t *testing.T) {
	// A value that parses fine but violates a Validate rule must still
	// surface as an error from Load, not just from calling Validate by
	// hand.
	t.Setenv("HELIOS_WORKER_LEASE_DURATION", "0s")

	_, err := Load()
	assert.Error(t, err)
}

func TestLoad_AllowedModelsCommaList(t *testing.T) {
	t.Setenv("HELIOS_TRANSCRIBER_ALLOWED_MODELS", " large-v3-turbo-q5_0 ")

	cfg, err := Load()
	require.NoError(t, err)
	assert.Equal(t, []string{SupportedModel}, cfg.Transcriber.AllowedModels)
}

func TestLoad_CORSAllowedOrigins_UnsetDisablesIt(t *testing.T) {
	cfg, err := Load()
	require.NoError(t, err)
	assert.Empty(t, cfg.CORS.AllowedOrigins)
}

func TestLoad_CORSAllowedOrigins_CommaSeparated(t *testing.T) {
	t.Setenv("HELIOS_CORS_ALLOWED_ORIGINS", "https://ats.example.com, http://localhost:5173 ,")

	cfg, err := Load()
	require.NoError(t, err)
	assert.Equal(t, []string{"https://ats.example.com", "http://localhost:5173"}, cfg.CORS.AllowedOrigins)
}

func TestLoad_CORSAllowedOrigins_NormalizesEndToEnd(t *testing.T) {
	t.Setenv("HELIOS_CORS_ALLOWED_ORIGINS", "https://ATS.home.lan:443")

	cfg, err := Load()
	require.NoError(t, err)
	assert.Equal(t, []string{"https://ats.home.lan"}, cfg.CORS.AllowedOrigins)
}

func TestLoad_CORSAllowedOrigins_InvalidOriginFailsValidate(t *testing.T) {
	t.Setenv("HELIOS_CORS_ALLOWED_ORIGINS", "not-a-valid-origin")

	cfg, err := Load()
	assert.Nil(t, cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "CORS")
}

// TestLoad_FullCustomEnvironment sets every documented variable to a
// non-default value and checks Load produces a Config that matches, end to
// end.
func TestLoad_FullCustomEnvironment(t *testing.T) {
	env := map[string]string{
		"HELIOS_HTTP_ADDR":                     ":9090",
		"HELIOS_HTTP_READ_TIMEOUT":             "2m",
		"HELIOS_HTTP_WRITE_TIMEOUT":            "90s",
		"HELIOS_HTTP_IDLE_TIMEOUT":             "30s",
		"HELIOS_HTTP_SHUTDOWN_TIMEOUT":         "5s",
		"HELIOS_HTTP_MAX_UPLOAD_BYTES":         "1000000",
		"HELIOS_DATABASE_DSN":                  "postgres://u:p@db.internal:5432/ats?sslmode=disable",
		"HELIOS_DATABASE_CONNECT_TIMEOUT":      "3s",
		"HELIOS_DATABASE_MAX_CONNS":            "25",
		"HELIOS_DATABASE_AUTO_MIGRATE":         "false",
		"HELIOS_STORAGE_ROOT_DIR":              "/srv/helios-data",
		"HELIOS_STORAGE_MIN_FREE_BYTES":        "5000000",
		"HELIOS_TRANSCRIBER_KIND":              "fake",
		"HELIOS_TRANSCRIBER_BINARY":            "/usr/local/bin/whisper-cli",
		"HELIOS_TRANSCRIBER_MODEL_DIR":         "/srv/models",
		"HELIOS_TRANSCRIBER_DEFAULT_MODEL":     SupportedModel,
		"HELIOS_TRANSCRIBER_ALLOWED_MODELS":    SupportedModel,
		"HELIOS_TRANSCRIBER_THREADS":           "8",
		"HELIOS_TRANSCRIBER_NO_GPU":            "true",
		"HELIOS_TRANSCRIBER_GPU_DEVICE":        "2",
		"HELIOS_TRANSCRIBER_TIMEOUT":           "5m",
		"HELIOS_AUDIO_FFMPEG_BINARY":           "/usr/local/bin/ffmpeg",
		"HELIOS_AUDIO_FFPROBE_BINARY":          "/usr/local/bin/ffprobe",
		"HELIOS_AUDIO_MAX_DURATION":            "1h",
		"HELIOS_AUDIO_TIMEOUT":                 "2m",
		"HELIOS_WORKER_CONCURRENCY":            "6",
		"HELIOS_WORKER_POLL_INTERVAL":          "500ms",
		"HELIOS_WORKER_LEASE_DURATION":         "2m",
		"HELIOS_WORKER_HEARTBEAT_INTERVAL":     "20s",
		"HELIOS_WORKER_JOB_TIMEOUT":            "20m",
		"HELIOS_WORKER_MAX_ATTEMPTS":           "5",
		"HELIOS_WORKER_BACKOFF_BASE":           "1s",
		"HELIOS_WORKER_BACKOFF_MAX":            "1m",
		"HELIOS_RETENTION_ENABLED":             "false",
		"HELIOS_RETENTION_MAX_AGE":             "24h",
		"HELIOS_RETENTION_INTERVAL":            "10m",
		"HELIOS_OBSERVABILITY_LOG_LEVEL":       "debug",
		"HELIOS_OBSERVABILITY_LOG_FORMAT":      "text",
		"HELIOS_OBSERVABILITY_METRICS_ENABLED": "false",
		"HELIOS_OBSERVABILITY_TRACING_ENABLED": "true",
		"HELIOS_OBSERVABILITY_OTLP_ENDPOINT":   "otel-collector:4317",
		"HELIOS_OBSERVABILITY_SERVICE_NAME":    "helios-ats-staging",
		"HELIOS_CORS_ALLOWED_ORIGINS":          "https://ats-cors.example.com,http://localhost:5173",
	}
	for k, v := range env {
		t.Setenv(k, v)
	}

	cfg, err := Load()
	require.NoError(t, err)

	assert.Equal(t, HTTPConfig{
		Addr:            ":9090",
		ReadTimeout:     2 * time.Minute,
		WriteTimeout:    90 * time.Second,
		IdleTimeout:     30 * time.Second,
		ShutdownTimeout: 5 * time.Second,
		MaxUploadBytes:  1000000,
	}, cfg.HTTP)

	assert.Equal(t, DatabaseConfig{
		DSN:            "postgres://u:p@db.internal:5432/ats?sslmode=disable",
		ConnectTimeout: 3 * time.Second,
		MaxConns:       25,
		AutoMigrate:    false,
	}, cfg.Database)

	assert.Equal(t, StorageConfig{
		RootDir:      "/srv/helios-data",
		MinFreeBytes: 5000000,
	}, cfg.Storage)

	assert.Equal(t, TranscriberConfig{
		Kind:          "fake",
		Binary:        "/usr/local/bin/whisper-cli",
		ModelDir:      "/srv/models",
		DefaultModel:  SupportedModel,
		AllowedModels: []string{SupportedModel},
		Threads:       8,
		NoGPU:         true,
		GPUDevice:     2,
		Timeout:       5 * time.Minute,
	}, cfg.Transcriber)

	assert.Equal(t, AudioConfig{
		FFmpegBinary:  "/usr/local/bin/ffmpeg",
		FFprobeBinary: "/usr/local/bin/ffprobe",
		MaxDuration:   time.Hour,
		Timeout:       2 * time.Minute,
	}, cfg.Audio)

	assert.Equal(t, WorkerConfig{
		Concurrency:       6,
		PollInterval:      500 * time.Millisecond,
		LeaseDuration:     2 * time.Minute,
		HeartbeatInterval: 20 * time.Second,
		JobTimeout:        20 * time.Minute,
		MaxAttempts:       5,
		BackoffBase:       time.Second,
		BackoffMax:        time.Minute,
	}, cfg.Worker)

	assert.Equal(t, RetentionConfig{
		Enabled:  false,
		MaxAge:   24 * time.Hour,
		Interval: 10 * time.Minute,
	}, cfg.Retention)

	assert.Equal(t, ObservabilityConfig{
		LogLevel:       "debug",
		LogFormat:      "text",
		MetricsEnabled: false,
		TracingEnabled: true,
		OTLPEndpoint:   "otel-collector:4317",
		ServiceName:    "helios-ats-staging",
	}, cfg.Observability)

	assert.Equal(t, []string{"https://ats-cors.example.com", "http://localhost:5173"}, cfg.CORS.AllowedOrigins)

	assert.NoError(t, cfg.Validate())
}
