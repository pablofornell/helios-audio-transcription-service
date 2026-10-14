// Package wire builds the dependency graph helios-transcription-api runs:
// every port and implementation named in CONTRACT.md, constructed in the
// order that matters and exposed as a Stack that knows how to tear itself
// down in the reverse order.
//
// This exists so cmd/helios-transcription-api's main and any future
// end-to-end test share one construction path instead of two: main calls
// Build to run the real service, and a test could call the exact same
// Build (with the transcriber substituted via WithTranscriber) so its
// scenarios exercise the real startup sequence, not a parallel imitation
// of it that could quietly drift out of sync with what main actually does.
package wire

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"go.opentelemetry.io/otel/trace"

	"log/slog"

	"github.com/pablofornell/helios-audio-transcription-service/internal/app"
	"github.com/pablofornell/helios-audio-transcription-service/internal/audio"
	"github.com/pablofornell/helios-audio-transcription-service/internal/config"
	"github.com/pablofornell/helios-audio-transcription-service/internal/httpapi"
	"github.com/pablofornell/helios-audio-transcription-service/internal/jobs"
	"github.com/pablofornell/helios-audio-transcription-service/internal/observability"
	"github.com/pablofornell/helios-audio-transcription-service/internal/postgres"
	"github.com/pablofornell/helios-audio-transcription-service/internal/retention"
	"github.com/pablofornell/helios-audio-transcription-service/internal/storage/localfs"
	"github.com/pablofornell/helios-audio-transcription-service/internal/transcriber/fake"
	"github.com/pablofornell/helios-audio-transcription-service/internal/transcriber/whispercpp"
)

// Compile-time assertions that every adapter this package wires together
// actually satisfies the port internal/app declares for it. These exist
// purely so a signature drift in any of the (independently built) adapter
// packages fails `go build` here, at the one place that wires them all
// together, instead of surfacing as a runtime type-assertion panic that
// would otherwise never happen because Go interface satisfaction is
// entirely structural.
var (
	_ app.Repository     = (*postgres.Store)(nil)
	_ app.Queue          = (*postgres.Store)(nil)
	_ app.Store          = (*localfs.Store)(nil)
	_ app.AudioProcessor = (*audio.Processor)(nil)
	_ app.Transcriber    = (*whispercpp.Transcriber)(nil)
	_ app.Transcriber    = (*fake.Transcriber)(nil)
)

// Stack is every long-lived dependency Build constructs, in construction
// order top to bottom. Fields are exported so both cmd/helios-transcription-
// api's main (which still owns the *http.Server and the OS signal
// handling — neither belongs in a package a test also links) and a future
// end-to-end test (which would need the router, the raw *pgxpool.Pool for
// assertions, etc.) can reach into it.
type Stack struct {
	Cfg    *config.Config
	Logger *slog.Logger

	// TracerProvider is a no-op provider when
	// cfg.Observability.TracingEnabled is false — see
	// observability.NewTracerProvider. tracerShutdown releases whatever
	// resources it holds; Shutdown calls it last.
	TracerProvider trace.TracerProvider
	tracerShutdown observability.Shutdown

	// PromRegistry is the private Prometheus registry Metrics is bound to;
	// kept on Stack so a caller (a test, mainly) can scrape it directly
	// without going through Router's /metrics handler.
	PromRegistry *prometheus.Registry
	Metrics      *observability.Metrics

	// DB is the pgxpool.Pool backing Repository/Queue/the "database" health
	// check. Shutdown closes it; Build does not hand ownership anywhere
	// else that would also try to.
	DB *pgxpool.Pool

	Store       *localfs.Store
	Audio       app.AudioProcessor
	Transcriber app.Transcriber

	// HealthRegistry aggregates the four checkers that back /readyz --
	// database, storage and transcriber (the things this process depends
	// on) plus the worker pool (this process itself). Kept on Stack too so
	// a caller (a test, mainly) can reach it independently of an HTTP
	// round trip.
	HealthRegistry *observability.Registry

	Service *app.Service
	JobPool *jobs.Pool

	// Retention is the background sweep that purges terminal
	// transcriptions older than cfg.Retention.MaxAge — both their database
	// rows and their files on disk. Nil when cfg.Retention.Enabled is
	// false: see this package's Start/Shutdown for why a nil Retention is
	// a normal, not a partially-built, Stack.
	Retention *retention.Sweeper

	Router http.Handler

	// poolCancel/poolDone back Start/Shutdown's control over
	// JobPool.Run's goroutine. Both are nil until Start has been called.
	poolCancel context.CancelFunc
	poolDone   chan struct{}

	// retentionCancel/retentionDone are the same pattern as
	// poolCancel/poolDone above, for Retention.Run's goroutine instead of
	// JobPool.Run's. Both stay nil when Retention is nil.
	retentionCancel context.CancelFunc
	retentionDone   chan struct{}
}

