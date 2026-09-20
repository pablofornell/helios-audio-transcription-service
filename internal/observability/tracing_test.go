package observability

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"go.opentelemetry.io/otel"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestNewTracerProvider_DisabledIsNoop is the load-bearing test: with
// tracing off (the default), NewTracerProvider must return instantly,
// without error, and hand back a genuinely working no-op provider — no
// collector required anywhere for the service to run.
func TestNewTracerProvider_DisabledIsNoop(t *testing.T) {
	logger, err := newLogger(&bytes.Buffer{}, "info", "json")
	require.NoError(t, err)

	start := time.Now()
	// A garbage endpoint that would fail to resolve/dial if anything ever
	// tried to use it — proving it never does.
	tp, shutdown, err := NewTracerProvider(context.Background(), logger, false, "this-host-does-not-exist.invalid:4317", "helios-test")
	elapsed := time.Since(start)

	require.NoError(t, err)
	require.NotNil(t, tp)
	require.NotNil(t, shutdown)
	// No network attempt happened: construction must be essentially
	// instantaneous, not bounded by any dial/DNS timeout.
	assert.Less(t, elapsed, 200*time.Millisecond)

	// The provider actually works: starting and ending a span on it must
	// not panic or block.
	tracer := tp.Tracer("observability_test")
	ctx, span := tracer.Start(context.Background(), "test-span")
	assert.NotNil(t, ctx)
	span.End()

	// A no-op provider's spans are not recording and carry an invalid span
	// context — that's what "no-op" means operationally.
	assert.False(t, span.SpanContext().IsValid())
	assert.False(t, span.IsRecording())

	// Shutdown is itself a no-op and returns immediately.
	shutdownStart := time.Now()
	require.NoError(t, shutdown(context.Background()))
	assert.Less(t, time.Since(shutdownStart), 200*time.Millisecond)
}

// TestNewTracerProvider_EnabledDoesNotBlock exercises the enabled path
// against an endpoint nothing is listening on. otlptracegrpc dials lazily,
// so construction must still return quickly and without error; only an
// actual export attempt (which nothing here triggers) would ever surface
// the connection failure.
func TestNewTracerProvider_EnabledDoesNotBlock(t *testing.T) {
	logger, err := newLogger(&bytes.Buffer{}, "info", "json")
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	start := time.Now()
	tp, shutdown, err := NewTracerProvider(ctx, logger, true, "127.0.0.1:0", "helios-test")
	require.NoError(t, err)
	require.NotNil(t, tp)
	require.NotNil(t, shutdown)
	assert.Less(t, time.Since(start), 2*time.Second)

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer shutdownCancel()
	// Regardless of whether the flush itself errors (nothing is
	// listening), Shutdown must respect its context and return instead of
	// hanging forever.
	done := make(chan error, 1)
	go func() { done <- shutdown(shutdownCtx) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("shutdown did not return within its context's timeout")
	}
}

// TestNewTracerProvider_ShutdownAgainstDownCollectorIsBoundedAndReportsError
// proves that against a down collector, with at least one span actually
// queued for export, Shutdown returns well within shutdownTimeoutCap's
// slack and surfaces a real error instead of masking the failure.
func TestNewTracerProvider_ShutdownAgainstDownCollectorIsBoundedAndReportsError(t *testing.T) {
	logger, err := newLogger(&bytes.Buffer{}, "info", "json")
	require.NoError(t, err)

	// Loopback, low-numbered port: nothing listens here in a test
	// environment and the connection is refused immediately rather than
	// merely slow to fail, so this test isn't at the mercy of an external
	// network path.
	tp, shutdown, err := NewTracerProvider(context.Background(), logger, true, "127.0.0.1:1", "helios-test")
	require.NoError(t, err)

	// Queue at least one span so Shutdown has something to actually try to
	// flush/export, rather than shutting down an idle batch processor.
	_, span := tp.Tracer("observability_test").Start(context.Background(), "test-span")
	span.End()

	// A generous caller-side budget, deliberately larger than
	// shutdownTimeoutCap, so this test proves NewTracerProvider imposes its
	// own tighter cap rather than merely inheriting whatever the caller
	// already provided.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	start := time.Now()
	shutdownErr := shutdown(shutdownCtx)
	elapsed := time.Since(start)

	assert.Less(t, elapsed, 8*time.Second,
		"shutdown against a down collector must be bounded by its own short cap, not eat most of a 20s graceful-shutdown budget")
	assert.Error(t, shutdownErr, "shutdown against a down collector must return the real failure, not mask it as nil")
}

// TestNewTracerProvider_BridgesOTelErrorsToStructuredLogger is the
// regression test for OTel export failures bypassing slog and printing a
// raw, unstructured line to stderr (which corrupts JSON log ingestion):
// after NewTracerProvider runs, an OTel-internal error reported via
// otel.Handle must come out through the logger NewTracerProvider was
// given, not OTel's own default stderr handler.
func TestNewTracerProvider_BridgesOTelErrorsToStructuredLogger(t *testing.T) {
	var buf bytes.Buffer
	logger, err := newLogger(&buf, "info", "json")
	require.NoError(t, err)

	_, shutdown, err := NewTracerProvider(context.Background(), logger, false, "", "helios-test")
	require.NoError(t, err)
	defer shutdown(context.Background())

	otel.Handle(errors.New("simulated otel export failure"))

	assert.Contains(t, buf.String(), "simulated otel export failure")
}

func TestHTTPMiddleware_WrapsHandlerWithoutError(t *testing.T) {
	logger, err := newLogger(&bytes.Buffer{}, "info", "json")
	require.NoError(t, err)

	tp, shutdown, err := NewTracerProvider(context.Background(), logger, false, "", "helios-test")
	require.NoError(t, err)
	defer shutdown(context.Background())

	called := false
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusTeapot)
	})

	wrapped := HTTPMiddleware(tp, "test-operation")(inner)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/anything", nil)
	wrapped.ServeHTTP(rec, req)

	assert.True(t, called)
	assert.Equal(t, http.StatusTeapot, rec.Code)
}
