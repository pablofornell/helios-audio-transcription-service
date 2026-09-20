// Package observability provides Helios's logging (slog), metrics
// (Prometheus), health check aggregation and tracing (OpenTelemetry,
// opt-in) primitives. It is a leaf package: it imports nothing else under
// internal/, so every other package can depend on it without creating an
// import cycle.
package observability

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"regexp"
	"strings"
)

// redactedKeys lists the (lower-cased) attribute key substrings that are
// redacted outright wherever they appear as a key, at any group nesting
// depth. This is a backstop, not a fence: it only ever sees a key slog's
// own machinery hands it, and a handler's struct/error encoder can render
// a secret into a value under a key that never makes this list (e.g.
// slog.Any("database", cfg.Database) renders a DSN under the key
// "database", or a wrapped error's message embeds a DSN under "error").
// credentialURLPattern below is the second line of defence for exactly
// that: it scrubs any string-shaped value (including the log message
// itself), regardless of key.
var redactedKeys = []string{
	"dsn",
	"password",
	"token",
	"cookie",
	"secret",
	"credential",
	"api_key",
	"auth",
	"conn",
	"url",
}

// redactedValue is substituted for any attribute value whose key matches
// redactedKeys.
const redactedValue = "[REDACTED]"

// credentialURLPattern matches the credential portion of a URL of the shape
// "scheme://user:password@host" (a Postgres DSN, an S3 endpoint, ...).
// scrubSecretURLs uses it to blank out just the password, wherever such a
// URL appears inside a string — a rendered struct, an error's message, or
// the log message itself — regardless of which attribute key, if any, it is
// filed under. This matters here specifically because
// HELIOS_DATABASE_DSN carries a password.
var credentialURLPattern = regexp.MustCompile(`([A-Za-z][A-Za-z0-9+.-]*://[^\s/:@]+):([^\s/@]+)@`)

// scrubSecretURLs returns s with every embedded "scheme://user:password@"
// credential reduced to "scheme://user:REDACTED@". It is cheap (a single
// compiled regexp pass) and safe to run over every string-shaped log value,
// not just ones whose key looks secret, which is what catches a DSN
// leaking through an error message, a rendered struct, or the log message
// itself.
func scrubSecretURLs(s string) string {
	return credentialURLPattern.ReplaceAllString(s, "$1:REDACTED@")
}

// Version is the running build's version string, baked into every log line
// NewLogger's logger emits. It defaults to "dev" and is meant to be
// overridden at build time, e.g.
// `go build -ldflags "-X .../internal/observability.Version=$(git describe)"`,
// by cmd/helios-transcription-api.
var Version = "dev"

// isSecretKey reports whether key looks like it holds sensitive data, by
// case-insensitive substring match against redactedKeys.
func isSecretKey(key string) bool {
	lower := strings.ToLower(key)
	for _, k := range redactedKeys {
		if strings.Contains(lower, k) {
			return true
		}
	}
	return false
}

// redactAttr is an slog HandlerOptions.ReplaceAttr function with two layers
// of defence, neither of which is a guarantee on its own:
//
//  1. If a.Key looks like isSecretKey, the whole value is replaced with
//     redactedValue. This is applied at every group nesting level (slog
//     calls ReplaceAttr for each member of a group, though not for the
//     group's own name), so e.g. a "connection" key nested three groups
//     deep is caught the same as a top-level one.
//  2. Regardless of the key, any string-shaped value — including the log
//     message itself, under the "msg" key — is passed through
//     scrubSecretURLs, so a credential embedded in an error's message, a
//     formatted string, or a struct rendered by the handler's own encoder
//     (which never reaches back into ReplaceAttr for the struct's fields)
//     doesn't survive just because its key doesn't look secret.
func redactAttr(groups []string, a slog.Attr) slog.Attr {
	if isSecretKey(a.Key) {
		a.Value = slog.StringValue(redactedValue)
		return a
	}
	a.Value = scrubValue(a.Value)
	return a
}

