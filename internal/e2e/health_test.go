package e2e

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pablofornell/helios-audio-transcription-service/internal/domain"
)

// readyzDependencyNames is exactly the set of checkers
// cmd/helios-transcription-api/wire.Build registers against
// Stack.HealthRegistry (see wire.go's own Build: dbChecker, storageChecker,
// transcriberChecker). Notably absent: jobs.Pool, which satisfies the
// observability.Checker shape (Name/Check) structurally but wire.Build
// never registers it -- so /readyz today cannot report a wedged claim loop
// via jobs.Pool.Check. That is a real, if narrow, gap this test's own list
// pins rather than papers over; see this package's report for detail.
var readyzDependencyNames = []string{"database", "storage", "transcriber"}

// TestReadyz_HealthyWithEverythingWired asserts /readyz reports 200 and
// every dependency wire.Build actually registers is healthy: the real
// shared Postgres pool, the real localfs store, and the (always ready)
// fake transcriber.
func TestReadyz_HealthyWithEverythingWired(t *testing.T) {
	requireDocker(t)
	requireFFmpeg(t)

	ts := newTestServer(t)

	resp := doJSON(t, http.MethodGet, ts.base+"/readyz", nil)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	report := decodeJSON[healthReportJSON](t, resp)
	assert.True(t, report.OK)
	for _, name := range readyzDependencyNames {
		c, ok := report.Checks[name]
		require.Truef(t, ok, "expected a %q entry in the readiness report, got %+v", name, report.Checks)
		assert.Truef(t, c.OK, "expected %q to be healthy, got detail %q", name, c.Detail)
	}
}

// TestReadyz_UnhealthyWhenTranscriberBroken points the transcriber
// dependency at brokenTranscriber (standing in for CONTRACT.md's "missing
// binary" scenario, which internal/transcriber/fake has no way to
// simulate -- see its own doc: it always reports ready) and asserts
// /readyz reports 503, with the transcriber check specifically reported
// unhealthy while every other dependency -- the real shared Postgres pool,
// the real store, the running worker pool -- stays healthy. This is the
// mapping CONTRACT.md section 6 promises: a broken dependency turns
// Registry.Check's Report.OK false, and httpapi's readyz handler maps that
// to 503, per-check detail visible in the same response.
func TestReadyz_UnhealthyWhenTranscriberBroken(t *testing.T) {
	requireDocker(t)
	requireFFmpeg(t)

	ts := newTestServerWithTranscriber(t, &brokenTranscriber{})

	resp := doJSON(t, http.MethodGet, ts.base+"/readyz", nil)
	defer resp.Body.Close()
	require.Equal(t, http.StatusServiceUnavailable, resp.StatusCode)

	report := decodeJSON[healthReportJSON](t, resp)
	assert.False(t, report.OK)

	transcriberCheck, ok := report.Checks["transcriber"]
	require.True(t, ok)
	assert.False(t, transcriberCheck.OK)
	assert.NotEmpty(t, transcriberCheck.Detail)

	for _, name := range []string{"database", "storage"} {
		c, ok := report.Checks[name]
		require.Truef(t, ok, "expected a %q entry in the readiness report", name)
		assert.Truef(t, c.OK, "expected %q to remain healthy despite the broken transcriber, got detail %q", name, c.Detail)
	}
}

// brokenTranscriber is an app.Transcriber whose Available always fails --
// standing in for a whisper-cli binary or model directory that is missing
// on disk (CONTRACT.md's own example of a broken transcriber dependency).
// Transcribe also fails, so a job that somehow reached it would not hang.
type brokenTranscriber struct{}

func (b *brokenTranscriber) Available(ctx context.Context) error {
	return domain.New(domain.ErrCodeModelUnavailable, "e2e: simulated missing whisper-cli binary/model")
}

func (b *brokenTranscriber) Transcribe(ctx context.Context, req domain.TranscribeRequest) (*domain.Result, error) {
	return nil, domain.New(domain.ErrCodeModelUnavailable, "e2e: simulated missing whisper-cli binary/model")
}
