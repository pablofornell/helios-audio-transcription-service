// Package config loads and validates Helios's runtime configuration from
// environment variables, all prefixed HELIOS_. It depends on nothing but
// the standard library — there is no config file format and no
// third-party parsing library — and nothing internal, so it can be
// imported from cmd/helios-transcription-api without pulling in anything
// else.
package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// envPrefix is prepended to every environment variable name this package
// reads.
const envPrefix = "HELIOS_"

// SupportedModel is the only model supported for new transcription requests.
const SupportedModel = "large-v3-turbo-q5_0"

// DefaultTranslationModel is the model the service uses to translate audio
// in a third language (not Spanish, not English) to English. The quantized
// turbo model cannot translate: it was not trained for that task. This is
// an internal choice of the service, not a value a caller can pick, so it
// stays outside the AllowedModels list.
const DefaultTranslationModel = "large-v3-q5_0"

// translationModelPattern keeps TranscriberConfig.TranslationModel a safe
// file-name part: the service builds ggml-<model>.bin from it. It matches
// the shape domain.Options.Validate asks of a model name.
var translationModelPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)

// giB is one gibibyte, used to spell out a couple of the byte-count
// defaults below without a long string of digits.
const giB = 1 << 30

// HTTPConfig controls the HTTP server.
type HTTPConfig struct {
	// Addr is the listen address, e.g. ":7462".
	Addr string
	// ReadTimeout bounds how long reading a request may take. Generous by
	// default: uploads are large audio files, not small JSON bodies.
	ReadTimeout time.Duration
	// WriteTimeout bounds how long writing a response may take.
	WriteTimeout time.Duration
	// IdleTimeout bounds how long a keep-alive connection may sit idle.
	IdleTimeout time.Duration
	// ShutdownTimeout bounds how long graceful shutdown waits for
	// in-flight requests to finish before forcing close.
	ShutdownTimeout time.Duration
	// MaxUploadBytes caps a single POST /transcriptions upload.
	MaxUploadBytes int64
}

// DatabaseConfig controls the Postgres connection.
type DatabaseConfig struct {
	// DSN is the Postgres connection string. Required; no default. It must
	// never be logged.
	DSN string
	// ConnectTimeout bounds how long establishing the initial pool
	// connection may take.
	ConnectTimeout time.Duration
	// MaxConns caps the pgxpool connection pool size.
	MaxConns int
	// AutoMigrate, when true, runs pending goose migrations at startup.
	AutoMigrate bool
}

// StorageConfig controls the local filesystem store.
type StorageConfig struct {
	// RootDir is the directory transcription artifacts (sources and
	// normalized WAVs) are written under.
	RootDir string
	// MinFreeBytes is the free-space floor: once free space on RootDir's
	// volume drops below this, uploads are refused with
	// domain.ErrCodeDiskFull.
	MinFreeBytes int64
}

// TranscriberConfig controls which Transcriber implementation is used and
// how whisper-cli is invoked.
type TranscriberConfig struct {
	// Kind selects the implementation: "whispercpp" (default, real
	// transcription via the whisper-cli subprocess) or "fake"
	// (deterministic, no subprocess — for tests and local dev).
	Kind string
	// Binary is the whisper-cli executable name or path, resolved via
	// PATH, used when Kind is "whispercpp".
	Binary string
	// ModelDir holds the ggml-<model>.bin weight files.
	ModelDir string
	// DefaultModel is used when a submission's Options.Model is empty.
	DefaultModel string
	// AllowedModels is the set Options.Model must belong to. Parsed from a
	// comma-separated list; only SupportedModel is supported.
	AllowedModels []string
	// TranslationModel is the model that translates third-language audio to
	// English. It is a fixed internal choice, not a request option. Load
	// fills DefaultTranslationModel. The
	// service does not need its weight file to start or to be ready: only a
	// job with third-language audio needs it, and that job fails with
	// model_unavailable when the file is missing.
	TranslationModel string
	// Threads is the -t value passed to whisper-cli.
	Threads int
	// NoGPU disables GPU acceleration and forces CPU transcription.
	NoGPU bool
	// GPUDevice selects whisper-cli's GPU device index when NoGPU is false.
	GPUDevice int
	// Timeout bounds a single Transcribe call. It, together with
	// Audio.Timeout, must leave headroom under Worker.JobTimeout — see
	// Validate.
	Timeout time.Duration
}

