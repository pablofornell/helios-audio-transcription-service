package observability

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-logr/logr"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
)

// Shutdown is the type of the function NewTracerProvider returns to release
// whatever resources its trace.TracerProvider holds (an exporter
// connection, a batch processor's flush goroutine, ...).
type Shutdown func(context.Context) error

// shutdownTimeoutCap bounds how long Shutdown will ever wait on the
// exporter, regardless of how generous the caller's own context is. An
// unreachable collector can otherwise eat most of a graceful-shutdown
// budget for a collector nobody is waiting for; capping it here means
// shutdown always returns promptly and, since the bound comes from cutting
// the context short rather than ignoring its error, surfaces the real
// timeout error instead of silently returning nil.
const shutdownTimeoutCap = 5 * time.Second

// bridgeOTelLogging routes OTel's internal logger and its global error
// handler through logger, instead of their defaults (stderr, via Go's
// standard "log" package). Without this, an export failure against a down
// collector prints a raw, unstructured line straight to stderr, which
// corrupts JSON log ingestion by interleaving a non-JSON line into what is
// otherwise a JSON log stream. otel.SetLogger/SetErrorHandler are
// process-global and cheap to call repeatedly (a mutex-guarded pointer
// swap), so this runs on every NewTracerProvider call — the last call
// before the OTel API is actually used wins, which for a long-running
// service is simply "the one call at startup".
func bridgeOTelLogging(logger *slog.Logger) {
	otel.SetLogger(logr.FromSlogHandler(logger.Handler()))
	otel.SetErrorHandler(otel.ErrorHandlerFunc(func(err error) {
		logger.Error("otel: internal error", "error", err)
	}))
}

// NewTracerProvider builds Helios's trace.TracerProvider.
//
// It always bridges OTel's internal logger and global error handler to
// logger first (see bridgeOTelLogging), so an export failure — whether
// tracing ends up enabled or not, since some other part of the process
// could still touch the OTel API — is reported through the structured
// logger instead of as a raw line on stderr.
//
// When enabled is false — the default — it returns a no-op
// trace.TracerProvider and a no-op Shutdown, without attempting any OTLP
// connection and without returning an error. Tracing is opt-in: the service
// must run correctly whether or not a collector exists anywhere.
//
// When enabled is true, it dials an OTLP/gRPC exporter at otlpEndpoint,
// attaches a resource identifying this service (serviceName, Version) and
// installs a batch span processor. The returned Shutdown flushes any
// buffered spans and closes the exporter connection; callers must call it
// during graceful shutdown. Shutdown bounds its own work to
// shutdownTimeoutCap (cut short by the caller's context if that is even
// tighter): against an unreachable collector, tp.Shutdown can otherwise
// take several seconds and return nil, masking the failure and consuming a
// large fraction of a typical shutdown budget on a collector nobody is
// waiting for. Cutting the context short makes it return promptly with the
// real (context deadline exceeded, wrapped) error instead.
func NewTracerProvider(ctx context.Context, logger *slog.Logger, enabled bool, otlpEndpoint, serviceName string) (trace.TracerProvider, Shutdown, error) {
	bridgeOTelLogging(logger)

	if !enabled {
		return noop.NewTracerProvider(), func(context.Context) error { return nil }, nil
	}

	exporter, err := otlptracegrpc.New(ctx,
		otlptracegrpc.WithEndpoint(otlpEndpoint),
		otlptracegrpc.WithInsecure(),
	)
	if err != nil {
		return nil, nil, err
	}

	name := serviceName
	if name == "" {
		name = "helios-ats"
	}
	res, err := resource.Merge(resource.Default(), resource.NewSchemaless(
		semconv.ServiceNameKey.String(name),
		semconv.ServiceVersionKey.String(Version),
	))
	if err != nil {
		return nil, nil, err
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(res),
	)

	shutdown := func(shutdownCtx context.Context) error {
		boundedCtx, cancel := context.WithTimeout(shutdownCtx, shutdownTimeoutCap)
		defer cancel()
		if err := tp.Shutdown(boundedCtx); err != nil {
			return fmt.Errorf("observability: shutdown trace provider: %w", err)
		}
		return nil
	}

	return tp, shutdown, nil
}

// HTTPMiddleware returns net/http middleware that wraps every request in an
// OTel span using tp, named operation (e.g. the service name). This is a
// thin re-export of otelhttp.NewMiddleware so callers never have to import
// otelhttp directly: they stay callers of this package like everything
// else.
//
// When tp is a no-op provider (tracing disabled), the returned middleware
// is itself a no-op: it creates spans that record nothing and are
// discarded, at negligible cost, so it is always safe to install
// unconditionally.
func HTTPMiddleware(tp trace.TracerProvider, operation string) func(http.Handler) http.Handler {
	return otelhttp.NewMiddleware(operation, otelhttp.WithTracerProvider(tp))
}
