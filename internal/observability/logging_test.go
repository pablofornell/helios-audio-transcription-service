package observability

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const secretDSN = "postgres://helios:s3cr3t-password@db:5432/helios_ats?sslmode=disable"

func TestNewLogger_JSONFormat(t *testing.T) {
	var buf bytes.Buffer
	logger, err := newLogger(&buf, "info", "json")
	require.NoError(t, err)

	logger.Info("started")

	var record map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &record))
	assert.Equal(t, "started", record["msg"])
}

func TestNewLogger_TextFormat(t *testing.T) {
	var buf bytes.Buffer
	logger, err := newLogger(&buf, "info", "text")
	require.NoError(t, err)

	logger.Info("started")

	out := buf.String()
	assert.Contains(t, out, "msg=started")
	// Text format is not JSON.
	var record map[string]any
	assert.Error(t, json.Unmarshal(buf.Bytes(), &record))
}

func TestNewLogger_DefaultFormatIsJSON(t *testing.T) {
	var buf bytes.Buffer
	logger, err := newLogger(&buf, "info", "")
	require.NoError(t, err)

	logger.Info("started")

	var record map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &record))
}

func TestNewLogger_UnknownFormat(t *testing.T) {
	_, err := NewLogger("info", "xml")
	assert.Error(t, err)
}

func TestNewLogger_UnknownLevel(t *testing.T) {
	_, err := NewLogger("verbose", "json")
	assert.Error(t, err)
}

func TestNewLogger_LevelFiltering(t *testing.T) {
	var buf bytes.Buffer
	logger, err := newLogger(&buf, "warn", "json")
	require.NoError(t, err)

	logger.Debug("debug line")
	logger.Info("info line")
	assert.Empty(t, buf.String(), "debug/info must be filtered out at warn level")

	logger.Warn("warn line")
	assert.Contains(t, buf.String(), "warn line")

	buf.Reset()
	logger.Error("error line")
	assert.Contains(t, buf.String(), "error line")
}

// TestNewLogger_RedactsSecrets asserts that a secret-shaped attribute value
// (here, a real-looking Postgres DSN with an embedded password — the exact
// shape HELIOS_DATABASE_DSN takes) never appears in the rendered log line,
// whatever key name reaches it.
func TestNewLogger_RedactsSecrets(t *testing.T) {
	cases := []string{"dsn", "DSN", "database_dsn", "password", "auth_token", "Authorization", "cookie", "session_cookie"}

	for _, key := range cases {
		t.Run(key, func(t *testing.T) {
			var buf bytes.Buffer
			logger, err := newLogger(&buf, "info", "json")
			require.NoError(t, err)

			logger.Info("connecting", slog.String(key, secretDSN))

			out := buf.String()
			assert.NotContains(t, out, secretDSN, "secret value leaked for key %q", key)
			assert.NotContains(t, out, "s3cr3t-password")
			assert.Contains(t, out, redactedValue)
		})
	}
}

// TestNewLogger_RedactsSecrets_NestedGroup covers a secret nested inside a
// slog group (the shape a logged config struct would take).
func TestNewLogger_RedactsSecrets_NestedGroup(t *testing.T) {
	var buf bytes.Buffer
	logger, err := newLogger(&buf, "info", "json")
	require.NoError(t, err)

	logger.Info("config",
		slog.Group("database",
			slog.String("dsn", secretDSN),
			slog.Int("max_conns", 10),
		),
	)

	out := buf.String()
	assert.NotContains(t, out, secretDSN)
	assert.Contains(t, out, redactedValue)
	assert.Contains(t, out, "max_conns")
}

// TestNewLogger_RedactsSecrets_InErrorAndMessage covers the two shapes a
// key-substring list alone cannot catch: a wrapped error whose message
// embeds the DSN, and the log message string itself.
func TestNewLogger_RedactsSecrets_InErrorAndMessage(t *testing.T) {
	run := func(name string, log func(l *slog.Logger)) {
		t.Run(name, func(t *testing.T) {
			var buf bytes.Buffer
			logger, err := newLogger(&buf, "info", "json")
			require.NoError(t, err)

			log(logger)

			out := buf.String()
			assert.NotContains(t, out, "s3cr3t-password", "secret leaked for shape %q", name)
		})
	}

	run("slog.Any error wrapping the DSN", func(l *slog.Logger) {
		l.Error("connect failed", slog.Any("error", errors.New("dial "+secretDSN+": refused")))
	})
	run("fmt.Errorf %w wrapping the DSN, logged as \"error\"", func(l *slog.Logger) {
		cause := errors.New("dial " + secretDSN + ": refused")
		l.Error("connect failed", slog.Any("error", fmt.Errorf("connect: %w", cause)))
	})
	run("secret inside msg", func(l *slog.Logger) {
		l.Info("connecting to " + secretDSN)
	})
	run("secret inside a formatted msg", func(l *slog.Logger) {
		l.Warn(fmt.Sprintf("retrying %s after failure", secretDSN))
	})
}

func TestContextLoggerAndRequestID(t *testing.T) {
	// FromContext on a bare context never returns nil and is safe to use.
	ctx := context.Background()
	logger := FromContext(ctx)
	require.NotNil(t, logger)

	var buf bytes.Buffer
	custom := slog.New(slog.NewJSONHandler(&buf, nil))
	ctx = ContextWithLogger(ctx, custom)
	assert.Same(t, custom, FromContext(ctx))

	ctx = WithRequestID(ctx, "req-123")
	id, ok := RequestIDFromContext(ctx)
	assert.True(t, ok)
	assert.Equal(t, "req-123", id)

	FromContext(ctx).Info("handled")
	var record map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &record))
	assert.Equal(t, "req-123", record["request_id"])
}

func TestFromContext_DefaultIsUsable(t *testing.T) {
	// Never nil, and doesn't panic when used — that's the whole contract.
	logger := FromContext(context.Background())
	require.NotNil(t, logger)
	logger.Info("no-op-safe default works")
}

func TestIsSecretKey(t *testing.T) {
	positive := []string{
		"dsn", "DSN", "database_dsn", "password", "user_password", "token", "access_token",
		"cookie", "Set-Cookie", "authorization", "Authorization",
		"secret", "upstream_secret", "credential", "credentials", "api_key", "API_KEY",
		"auth", "auth_token", "db_url", "database_url", "callback_url",
		"conn", "conn_string", "connection", "connection_string",
	}
	for _, k := range positive {
		assert.True(t, isSecretKey(k), "expected %q to be treated as secret", k)
	}

	negative := []string{"service", "version", "request_id", "status", "model"}
	for _, k := range negative {
		assert.False(t, isSecretKey(k), "expected %q to not be treated as secret", k)
	}
}

func TestRedactAttr(t *testing.T) {
	a := redactAttr(nil, slog.String("password", "hunter2"))
	assert.Equal(t, redactedValue, a.Value.String())

	a = redactAttr(nil, slog.String("model", "base"))
	assert.Equal(t, "base", a.Value.String())
}
