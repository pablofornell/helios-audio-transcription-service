package httpclient

// The types below are declared here, rather than reused from
// internal/observability, because that package pulls in this service's
// health-checker wiring (database, storage, transcriber) that a client has
// no business depending on, and because its Go types (observability.Report,
// observability.CheckResult) carry no json tags at all -- they are not
// wire types, internal/httpapi's readyReportDTO/checkResultDTO are. See
// this package's doc comment for when a client-local DTO is warranted
// versus reusing internal/domain directly.

// CheckResult is one named dependency check's outcome, as reported by
// GET /readyz. Mirrors internal/httpapi's checkResultDTO field for field.
type CheckResult struct {
	OK bool `json:"ok"`
	// Detail is a short human-readable diagnostic, present whether OK is
	// true or false.
	Detail string `json:"detail,omitempty"`
}

// ReadyReport is GET /readyz's response body: every registered dependency
// check, aggregated. Mirrors internal/httpapi's readyReportDTO field for
// field.
type ReadyReport struct {
	// OK is true only if every registered check passed.
	OK     bool                   `json:"ok"`
	Checks map[string]CheckResult `json:"checks"`
}
