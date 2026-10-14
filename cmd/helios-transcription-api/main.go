// Command helios-transcription-api is the service entrypoint: it loads
// config, wires up every dependency (see
// cmd/helios-transcription-api/wire), and serves HTTP plus an in-process
// worker pool and retention sweep until an operator asks it to stop. The
// one exception is -migrate-only, which applies pending goose migrations
// (via internal/postgres.Migrate) and exits without starting any of that.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/pablofornell/helios-audio-transcription-service/cmd/helios-transcription-api/wire"
	"github.com/pablofornell/helios-audio-transcription-service/internal/config"
	"github.com/pablofornell/helios-audio-transcription-service/internal/observability"
	"github.com/pablofornell/helios-audio-transcription-service/internal/postgres"
)

func main() {
	if err := run(); err != nil {
		slog.Error("helios-transcription-api exited with error", "error", err)
		os.Exit(1)
	}
}

func run() error {
	migrateOnly := flag.Bool("migrate-only", false, "apply pending database migrations, then exit")
	flag.Parse()

	cfg, err := config.Load()
	if err != nil {
		return err
	}

	logger, err := observability.NewLogger(cfg.Observability.LogLevel, cfg.Observability.LogFormat)
	if err != nil {
		return err
	}
	slog.SetDefault(logger)

	if *migrateOnly {
		return runMigrateOnly(context.Background(), cfg, logger)
	}

	return runServer(cfg, logger)
}

// runMigrateOnly connects to the database DSN in cfg, applies every pending
// goose migration embedded in internal/postgres, and returns — it never
// starts the HTTP server, the worker pool or the retention sweep. It never
// logs cfg.Database.DSN unredacted: observability.NewLogger's handler
// redacts any attribute whose key looks secret-shaped (see
// internal/observability/logging.go's redactedKeys), which "database_dsn"
// matches, so logging it below is safe by construction rather than by
// care taken at each call site.
func runMigrateOnly(ctx context.Context, cfg *config.Config, logger *slog.Logger) error {
	pool, err := pgxpool.New(ctx, cfg.Database.DSN)
	if err != nil {
		return fmt.Errorf("migrate: connect to database: %w", err)
	}
	defer pool.Close()

	if err := postgres.Migrate(ctx, pool); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}

	logger.Info("migrations applied")
	return nil
}