// AudioConfig controls the ffprobe/ffmpeg pipeline that runs before
// transcription.
type AudioConfig struct {
	// FFmpegBinary is the ffmpeg executable name or path, resolved via
	// PATH.
	FFmpegBinary string
	// FFprobeBinary is the ffprobe executable name or path, resolved via
	// PATH.
	FFprobeBinary string
	// MaxDuration rejects any probed audio longer than this, with
	// domain.ErrCodePayloadTooLarge.
	MaxDuration time.Duration
	// Timeout bounds the combined probe+normalize budget for a single job.
	Timeout time.Duration
}

// WorkerConfig controls the in-process job worker pool.
type WorkerConfig struct {
	// Concurrency is how many jobs may run at once per process.
	Concurrency int
	// PollInterval is how often an idle worker pool checks for newly
	// eligible pending jobs.
	PollInterval time.Duration
	// LeaseDuration is how long a claimed job's lease lasts before it is
	// eligible for another worker to reclaim, absent a heartbeat.
	LeaseDuration time.Duration
	// HeartbeatInterval is how often a running worker extends its lease.
	// Must be strictly less than LeaseDuration, or a lease can expire
	// between heartbeats even under normal operation.
	HeartbeatInterval time.Duration
	// JobTimeout bounds a single job's run, end to end (probe + normalize
	// + transcribe).
	JobTimeout time.Duration
	// MaxAttempts caps how many times a job is retried before it is marked
	// permanently failed.
	MaxAttempts int
	// BackoffBase is the initial delay before retrying a failed attempt.
	BackoffBase time.Duration
	// BackoffMax caps the delay between retries.
	BackoffMax time.Duration
}

// RetentionConfig controls the background sweep that purges old, terminal
// jobs — both their database rows and the files they own on disk.
type RetentionConfig struct {
	// Enabled toggles the retention sweep entirely.
	Enabled bool
	// MaxAge is how old a terminal (succeeded/failed/canceled) job must be
	// before the sweep removes it.
	MaxAge time.Duration
	// Interval is how often the retention sweep runs.
	Interval time.Duration
}

// ObservabilityConfig controls logging, metrics and tracing.
type ObservabilityConfig struct {
	// LogLevel is one of "debug", "info", "warn", "error".
	LogLevel string
	// LogFormat is one of "json" or "text".
	LogFormat string
	// MetricsEnabled toggles the Prometheus /metrics endpoint.
	MetricsEnabled bool
	// TracingEnabled toggles OpenTelemetry tracing. Off by default: no
	// collector is required to run Helios.
	TracingEnabled bool
	// OTLPEndpoint is the OTLP exporter target, required when
	// TracingEnabled is true.
	OTLPEndpoint string
	// ServiceName identifies this service in logs, metrics and traces.
	ServiceName string
}

// CORSConfig controls whether internal/httpapi answers cross-origin
// requests at all. Every deployment shipped before this existed assumed a
// same-origin browser client (the desktop app's web build served, and
// proxied to, from one origin — see deploy's Caddy/nginx config) or a
// non-browser caller CORS never applies to in the first place; this exists
// solely for the case that assumption no longer holds, e.g. that same web
// build pointed at a *different* HELIOS_HTTP_ADDR via its own Settings →
// Server (desktop/frontend/src/lib/browser's server setting).
type CORSConfig struct {
	// AllowedOrigins is the exact set of origins (scheme://host[:port], no
	// path — e.g. "https://ats.example.com") a cross-origin browser
	// request's Origin header is checked against. Empty (the default)
	// disables CORS entirely: no Origin header is ever inspected and no
	// Access-Control-* header is ever written, exactly as if this config
	// did not exist — see httpapi's CORS middleware doc. There is no
	// wildcard "*" support: an allowlist of exact origins is the only mode
	// this accepts, matching the closed set of browser deployments Helios
	// actually expects (unlike a public API, this is not meant to be
	// embedded from arbitrary third-party origins).
	//
	// Every entry here is lowercased and stripped of a default port
	// (":443" for https, ":80" for http) by Validate — see
	// normalizeCORSOrigin's own doc for why: a real browser's Origin
	// header is always in that exact form, and httpapi's CORS middleware
	// matches against it with a plain, case-sensitive exact-match lookup.
	// Read this field only after Validate has run (Load always runs it;
	// anything else constructing a Config directly must call it too) —
	// before that, an operator-typed "https://HOST:443" is still here
	// verbatim, not yet the "https://host" it is equivalent to.
	AllowedOrigins []string
}

