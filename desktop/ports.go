package main

import (
	"context"
	"io"

	"github.com/pablofornell/helios-audio-transcription-service/internal/domain"
	"github.com/pablofornell/helios-audio-transcription-service/internal/httpclient"
	"github.com/pablofornell/helios-audio-transcription-service/internal/transcript"
)

// transcriptionAPI is the seam every App bound method (app.go) calls
// through instead of a concrete *httpclient.Client directly -- the same
// role the sibling ingestion service's app.IngestionAPI interface plays,
// trimmed to this service's own eight-call surface. remoteClient
// (serversettings.go) is the production implementation, backed by a
// *httpclient.Client that can be swapped out live by SetServerSettings;
// fakeAPI (fakes_test.go) is a hand-written test double. Declaring it here
// rather than importing httpclient.Client's methods directly is what lets
// app_test.go and reveal_test.go exercise App without a running server.
type transcriptionAPI interface {
	FreeStorageBytes(ctx context.Context) (uint64, error)
	Submit(ctx context.Context, req httpclient.SubmitRequest) (*domain.Transcription, error)
	Get(ctx context.Context, id string) (*domain.Transcription, error)
	List(ctx context.Context, f domain.ListFilter) (domain.Page, error)
	Delete(ctx context.Context, id string) error
	Cancel(ctx context.Context, id string) (*domain.Transcription, error)
	Retry(ctx context.Context, id string) (*domain.Transcription, error)
	Prioritize(ctx context.Context, id string) (*domain.Transcription, error)
	// GetTranscript returns the rendered transcript body for id in format
	// -- see internal/transcript.Format's own doc for the four accepted
	// values.
	GetTranscript(ctx context.Context, id string, format transcript.Format) ([]byte, error)
	// OpenAudio opens id's original uploaded audio for streaming -- see
	// reveal.go's own doc for why this process always spools it through a
	// local cache rather than assuming any direct filesystem access to the
	// server's storage.
	OpenAudio(ctx context.Context, id string) (io.ReadSeekCloser, httpclient.AudioInfo, error)
	// Health reports the target server's reachability and readiness. It
	// never returns a non-nil error -- see HealthReport's own doc -- so
	// Health() (app.go) and TestServerConnection (serversettings.go) can
	// always render its result directly.
	Health(ctx context.Context) (HealthReport, error)
}

// CheckResult is one named dependency check's outcome, mirroring
// httpclient.CheckResult -- declared separately, rather than reused
// directly, only so this package's own HealthReport (below) does not
// couple every caller of transcriptionAPI.Health to httpclient's specific
// type, the same reason app.go's other methods never hand a domain type
// straight to the frontend either.
type CheckResult struct {
	OK         bool
	Detail     string
	FreeBytes  *int64 `json:"freeBytes,omitempty"`
	TotalBytes *int64 `json:"totalBytes,omitempty"`
}

// HealthReport is transcriptionAPI.Health's result: the target server's
// reachability and readiness, aggregated. A transport failure (the server
// is unreachable, refused the connection, or timed out) is reported as
// HealthReport{OK: false, Checks: {"connection": {...}}}, mirroring
// GET /readyz's own "always 200 or 503 with a body" contract one layer up
// -- see remoteClient.Health's own doc for exactly how a transport error
// is turned into that shape.
type HealthReport struct {
	OK     bool
	Checks map[string]CheckResult
}

// connectionCheckName names the synthesised check remoteClient.Health and
// TestServerConnection (serversettings.go) both report under when the
// request could not even complete -- e.g. because the server is down --
// as opposed to a named check GET /readyz itself returned (e.g.
// "database", "transcriber"). Both call sites share this one constant so
// the frontend can key off a single, stable name regardless of which of
// the two produced the report.
const connectionCheckName = "connection"

// healthReportFromTransportErr builds the HealthReport a failed Healthz/
// Readyz round-trip reports as, rather than propagating err -- see
// HealthReport's own doc.
func healthReportFromTransportErr(err error) HealthReport {
	return HealthReport{
		OK: false,
		Checks: map[string]CheckResult{
			connectionCheckName: {OK: false, Detail: domain.AsError(err).Message},
		},
	}
}

// healthReportFromReady converts an httpclient.ReadyReport to a
// HealthReport.
func healthReportFromReady(r httpclient.ReadyReport) HealthReport {
	checks := make(map[string]CheckResult, len(r.Checks))
	for name, c := range r.Checks {
		checks[name] = CheckResult{OK: c.OK, Detail: c.Detail, FreeBytes: c.FreeBytes, TotalBytes: c.TotalBytes}
	}
	return HealthReport{OK: r.OK, Checks: checks}
}