// Option customises Build. The only one today is WithTranscriber.
type Option func(*buildOptions)

type buildOptions struct {
	transcriber app.Transcriber
}

// WithTranscriber overrides the app.Transcriber Build would otherwise
// select from cfg.Transcriber.Kind. Production wiring
// (cmd/helios-transcription-api) never passes this: it exists for a future
// end-to-end test whose scenarios (a scripted retryable failure, a
// transcription that blocks until canceled) need transcriber behaviour
// config.TranscriberConfig has no field to express — while still going
// through Build for every other dependency, so the test exercises the real
// construction path with one deliberate, documented substitution, not a
// second one built by hand.
func WithTranscriber(t app.Transcriber) Option {
	return func(o *buildOptions) { o.transcriber = t }
}

// Build constructs every dependency helios-transcription-api needs, in
// dependency order: logger → tracer provider → Prometheus registry +
// metrics → pgxpool (parsed from cfg.Database.DSN, tuned, pinged, then
// migrated when cfg.Database.AutoMigrate) → localfs store → audio
// processor → transcriber (whispercpp or fake, per
// cfg.Transcriber.Kind) → health registry → app.Service → jobs.Pool →
// retention.Sweeper (only when cfg.Retention.Enabled) → httpapi.NewRouter.
//
// It deliberately stops short of two things a caller must still do:
// starting the job pool and retention sweep (see Stack.Start) and
// listening on HTTP (owned by main) — both are lifecycle decisions Build
// itself should not make, since a caller may want to do other setup (or
// nothing at all, for a test that never submits a job) between
// constructing the Stack and running it.
//
// On any failure, Build tears down whatever it had already brought up
// (closing the pool it opened, flushing the tracer it started) before
// returning the error, so a failed Build never leaks a connection pool or
// a tracer's background flush goroutine.
func Build(ctx context.Context, cfg *config.Config, opts ...Option) (stack *Stack, err error) {
	var built buildOptions
	for _, opt := range opts {
		opt(&built)
	}

	s := &Stack{Cfg: cfg}

	var cleanups []func()
	defer func() {
		if err != nil {
			for i := len(cleanups) - 1; i >= 0; i-- {
				cleanups[i]()
			}
		}
	}()

	s.Logger, err = observability.NewLogger(cfg.Observability.LogLevel, cfg.Observability.LogFormat)
	if err != nil {
		return nil, fmt.Errorf("wire: build logger: %w", err)
	}

	s.TracerProvider, s.tracerShutdown, err = observability.NewTracerProvider(
		ctx, s.Logger, cfg.Observability.TracingEnabled, cfg.Observability.OTLPEndpoint, cfg.Observability.ServiceName,
	)
	if err != nil {
		return nil, fmt.Errorf("wire: build tracer provider: %w", err)
	}
	cleanups = append(cleanups, func() { _ = s.tracerShutdown(context.Background()) })

	s.PromRegistry = prometheus.NewRegistry()
	s.Metrics = observability.NewMetrics(s.PromRegistry)

	poolCfg, err := pgxpool.ParseConfig(cfg.Database.DSN)
	if err != nil {
		return nil, fmt.Errorf("wire: parse database DSN: %w", err)
	}
	poolCfg.MaxConns = int32(cfg.Database.MaxConns)
	poolCfg.ConnConfig.ConnectTimeout = cfg.Database.ConnectTimeout

	s.DB, err = pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		return nil, fmt.Errorf("wire: connect to database: %w", err)
	}
	cleanups = append(cleanups, s.DB.Close)

	// pgxpool.NewWithConfig itself never dials: it only prepares the pool,
	// so a bad host/credentials/network would otherwise surface on the
	// first request instead of at startup. Ping, bounded by
	// Database.ConnectTimeout, is what actually makes Postgres a real
	// startup dependency rather than a lazy one.
	{
		connectCtx, cancel := context.WithTimeout(ctx, cfg.Database.ConnectTimeout)
		pingErr := s.DB.Ping(connectCtx)
		cancel()
		if pingErr != nil {
			return nil, fmt.Errorf("wire: ping database: %w", pingErr)
		}
	}

	if cfg.Database.AutoMigrate {
		if err = postgres.Migrate(ctx, s.DB); err != nil {
			return nil, fmt.Errorf("wire: run migrations: %w", err)
		}
	}

	s.Store, err = localfs.New(cfg.Storage.RootDir)
	if err != nil {
		return nil, fmt.Errorf("wire: build storage: %w", err)
	}

	// Computed here, before audio.New below, rather than down by
	// storageChecker's construction where it used to live: audio.Config's
	// own MinFreeBytes (Normalize's pre-run re-check, one of the
	// mitigations for a lying-duration playlist amplifying a tiny upload
	// into a huge decoded WAV -- see internal/audio's own doc) needs the
	// exact same floor the storage health check and Service.Submit already
	// enforce, not a second, independently-drifting one.
	minFreeBytes := uint64(0)
	if cfg.Storage.MinFreeBytes > 0 {
		minFreeBytes = uint64(cfg.Storage.MinFreeBytes)
	}

	s.Audio = audio.New(audio.Config{
		FFmpegBin:    cfg.Audio.FFmpegBinary,
		FFprobeBin:   cfg.Audio.FFprobeBinary,
		MaxDuration:  cfg.Audio.MaxDuration,
		Timeout:      cfg.Audio.Timeout,
		MinFreeBytes: minFreeBytes,
	})

	s.Transcriber = buildTranscriber(cfg.Transcriber, built.transcriber)

	dbStore := postgres.New(s.DB)

	s.HealthRegistry = observability.NewRegistry()
	checkers := []observability.Checker{
		dbChecker{pool: s.DB},
		storageChecker{store: s.Store, minFreeBytes: minFreeBytes},
		transcriberChecker{transcriber: s.Transcriber},
	}
	for _, checker := range checkers {
		if err = s.HealthRegistry.Register(checker); err != nil {
			return nil, fmt.Errorf("wire: register health checker: %w", err)
		}
	}

	s.Service = app.NewService(app.Deps{
		Repo:           dbStore,
		Queue:          dbStore,
		Store:          s.Store,
		Clock:          app.SystemClock{},
		IDs:            uuidGenerator{},
		Logger:         s.Logger,
		Metrics:        s.Metrics,
		DefaultModel:   cfg.Transcriber.DefaultModel,
		AllowedModels:  cfg.Transcriber.AllowedModels,
		MaxAttempts:    cfg.Worker.MaxAttempts,
		MaxUploadBytes: cfg.HTTP.MaxUploadBytes,
		MinFreeBytes:   minFreeBytes,
	})

	s.JobPool = jobs.NewPool(jobs.Config{
		Concurrency:       cfg.Worker.Concurrency,
		PollInterval:      cfg.Worker.PollInterval,
		LeaseDuration:     cfg.Worker.LeaseDuration,
		HeartbeatInterval: cfg.Worker.HeartbeatInterval,
		JobTimeout:        cfg.Worker.JobTimeout,
		MaxAttempts:       cfg.Worker.MaxAttempts,
		BackoffBase:       cfg.Worker.BackoffBase,
		BackoffMax:        cfg.Worker.BackoffMax,
	}, jobs.Deps{
		Queue:       dbStore,
		Store:       s.Store,
		Audio:       s.Audio,
		Transcriber: s.Transcriber,
		Clock:       app.SystemClock{},
		Logger:      s.Logger,
		Metrics:     s.Metrics,
	})

	// The pool registers itself only here, after construction, because it
	// needs the three dependency checkers above to exist first -- and
	// because it is the one checker that reports on this process rather
	// than on something it talks to. Without it /readyz stays green while
	// the claim loop is wedged, which is precisely the failure an operator
	// most needs to see: every dependency healthy, and no job moving.
	if err = s.HealthRegistry.Register(s.JobPool); err != nil {
		return nil, fmt.Errorf("wire: register worker pool health checker: %w", err)
	}

	// Only constructed -- and, per Start's doc, only ever run -- when the
	// operator opted in. See Config's own doc on why "disabled" has no
	// field of its own to check at run time: a nil Retention here is what
	// makes "disabled" a construction-time decision instead.
	if cfg.Retention.Enabled {
		s.Retention = retention.New(retention.Config{
			MaxAge:   cfg.Retention.MaxAge,
			Interval: cfg.Retention.Interval,
		}, retention.Deps{
			Queue:  dbStore,
			Store:  s.Store,
			Clock:  app.SystemClock{},
			Logger: s.Logger,
		})
	}

	s.Router = httpapi.NewRouter(httpapi.RouterDeps{
		Service:        s.Service,
		Storage:        s.Store,
		Health:         s.HealthRegistry,
		Metrics:        s.Metrics,
		Logger:         s.Logger,
		TracerProvider: s.TracerProvider,
		MaxUploadBytes: cfg.HTTP.MaxUploadBytes,
		// ReadTimeout, not WriteTimeout: CONTRACT.md section 6's upload
		// route reads the whole (up to MaxUploadBytes) multipart body
		// inside the handler itself, and HTTP.ReadTimeout is the knob
		// already sized for that ("long, uploads are large" -- see
		// config.go). A route timeout any shorter would cut off a large,
		// merely slow, upload before the handler's own logic ever gets a
		// chance to reject or accept it.
		RouteTimeout:       cfg.HTTP.ReadTimeout,
		MetricsEnabled:     cfg.Observability.MetricsEnabled,
		CORSAllowedOrigins: cfg.CORS.AllowedOrigins,
	})

	return s, nil
}

