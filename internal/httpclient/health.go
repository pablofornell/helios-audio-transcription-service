package httpclient

import (
	"context"
	"net/http"
)

// Healthz implements GET /healthz: liveness. Per docs/openapi.yaml it is
// unconditionally 200 (with no body) the instant the process can answer
// HTTP at all, so this reports only whether the round-trip itself
// succeeded -- any non-2xx or transport failure is returned as the usual
// *domain.Error.
func (c *Client) Healthz(ctx context.Context) error {
	return c.doJSON(ctx, http.MethodGet, "/healthz", nil, nil, http.StatusOK)
}

// Readyz implements GET /readyz: readiness, aggregating every dependency
// check the server has registered. Unlike Healthz, both its 200 (ready)
// and 503 (not ready) responses carry a ReadyReport body -- this method
// decodes it in either case and only returns an error for something GET
// /readyz never documents (a transport failure, or a response this client
// cannot even parse as a ReadyReport), so a caller can always read
// report.OK to tell "not ready" apart from "could not ask".
func (c *Client) Readyz(ctx context.Context) (ReadyReport, error) {
	var report ReadyReport
	if err := c.doJSON(ctx, http.MethodGet, "/readyz", nil, &report, http.StatusOK, http.StatusServiceUnavailable); err != nil {
		return ReadyReport{}, err
	}
	return report, nil
}