// Config is the fully assembled, validated runtime configuration.
type Config struct {
	HTTP          HTTPConfig
	Database      DatabaseConfig
	Storage       StorageConfig
	Transcriber   TranscriberConfig
	Audio         AudioConfig
	Worker        WorkerConfig
	Retention     RetentionConfig
	Observability ObservabilityConfig
	CORS          CORSConfig
}

// Load reads Config from the environment (every variable prefixed
// HELIOS_), applies documented defaults for anything unset, and calls
// Validate before returning. An unset variable always takes its default; a
// variable that is present but fails to parse (e.g.
// HELIOS_WORKER_CONCURRENCY=four) is collected as an error, and Load
// returns every such error at once (via errors.Join) rather than the first
// one it hits, so an operator sees every mistake in a single run instead
// of one per restart. It never logs or returns the DSN in an error
// message.
func Load() (*Config, error) {
	l := &loader{}
	cfg := &Config{
		HTTP: HTTPConfig{
			Addr:            l.getString("HTTP_ADDR", ":7462"),
			ReadTimeout:     l.getDuration("HTTP_READ_TIMEOUT", 5*time.Minute),
			WriteTimeout:    l.getDuration("HTTP_WRITE_TIMEOUT", 60*time.Second),
			IdleTimeout:     l.getDuration("HTTP_IDLE_TIMEOUT", 120*time.Second),
			ShutdownTimeout: l.getDuration("HTTP_SHUTDOWN_TIMEOUT", 20*time.Second),
			MaxUploadBytes:  l.getInt64("HTTP_MAX_UPLOAD_BYTES", 2*giB),
		},
		Database: DatabaseConfig{
			DSN:            l.getString("DATABASE_DSN", "postgres://helios:helios@localhost:5432/helios_ats?sslmode=disable"),
			ConnectTimeout: l.getDuration("DATABASE_CONNECT_TIMEOUT", 10*time.Second),
			MaxConns:       l.getInt("DATABASE_MAX_CONNS", 10),
			AutoMigrate:    l.getBool("DATABASE_AUTO_MIGRATE", true),
		},
		Storage: StorageConfig{
			RootDir:      l.getString("STORAGE_ROOT_DIR", "/data"),
			MinFreeBytes: l.getInt64("STORAGE_MIN_FREE_BYTES", giB),
		},
		Transcriber: TranscriberConfig{
			Kind:             l.getString("TRANSCRIBER_KIND", "whispercpp"),
			Binary:           l.getString("TRANSCRIBER_BINARY", "whisper-cli"),
			ModelDir:         l.getString("TRANSCRIBER_MODEL_DIR", "/models"),
			DefaultModel:     l.getString("TRANSCRIBER_DEFAULT_MODEL", SupportedModel),
			AllowedModels:    l.getStringSlice("TRANSCRIBER_ALLOWED_MODELS", []string{SupportedModel}),
			TranslationModel: l.getString("TRANSCRIBER_TRANSLATION_MODEL", DefaultTranslationModel),
			Threads:          l.getInt("TRANSCRIBER_THREADS", 4),
			NoGPU:            l.getBool("TRANSCRIBER_NO_GPU", false),
			GPUDevice:        l.getInt("TRANSCRIBER_GPU_DEVICE", 0),
			Timeout:          l.getDuration("TRANSCRIBER_TIMEOUT", 30*time.Minute),
		},
		Audio: AudioConfig{
			FFmpegBinary:  l.getString("AUDIO_FFMPEG_BINARY", "ffmpeg"),
			FFprobeBinary: l.getString("AUDIO_FFPROBE_BINARY", "ffprobe"),
			MaxDuration:   l.getDuration("AUDIO_MAX_DURATION", 4*time.Hour),
			Timeout:       l.getDuration("AUDIO_TIMEOUT", 10*time.Minute),
		},
		Worker: WorkerConfig{
			Concurrency:       l.getInt("WORKER_CONCURRENCY", 2),
			PollInterval:      l.getDuration("WORKER_POLL_INTERVAL", 2*time.Second),
			LeaseDuration:     l.getDuration("WORKER_LEASE_DURATION", time.Minute),
			HeartbeatInterval: l.getDuration("WORKER_HEARTBEAT_INTERVAL", 15*time.Second),
			JobTimeout:        l.getDuration("WORKER_JOB_TIMEOUT", 45*time.Minute),
			MaxAttempts:       l.getInt("WORKER_MAX_ATTEMPTS", 3),
			BackoffBase:       l.getDuration("WORKER_BACKOFF_BASE", 10*time.Second),
			BackoffMax:        l.getDuration("WORKER_BACKOFF_MAX", 10*time.Minute),
		},
		Retention: RetentionConfig{
			Enabled: l.getBool("RETENTION_ENABLED", true),
			// 168h = 7 days.
			MaxAge:   l.getDuration("RETENTION_MAX_AGE", 168*time.Hour),
			Interval: l.getDuration("RETENTION_INTERVAL", time.Hour),
		},
		Observability: ObservabilityConfig{
			LogLevel:       l.getString("OBSERVABILITY_LOG_LEVEL", "info"),
			LogFormat:      l.getString("OBSERVABILITY_LOG_FORMAT", "json"),
			MetricsEnabled: l.getBool("OBSERVABILITY_METRICS_ENABLED", true),
			TracingEnabled: l.getBool("OBSERVABILITY_TRACING_ENABLED", false),
			OTLPEndpoint:   l.getString("OBSERVABILITY_OTLP_ENDPOINT", ""),
			ServiceName:    l.getString("OBSERVABILITY_SERVICE_NAME", "helios-ats"),
		},
		CORS: CORSConfig{
			AllowedOrigins: l.getStringSlice("CORS_ALLOWED_ORIGINS", nil),
		},
	}

	if len(l.errs) > 0 {
		return nil, errors.Join(l.errs...)
	}

	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// durationField pairs a Config time.Duration knob with the env var name an
// operator would recognise, for validateDurations' table.
type durationField struct {
	envVar string
	value  time.Duration
}

// validateDurations returns every Config time.Duration knob (that this
// package parses via getDuration) paired with its env var name.
// Validate requires every one of them to be strictly positive: a zero or
// negative WORKER_HEARTBEAT_INTERVAL, WORKER_POLL_INTERVAL or
// RETENTION_INTERVAL reaches its respective background loop's
// time.NewTicker, which panics on a non-positive duration — an unrecovered
// panic that crashes the whole process on the first heartbeat/poll/sweep
// tick, well after a clean startup and a green /readyz (review finding).
// The same non-positive value is nonsensical for every other duration knob
// too (a zero timeout is not "no timeout", it is "fails immediately"), so
// the rule is applied uniformly rather than only to the three knobs that
// happen to reach a ticker.
func (c *Config) validateDurations() []durationField {
	return []durationField{
		{"HTTP_READ_TIMEOUT", c.HTTP.ReadTimeout},
		{"HTTP_WRITE_TIMEOUT", c.HTTP.WriteTimeout},
		{"HTTP_IDLE_TIMEOUT", c.HTTP.IdleTimeout},
		{"HTTP_SHUTDOWN_TIMEOUT", c.HTTP.ShutdownTimeout},
		{"DATABASE_CONNECT_TIMEOUT", c.Database.ConnectTimeout},
		{"TRANSCRIBER_TIMEOUT", c.Transcriber.Timeout},
		{"AUDIO_MAX_DURATION", c.Audio.MaxDuration},
		{"AUDIO_TIMEOUT", c.Audio.Timeout},
		{"WORKER_POLL_INTERVAL", c.Worker.PollInterval},
		{"WORKER_LEASE_DURATION", c.Worker.LeaseDuration},
		{"WORKER_HEARTBEAT_INTERVAL", c.Worker.HeartbeatInterval},
		{"WORKER_JOB_TIMEOUT", c.Worker.JobTimeout},
		{"WORKER_BACKOFF_BASE", c.Worker.BackoffBase},
		{"WORKER_BACKOFF_MAX", c.Worker.BackoffMax},
		{"RETENTION_MAX_AGE", c.Retention.MaxAge},
		{"RETENTION_INTERVAL", c.Retention.Interval},
	}
}

// Validate rejects a Config that would misbehave at runtime: an empty
// Database.DSN, a non-positive Database.MaxConns, a non-positive
// HTTP.MaxUploadBytes, a negative Storage.MinFreeBytes, an empty
// Storage.RootDir, an unrecognised Transcriber.Kind, an empty
// Transcriber.AllowedModels or a Transcriber.DefaultModel absent from it,
// any non-positive time.Duration knob (see validateDurations), a combined
// Audio.Timeout+Transcriber.Timeout that does not leave Worker.JobTimeout
// strictly ahead of it, Worker.Concurrency or Worker.MaxAttempts below 1, a
// Worker.HeartbeatInterval that is not strictly less than
// Worker.LeaseDuration, TracingEnabled without an OTLPEndpoint, and a
// malformed CORS.AllowedOrigins entry (normalizeCORSOrigin's own doc).
// That last check is the one exception to "Validate only ever reads c": a
// well-formed origin is rewritten in place
// (c.CORS.AllowedOrigins[i] = normalizeCORSOrigin(...)) rather than merely
// accepted as-is, so every Config that has passed Validate — whether via
// Load or constructed directly, e.g. in a test — carries the exact form
// httpapi's CORS middleware needs to match a real browser's Origin header.
func (c *Config) Validate() error {
	if c.Database.DSN == "" {
		return fmt.Errorf("config: database DSN is required")
	}
	if c.Database.MaxConns <= 0 {
		return fmt.Errorf("config: database max conns must be > 0, got %d", c.Database.MaxConns)
	}
	if c.HTTP.MaxUploadBytes <= 0 {
		return fmt.Errorf("config: http max upload bytes must be > 0, got %d", c.HTTP.MaxUploadBytes)
	}
	if c.Storage.RootDir == "" {
		return fmt.Errorf("config: storage root dir is required")
	}
	if c.Storage.MinFreeBytes < 0 {
		// Rejecting outright, rather than silently clamping to 0, matters:
		// clamping would disable the free-space floor entirely for a
		// misconfigured (e.g. accidentally negative) value instead of
		// telling the operator their configuration is wrong.
		return fmt.Errorf("config: storage min free bytes must be >= 0, got %d", c.Storage.MinFreeBytes)
	}
	for _, f := range c.validateDurations() {
		if f.value <= 0 {
			return fmt.Errorf("config: %s%s must be > 0, got %s", envPrefix, f.envVar, f.value)
		}
	}
	switch c.Transcriber.Kind {
	case "whispercpp", "fake":
	default:
		return fmt.Errorf("config: unknown transcriber kind %q", c.Transcriber.Kind)
	}
	if c.Transcriber.GPUDevice < 0 {
		return fmt.Errorf("config: transcriber GPU device must be >= 0, got %d", c.Transcriber.GPUDevice)
	}
	if len(c.Transcriber.AllowedModels) == 0 {
		// An empty allow-list does not mean "any model is fine": it made
		// both this check and Options.Validate's model check no-ops,
		// letting an unvalidated model name reach
		// filepath.Join(ModelDir, "ggml-"+model+".bin") and escape ModelDir
		// (review finding; see domain.Options.Validate and
		// whispercpp.modelPath).
		return fmt.Errorf("config: transcriber allowed models must not be empty")
	}
	for _, model := range c.Transcriber.AllowedModels {
		if model != SupportedModel {
			return fmt.Errorf("config: transcriber model %q is unsupported; only %q is supported", model, SupportedModel)
		}
	}
	if !contains(c.Transcriber.AllowedModels, c.Transcriber.DefaultModel) {
		return fmt.Errorf("config: transcriber default model %q must be one of the allowed models %v",
			c.Transcriber.DefaultModel, c.Transcriber.AllowedModels)
	}
	// Empty is allowed: a Config built by hand (not by Load) then gets the
	// transcriber's own default.
	if c.Transcriber.TranslationModel != "" && !translationModelPattern.MatchString(c.Transcriber.TranslationModel) {
		return fmt.Errorf("config: transcriber translation model %q must be a model name (lowercase letters, digits, '.', '_' or '-')",
			c.Transcriber.TranslationModel)
	}
	if c.Audio.Timeout+c.Transcriber.Timeout >= c.Worker.JobTimeout {
		return fmt.Errorf("config: audio timeout (%s) + transcriber timeout (%s) must be less than worker job timeout (%s)",
			c.Audio.Timeout, c.Transcriber.Timeout, c.Worker.JobTimeout)
	}
	if c.Worker.Concurrency < 1 {
		return fmt.Errorf("config: worker concurrency must be >= 1, got %d", c.Worker.Concurrency)
	}
	if c.Worker.MaxAttempts < 1 {
		return fmt.Errorf("config: worker max attempts must be >= 1, got %d", c.Worker.MaxAttempts)
	}
	if c.Worker.HeartbeatInterval >= c.Worker.LeaseDuration {
		return fmt.Errorf("config: worker heartbeat interval (%s) must be less than worker lease duration (%s)",
			c.Worker.HeartbeatInterval, c.Worker.LeaseDuration)
	}
	if c.Observability.TracingEnabled && c.Observability.OTLPEndpoint == "" {
		return fmt.Errorf("config: observability OTLP endpoint is required when tracing is enabled")
	}
	// Validated *and* normalised in place — see normalizeCORSOrigin's own
	// doc for why storing the normalised form back onto c matters: it is
	// what makes httpapi's CORS middleware's exact-match lookup actually
	// match a real browser's Origin header, not just what makes this
	// config well-formed.
	for i, origin := range c.CORS.AllowedOrigins {
		normalized, err := normalizeCORSOrigin(origin)
		if err != nil {
			return fmt.Errorf("config: CORS allowed origin %q is invalid: %w", origin, err)
		}
		c.CORS.AllowedOrigins[i] = normalized
	}
	return nil
}

// normalizeCORSOrigin rejects anything that is not a bare
// "scheme://host[:port]" origin — a wildcard "*" (CORSConfig.AllowedOrigins'
// own doc says why this package never supports one), a scheme other than
// http/https, a missing host, or anything carrying a path/query/fragment
// beyond the origin itself (an origin, unlike a URL, has none of those —
// accepting one here would silently be compared against an Origin header
// that can never contain it either, so it could never match anything) —
// and otherwise returns it lowercased (scheme and host both; a browser's
// own Origin header is always lowercase there) with a default port
// stripped (":443" for https, ":80" for http; a browser's own Origin
// header never carries one). Both normalisations exist for exactly one
// reason: httpapi's CORS middleware compares a configured origin against
// an incoming Origin header with a bare `map[string]bool` exact-match
// lookup, not a case-insensitive or scheme-aware one — an operator typing
// "https://HOST:443" (both perfectly valid, equivalent ways to spell the
// same origin) must not silently never match the "https://host" a real
// browser actually sends.
func normalizeCORSOrigin(origin string) (string, error) {
	if origin == "*" {
		return "", fmt.Errorf(`wildcard "*" is not supported — list exact origins instead`)
	}
	u, err := url.Parse(origin)
	if err != nil {
		return "", fmt.Errorf("not a valid URL: %w", err)
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return "", fmt.Errorf("must use http or https, got %q", u.Scheme)
	}
	if u.Host == "" {
		return "", fmt.Errorf("missing host")
	}
	if u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		return "", fmt.Errorf("must be a bare origin (scheme://host[:port]), not a path, query or fragment")
	}

	host := strings.ToLower(u.Hostname())
	port := u.Port()
	if port != "" {
		// Browsers serialise the port as a plain decimal, so "0443" must
		// become "443" before the default-port check below can see it.
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return "", fmt.Errorf("invalid port %q", port)
		}
		port = strconv.Itoa(n)
	}
	if (scheme == "https" && port == "443") || (scheme == "http" && port == "80") {
		port = ""
	}
	// u.Hostname() drops an IPv6 literal's brackets; a browser's Origin
	// header keeps them, so put them back or the entry can never match.
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	if port == "" {
		return scheme + "://" + host, nil
	}
	return scheme + "://" + host + ":" + port, nil
}