// buildTranscriber selects the app.Transcriber Build wires in: override
// when a caller supplied one via WithTranscriber, otherwise whichever
// implementation cfg.Kind names. cfg.Kind is assumed already validated to
// be "whispercpp" or "fake" (config.Config.Validate enforces this before
// Build ever sees cfg), so the default case below is whispercpp's, not a
// third, unrecognised kind.
func buildTranscriber(cfg config.TranscriberConfig, override app.Transcriber) app.Transcriber {
	if override != nil {
		return override
	}
	if cfg.Kind == "fake" {
		return fake.New(fake.Config{})
	}
	return whispercpp.New(whispercpp.Config{
		Binary:       cfg.Binary,
		ModelDir:     cfg.ModelDir,
		DefaultModel: cfg.DefaultModel,
		Threads:      cfg.Threads,
		NoGPU:        cfg.NoGPU,
		GPUDevice:    cfg.GPUDevice,
		Timeout:      cfg.Timeout,
	})
}

// uuidGenerator implements app.IDGenerator by minting a random UUIDv4 per
// call, which is exactly what domain.Transcription.ID documents its shape
// as.
type uuidGenerator struct{}

func (uuidGenerator) NewID() string { return uuid.NewString() }

// dbChecker implements observability.Checker (and, structurally,
// app.HealthChecker) for the database dependency: readiness is simply
// whether the pool can still reach Postgres.
type dbChecker struct {
	pool *pgxpool.Pool
}

