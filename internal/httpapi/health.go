package httpapi

import (
	"net/http"

	"github.com/pablofornell/helios-audio-transcription-service/internal/observability"
)

// healthz implements GET /healthz: liveness. It never consults the health
// registry -- CONTRACT.md is explicit that this only reports "the process
// is alive", which is true the instant this handler runs at all, so it is
// always 200. Anything that would make the process unable to answer HTTP
// at all (the real meaning of "unhealthy" here) means this handler never
// runs in the first place; there is no in-process condition for it to
// check.
func (h *handlers) healthz(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
}

// readyz implements GET /readyz: readiness, backed by h.health
// (*observability.Registry). The report is always the response body, on
// both success and failure, so a caller can see which check failed
// without a second request.
func (h *handlers) readyz(w http.ResponseWriter, r *http.Request) {
	report, err := h.health.Check(r.Context())
	if err != nil {
		// Registry.Check documents its returned error as always nil --
		// an individual checker's failure is reported inside Report, not
		// here. This branch exists purely as a fail-closed backstop in
		// case that contract is ever violated: still respond with a
		// (synthesised, not-OK) report rather than propagate a bare
		// error, since docs/CONTRACT.md gives /readyz no error envelope
		// of its own -- both its 200 and its 503 carry a report.
		observability.FromContext(r.Context()).Error("readyz: health check failed to run", "error", err)
		writeJSON(w, http.StatusServiceUnavailable, readyReportDTO{
			OK: false,
			Checks: map[string]checkResultDTO{
				"health": {OK: false, Detail: err.Error()},
			},
		})
		return
	}

	status := http.StatusOK
	if !report.OK {
		status = http.StatusServiceUnavailable
	}
	writeJSON(w, status, readyReportFromObservability(report))
}

// readyReportFromObservability converts an observability.Report to its
// wire shape.
func readyReportFromObservability(r observability.Report) readyReportDTO {
	checks := make(map[string]checkResultDTO, len(r.Checks))
	for name, c := range r.Checks {
		checks[name] = checkResultDTO{OK: c.OK, Detail: c.Detail, FreeBytes: c.FreeBytes, TotalBytes: c.TotalBytes}
	}
	return readyReportDTO{OK: r.OK, Checks: checks}
}