// runServer builds the full dependency graph (wire.Build), starts the
// worker pool and retention sweep, starts the HTTP server, and blocks
// until an operator's first SIGINT/SIGTERM, at which point it shuts
// everything down in the order that matters — see the comment above
// shutdownAndDrain for why. A second SIGINT/SIGTERM while that is in
// progress forces an immediate, unclean exit: an operator who presses
// Ctrl-C twice means it, and should not have to wait out however long a
// graceful drain takes.
func runServer(cfg *config.Config, logger *slog.Logger) error {
	// ctx carries only the first shutdown signal. Everything Build and
	// Start do runs against context.Background() instead, deliberately:
	// wire.Stack.Start's own doc explains why the worker pool's and
	// retention sweep's lifetimes must not be tied to it, and the same is
	// true of the HTTP server below — both must keep running right up
	// until this function chooses to stop them, not the instant ctx
	// happens to end.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	logger.Info("starting",
		"http_addr", cfg.HTTP.Addr,
		"database_dsn", cfg.Database.DSN, // redacted by the logger's handler -- see runMigrateOnly's doc.
		"transcriber_kind", cfg.Transcriber.Kind,
		"transcriber_no_gpu", cfg.Transcriber.NoGPU,
		"transcriber_gpu_device", cfg.Transcriber.GPUDevice,
		"storage_root_dir", cfg.Storage.RootDir,
		"worker_concurrency", cfg.Worker.Concurrency,
		"retention_enabled", cfg.Retention.Enabled,
		"observability_service_name", cfg.Observability.ServiceName,
		"observability_tracing_enabled", cfg.Observability.TracingEnabled,
	)

	stack, err := wire.Build(ctx, cfg)
	if err != nil {
		return fmt.Errorf("build stack: %w", err)
	}
	// Start does not accept ctx: see its doc on why the pool's and
	// sweep's lifetimes are independent of it. They are stopped only by
	// stack.Shutdown, below.
	stack.Start()

	srv := &http.Server{
		Addr:        cfg.HTTP.Addr,
		Handler:     stack.Router,
		ReadTimeout: cfg.HTTP.ReadTimeout,
		// WriteTimeout is kept here, server-wide, rather than disabled
		// (WriteTimeout: 0) -- it is still exactly the protection every
		// OTHER route needs against a slow-reading client tying up a
		// connection indefinitely while a handler writes its response.
		// The one route that legitimately needs longer --
		// POST /transcriptions, whose upload can outlive this budget on a
		// merely slow (not stuck) connection, since net/http resets
		// WriteTimeout from the moment headers are read, not from when the
		// handler starts writing -- extends its OWN per-request write
		// deadline via http.ResponseController.SetWriteDeadline instead;
		// see httpapi's submitTranscription. That keeps the server-wide
		// floor in place for every handler that never opted into a longer
		// one.
		WriteTimeout: cfg.HTTP.WriteTimeout,
		IdleTimeout:  cfg.HTTP.IdleTimeout,
	}

	serveErr := make(chan error, 1)
	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErr <- err
			return
		}
		serveErr <- nil
	}()

	select {
	case <-ctx.Done():
		logger.Info("shutdown signal received, draining")
		// Armed the instant the first signal arrives: a second one forces
		// an immediate exit rather than waiting out shutdownAndDrain below.
		go forceExitOnSecondSignal(logger)
	case err := <-serveErr:
		if err != nil {
			return fmt.Errorf("http server: %w", err)
		}
		// The server exited on its own (e.g. the listener was closed by
		// something other than Shutdown) without ever receiving a signal;
		// still tear the rest of the stack down cleanly rather than
		// leaking it.
	}

	return shutdownAndDrain(cfg, logger, srv, stack)
}

// shutdownAndDrain performs the shutdown sequence in the only order that
// is safe: stop accepting HTTP requests first (srv.Shutdown, bounded by
// HTTP.ShutdownTimeout), THEN stop the worker pool and retention sweep and
// let them drain (stack.Shutdown). Reversing this would let a *new* HTTP
// request start a *new* job while the pool was already draining, which is
// the failure this ordering exists to prevent — stack.Shutdown's own
// internal ordering (job pool drained, then the database pool it and any
// still-finishing job depend on is closed) is a separate, narrower
// guarantee that does not on its own stop the HTTP server from accepting
// more work in the meantime.
func shutdownAndDrain(cfg *config.Config, logger *slog.Logger, srv *http.Server, stack *wire.Stack) error {
	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.HTTP.ShutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.Error("http server shutdown did not complete cleanly", "error", err)
	}

	// stack.Shutdown's own bound has to cover the worker pool's drain
	// deadline, which is sized off Worker.JobTimeout (see jobs.Pool.Run:
	// up to one JobTimeout waiting for in-flight jobs to finish on their
	// own, then up to another JobTimeout after force-canceling them), not
	// HTTP.ShutdownTimeout, which is sized for requests, not
	// transcriptions.
	stackCtx, cancelStack := context.WithTimeout(context.Background(), 2*cfg.Worker.JobTimeout+cfg.HTTP.ShutdownTimeout)
	defer cancelStack()
	if err := stack.Shutdown(stackCtx); err != nil {
		logger.Error("stack shutdown did not complete cleanly", "error", err)
	}

	logger.Info("stopped")
	return nil
}

// forceExitOnSecondSignal blocks for one more SIGINT/SIGTERM and exits the
// process immediately (skipping every deferred cleanup) if one arrives. It
// is only ever started after the first signal has already begun a graceful
// shutdown (see runServer), so a second one unambiguously means "stop
// waiting".
func forceExitOnSecondSignal(logger *slog.Logger) {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	<-ctx.Done()
	logger.Error("second shutdown signal received, forcing immediate exit")
	os.Exit(1)
}