func (dbChecker) Name() string { return "database" }

func (c dbChecker) Check(ctx context.Context) error {
	if err := c.pool.Ping(ctx); err != nil {
		return fmt.Errorf("database: ping: %w", err)
	}
	return nil
}

// storageChecker implements observability.Checker for the local
// filesystem store: readiness requires both that free space has not
// dropped below the configured floor (the same floor Submit itself
// enforces, so /readyz goes unhealthy at the same point uploads start
// being refused, not some other threshold) and that the store is still
// actually writable, proven by round-tripping a small probe file rather
// than trusting a free-space number that a read-only remount would not
// change.
type storageChecker struct {
	store        *localfs.Store
	minFreeBytes uint64
}

func (c storageChecker) StorageCapacity(ctx context.Context) (int64, int64, error) {
	return c.store.StorageCapacity(ctx)
}

func (storageChecker) Name() string { return "storage" }

// healthProbePath is the store-relative path storageChecker.Check writes
// to and removes on every run. It lives under its own directory, never
// under "transcriptions/", so it can never collide with a real upload no
// matter what identifier a caller supplies.
const healthProbePath = "health/probe"

func (c storageChecker) Check(ctx context.Context) error {
	free, err := c.store.FreeBytes()
	if err != nil {
		return fmt.Errorf("storage: free bytes: %w", err)
	}
	if free < c.minFreeBytes {
		return fmt.Errorf("storage: %d bytes free, below the %d byte floor", free, c.minFreeBytes)
	}

	if _, err := c.store.Put(ctx, healthProbePath, strings.NewReader("ok")); err != nil {
		return fmt.Errorf("storage: writability probe: %w", err)
	}
	if err := c.store.Remove(ctx, healthProbePath); err != nil {
		return fmt.Errorf("storage: writability probe cleanup: %w", err)
	}
	return nil
}