// contains reports whether s holds v. A tiny local helper rather than
// slices.Contains's import just for this one call site, to keep this
// package's surface exactly what BLUEPRINT's does: stdlib only, nothing
// beyond os/strconv/strings/time.
func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

// loader reads and parses HELIOS_-prefixed environment variables,
// collecting every parse failure it encounters in errs instead of
// returning on the first one. An unset variable always yields its default
// and is never an error; a present-but-unparsable value yields the default
// too (so the rest of Load can proceed and report every mistake), but is
// recorded in errs, which Load joins and returns instead of the parsed
// Config.
type loader struct {
	errs []error
}

// getString reads envPrefix+name from the environment, returning def if it
// is unset or empty. Strings cannot fail to parse.
func (l *loader) getString(name, def string) string {
	if v := os.Getenv(envPrefix + name); v != "" {
		return v
	}
	return def
}

// getStringSlice reads envPrefix+name as a comma-separated list, trimming
// whitespace around each element and dropping empty ones, returning def if
// it is unset or empty. A list cannot fail to parse.
func (l *loader) getStringSlice(name string, def []string) []string {
	v := os.Getenv(envPrefix + name)
	if v == "" {
		return def
	}
	var out []string
	for _, part := range strings.Split(v, ",") {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

// getInt reads envPrefix+name as an int, returning def if it is unset. A
// present but unparsable value is recorded in l.errs and def is returned.
func (l *loader) getInt(name string, def int) int {
	v := os.Getenv(envPrefix + name)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		l.errs = append(l.errs, fmt.Errorf("config: %s%s=%q: invalid integer: %w", envPrefix, name, v, err))
		return def
	}
	return n
}

// getInt64 reads envPrefix+name as an int64, returning def if it is unset.
// A present but unparsable value is recorded in l.errs and def is
// returned.
func (l *loader) getInt64(name string, def int64) int64 {
	v := os.Getenv(envPrefix + name)
	if v == "" {
		return def
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		l.errs = append(l.errs, fmt.Errorf("config: %s%s=%q: invalid integer: %w", envPrefix, name, v, err))
		return def
	}
	return n
}

// getBool reads envPrefix+name as a bool (accepting anything
// strconv.ParseBool does, e.g. "1"/"true"/"false"), returning def if it is
// unset. A present but unparsable value is recorded in l.errs and def is
// returned.
func (l *loader) getBool(name string, def bool) bool {
	v := os.Getenv(envPrefix + name)
	if v == "" {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		l.errs = append(l.errs, fmt.Errorf("config: %s%s=%q: invalid boolean: %w", envPrefix, name, v, err))
		return def
	}
	return b
}

// getDuration reads envPrefix+name as a time.Duration (using
// time.ParseDuration syntax, e.g. "30s", "2m"), returning def if it is
// unset. A present but unparsable value is recorded in l.errs and def is
// returned — this is what catches HELIOS_WORKER_LEASE_DURATION=60 (no
// unit), which time.ParseDuration rejects.
func (l *loader) getDuration(name string, def time.Duration) time.Duration {
	v := os.Getenv(envPrefix + name)
	if v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		l.errs = append(l.errs, fmt.Errorf("config: %s%s=%q: invalid duration: %w", envPrefix, name, v, err))
		return def
	}
	return d
}
