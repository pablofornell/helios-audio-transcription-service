package httpclient_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/pablofornell/helios-audio-transcription-service/internal/domain"
	"github.com/pablofornell/helios-audio-transcription-service/internal/httpclient"
)

// newTestServer starts an httptest.Server running handler and returns a
// Client pointed at it, alongside the server itself so a test can close it
// (or inspect requests it received). Every test in this package uses
// httptest -- no test dials a real network address.
func newTestServer(t *testing.T, handler http.Handler) (*httpclient.Client, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	c := httpclient.New(httpclient.Config{BaseURL: srv.URL})
	return c, srv
}

// writeJSON is the test-side twin of httpapi's own writeJSON: every fixture
// server response in this package's tests goes through it, so every test
// exercises exactly the same JSON encoding path a real helios-ats server
// would.
func writeJSON(t *testing.T, w http.ResponseWriter, status int, v any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	require.NoError(t, json.NewEncoder(w).Encode(v))
}

// writeErrorEnvelope writes the `{"error":{"code","message","details"}}`
// envelope every non-2xx JSON response in this API uses.
func writeErrorEnvelope(t *testing.T, w http.ResponseWriter, status int, code domain.ErrorCode, message string) {
	t.Helper()
	writeJSON(t, w, status, map[string]any{
		"error": map[string]any{"code": string(code), "message": message},
	})
}

// mustID returns a fresh UUID string -- the shape of a real transcription
// id, but no test in this package depends on it being one specifically
// (domain.Transcription.ID is an opaque string on the wire).
func mustID(t *testing.T) string {
	t.Helper()
	return uuid.NewString()
}

// errCode extracts the domain.ErrorCode a *domain.Error carries, failing
// the calling test loudly if err is not one -- every method under test here
// is documented to return one, so a failure here would mean httpclient
// itself is returning a bare error, which is exactly the bug this
// package's error-mapping tests exist to catch.
func errCode(t *testing.T, err error) domain.ErrorCode {
	t.Helper()
	var derr *domain.Error
	require.ErrorAs(t, err, &derr, "expected a *domain.Error, got %T: %v", err, err)
	return derr.Code
}