// transcriberChecker implements observability.Checker for the transcriber
// dependency, delegating to its own Available method (binary resolvable,
// model directory readable) exactly as CONTRACT.md section 5 documents it.
type transcriberChecker struct {
	transcriber app.Transcriber
}

func (transcriberChecker) Name() string { return "transcriber" }

func (c transcriberChecker) Check(ctx context.Context) error {
	if err := c.transcriber.Available(ctx); err != nil {
		return fmt.Errorf("transcriber: %w", err)
	}
	return nil
}

// Start launches the job pool's claim/sweep loops and, when configured,
// the retention sweep, both as independent background goroutines with
// their own lifetime -- deliberately not derived from ctx (Build's ctx is
// long gone by the time Start is called; see main.go's own doc on why the
// pool's lifetime must not be tied to the signal-bearing context either).
// Both are stopped only by Shutdown.
func (s *Stack) Start() {
	poolCtx, poolCancel := context.WithCancel(context.Background())
	s.poolCancel = poolCancel
	s.poolDone = make(chan struct{})
	go func() {
		defer close(s.poolDone)
		if err := s.JobPool.Run(poolCtx); err != nil {
			s.Logger.Error("wire: job pool exited with an error", "error", err)
		}
	}()

	if s.Retention == nil {
		return
	}
	retentionCtx, retentionCancel := context.WithCancel(context.Background())
	s.retentionCancel = retentionCancel
	s.retentionDone = make(chan struct{})
	go func() {
		defer close(s.retentionDone)
		if err := s.Retention.Run(retentionCtx); err != nil {
			s.Logger.Error("wire: retention sweep exited with an error", "error", err)
		}
	}()
}

// Shutdown stops every background loop Start began and releases every
// resource Build acquired, in the order that matters:
//
//  1. Stop the retention sweep. It never holds a job's lease and never
//     races an in-flight job, so there is no ordering constraint between
//     it and the job pool below; stopping it first just means it is not
//     still purging rows while the rest of shutdown runs.
//  2. Stop the job pool: first cancel its claim/sweep loops so it claims
//     nothing new, then call JobPool.Shutdown(ctx) to drain whatever is
//     already in flight -- see jobs.Pool.Shutdown's own doc for its
//     passive-then-forced drain sequence.
//  3. Flush the tracer provider.
//  4. Close the database pool.
//
// The database pool is closed LAST, deliberately: a job draining in step 2
// still needs it for Queue.Heartbeat/Finish calls, and closing it any
// earlier would turn a job that was about to finish cleanly into one that
// fails on its very last write instead.
func (s *Stack) Shutdown(ctx context.Context) error {
	var errs []error

	if s.retentionCancel != nil {
		s.retentionCancel()
		select {
		case <-s.retentionDone:
		case <-ctx.Done():
			errs = append(errs, fmt.Errorf("wire: retention sweep did not stop before the shutdown deadline: %w", ctx.Err()))
		}
	}

	if s.poolCancel != nil {
		s.poolCancel()
		if err := s.JobPool.Shutdown(ctx); err != nil {
			errs = append(errs, fmt.Errorf("wire: job pool shutdown: %w", err))
		}
		select {
		case <-s.poolDone:
		case <-ctx.Done():
			errs = append(errs, fmt.Errorf("wire: job pool's Run did not return before the shutdown deadline: %w", ctx.Err()))
		}
	}

	if s.tracerShutdown != nil {
		if err := s.tracerShutdown(ctx); err != nil {
			errs = append(errs, fmt.Errorf("wire: tracer provider shutdown: %w", err))
		}
	}

	if s.DB != nil {
		s.DB.Close()
	}

	return errors.Join(errs...)
}