// scrubValue runs scrubSecretURLs over v's string-shaped rendering and
// returns a replacement slog.Value only if that found something to redact,
// so a value with nothing to scrub keeps its original type (an int stays
// an int, a nested struct stays structured JSON) instead of being flattened
// to a string unconditionally.
func scrubValue(v slog.Value) slog.Value {
	var rendered string
	switch v.Kind() {
	case slog.KindString:
		rendered = v.String()
	default:
		// Covers everything ReplaceAttr can see that isn't already a plain
		// string: a logged struct, an error (slog.Any("error", err)), etc.
		// fmt's default formatting is enough to surface an embedded
		// credential URL verbatim, which is all scrubSecretURLs needs to
		// find it.
		rendered = fmt.Sprintf("%v", v.Any())
	}
	scrubbed := scrubSecretURLs(rendered)
	if scrubbed == rendered {
		return v
	}
	return slog.StringValue(scrubbed)
}

// NewLogger builds the service's root *slog.Logger: a JSON or text handler
// chosen by format, filtered to level, writing to stderr.
//
// Every attribute (and the log message itself) is passed through
// redactAttr, which is a backstop, not a fence: it redacts secret-shaped
// keys (see redactedKeys) and scrubs "scheme://user:password@host"
// credentials out of any string-shaped value it can see. It cannot see
// into a struct's fields once a handler's own encoder has taken over
// rendering it — logging a config struct wholesale can still leak a DSN in
// full, because ReplaceAttr is never called for the struct's individual
// fields; callers that want to log a config summary at startup safely must
// reduce a DSN to a non-secret "host:port/dbname" themselves before it ever
// reaches this logger.
func NewLogger(level, format string) (*slog.Logger, error) {
	return newLogger(os.Stderr, level, format)
}

// newLogger is NewLogger's implementation, parameterised on the output
// writer so tests can capture what gets written instead of stderr.
func newLogger(w io.Writer, level, format string) (*slog.Logger, error) {
	lvl, err := parseLevel(level)
	if err != nil {
		return nil, err
	}

	opts := &slog.HandlerOptions{
		Level:       lvl,
		ReplaceAttr: redactAttr,
	}

	var handler slog.Handler
	switch format {
	case "json", "":
		handler = slog.NewJSONHandler(w, opts)
	case "text":
		handler = slog.NewTextHandler(w, opts)
	default:
		return nil, fmt.Errorf("observability: unknown log format %q", format)
	}

	return slog.New(handler), nil
}

// parseLevel maps level ("debug", "info", "warn", "error") onto a
// slog.Level, defaulting to slog.LevelInfo for an empty string.
func parseLevel(level string) (slog.Level, error) {
	switch strings.ToLower(level) {
	case "debug":
		return slog.LevelDebug, nil
	case "info", "":
		return slog.LevelInfo, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return 0, fmt.Errorf("observability: unknown log level %q", level)
	}
}

// loggerContextKey is the unexported type for the context key under which a
// request-scoped *slog.Logger is stored, so it cannot collide with a key
// from another package.
type loggerContextKey struct{}

// requestIDContextKey is the unexported context key for a request ID
// attached via WithRequestID.
type requestIDContextKey struct{}

// defaultLogger is returned by FromContext when ctx carries none: a working
// handler over os.Stderr rather than slog.Default(), so callers always get
// sane, redacted output instead of relying on whatever the process-wide
// default happens to be.
var defaultLogger = slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{
	ReplaceAttr: redactAttr,
}))

// ContextWithLogger returns a copy of ctx carrying logger, retrievable via
// FromContext.
func ContextWithLogger(ctx context.Context, logger *slog.Logger) context.Context {
	return context.WithValue(ctx, loggerContextKey{}, logger)
}

// FromContext returns the *slog.Logger previously attached with
// ContextWithLogger, or a working default if ctx carries none. It never
// returns nil, so callers can log unconditionally without a nil check.
func FromContext(ctx context.Context) *slog.Logger {
	if logger, ok := ctx.Value(loggerContextKey{}).(*slog.Logger); ok && logger != nil {
		return logger
	}
	return defaultLogger
}

// WithRequestID returns a copy of ctx with id attached, both as a plain
// context value (retrievable independently) and folded into the logger
// FromContext will subsequently return — so every log line emitted via
// FromContext(ctx) after this call carries "request_id" automatically.
func WithRequestID(ctx context.Context, id string) context.Context {
	ctx = context.WithValue(ctx, requestIDContextKey{}, id)
	logger := FromContext(ctx).With(slog.String("request_id", id))
	return ContextWithLogger(ctx, logger)
}

// RequestIDFromContext returns the request ID attached with WithRequestID,
// and whether one was present.
func RequestIDFromContext(ctx context.Context) (string, bool) {
	id, ok := ctx.Value(requestIDContextKey{}).(string)
	return id, ok
}
