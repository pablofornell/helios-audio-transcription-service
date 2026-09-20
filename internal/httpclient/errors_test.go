package httpclient_test

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pablofornell/helios-audio-transcription-service/internal/domain"
)

// statusTable mirrors internal/httpapi/errors.go's statusForCode table
// exactly (see docs/openapi.yaml's info.description for the same table).
// Every row is tested here as its own subtest: whatever status and
// ErrorCode the server started with, this client must reconstruct that
// exact ErrorCode back -- the round trip the whole error envelope exists
// for.
var statusTable = []struct {
	code   domain.ErrorCode
	status int
}{
	{domain.ErrCodeInvalidInput, http.StatusBadRequest},
	{domain.ErrCodeUnsupportedMedia, http.StatusUnsupportedMediaType},
	{domain.ErrCodeNoAudioStream, http.StatusUnprocessableEntity},
	{domain.ErrCodePayloadTooLarge, http.StatusRequestEntityTooLarge},
	{domain.ErrCodeNotFound, http.StatusNotFound},
	{domain.ErrCodeConflict, http.StatusConflict},
	{domain.ErrCodeDiskFull, http.StatusInsufficientStorage},
	{domain.ErrCodeModelUnavailable, http.StatusServiceUnavailable},
	{domain.ErrCodeToolFailure, http.StatusBadGateway},
	{domain.ErrCodeTimeout, http.StatusGatewayTimeout},
	{domain.ErrCodeCanceled, 499},
	{domain.ErrCodeInternal, http.StatusInternalServerError},
}

func TestErrorRoundTrip_EveryStatusTableRow(t *testing.T) {
	for _, row := range statusTable {
		t.Run(string(row.code), func(t *testing.T) {
			c, _ := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				writeJSON(t, w, row.status, map[string]any{
					"error": map[string]any{
						"code":    string(row.code),
						"message": fmt.Sprintf("classified as %s", row.code),
						"details": map[string]any{"field": "value"},
					},
				})
			}))

			_, err := c.Get(context.Background(), mustID(t))
			require.Error(t, err)

			var derr *domain.Error
			require.ErrorAs(t, err, &derr)
			assert.Equal(t, row.code, derr.Code,
				"HTTP status %d carrying error.code=%q must round-trip to the SAME code, not whatever fallbackCodeForStatus would guess from the status alone", row.status, row.code)
			assert.Equal(t, fmt.Sprintf("classified as %s", row.code), derr.Message)
			assert.Equal(t, "value", derr.Details["field"])
		})
	}
}

func TestErrorRoundTrip_FallbackByStatus_NoEnvelope(t *testing.T) {
	// Every row's status, absent an error envelope entirely, must still
	// classify to the same code via fallbackCodeForStatus -- proving the
	// fallback table is not just present but correctly wired to every
	// status this API actually uses.
	for _, row := range statusTable {
		t.Run(string(row.code), func(t *testing.T) {
			c, _ := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/plain")
				w.WriteHeader(row.status)
				_, _ = w.Write([]byte("not an envelope"))
			}))

			_, err := c.Get(context.Background(), mustID(t))
			require.Error(t, err)
			assert.Equal(t, row.code, errCode(t, err))
		})
	}
}

func TestClassifyError_MalformedNonJSONBody(t *testing.T) {
	c, _ := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("not json at all, just a stack trace or whatever"))
	}))

	_, err := c.Get(context.Background(), mustID(t))
	require.Error(t, err)

	var derr *domain.Error
	require.ErrorAs(t, err, &derr)
	assert.Equal(t, domain.ErrCodeInternal, derr.Code)
	assert.Contains(t, derr.Message, "not json at all")
}

func TestClassifyError_ProxyHTML502(t *testing.T) {
	const html = `<html><head><title>502 Bad Gateway</title></head><body><center>nginx</center></body></html>`
	c, _ := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(html))
	}))

	_, err := c.Get(context.Background(), mustID(t))
	require.Error(t, err)

	var derr *domain.Error
	require.ErrorAs(t, err, &derr)
	assert.Equal(t, domain.ErrCodeToolFailure, derr.Code, "a 502 with no envelope must still classify per fallbackCodeForStatus, never panic or a bare string")
	assert.Contains(t, derr.Message, "502 Bad Gateway")
}

func TestClassifyError_TruncatedEmptyBody(t *testing.T) {
	c, _ := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		// No body at all.
	}))

	_, err := c.Get(context.Background(), mustID(t))
	require.Error(t, err)

	var derr *domain.Error
	require.ErrorAs(t, err, &derr)
	assert.Equal(t, domain.ErrCodeModelUnavailable, derr.Code)
	assert.Contains(t, derr.Message, "no error body")
}

func TestClassifyError_UnrecognisedStatus_FailsClosedToInternal(t *testing.T) {
	c, _ := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTeapot) // not in fallbackCodeForStatus at all
	}))

	_, err := c.Get(context.Background(), mustID(t))
	require.Error(t, err)
	assert.Equal(t, domain.ErrCodeInternal, errCode(t, err))
}

func TestClassifyTransportErr_UnreachableServer(t *testing.T) {
	c, srv := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	srv.Close() // now genuinely unreachable

	_, err := c.Get(context.Background(), mustID(t))
	require.Error(t, err)

	// A raw dial failure must be classified, never leak its (potentially
	// implementation-revealing) raw text as the whole story -- it must at
	// least carry a domain.ErrorCode a caller can branch on. Specifically
	// ErrCodeNetwork, not ErrCodeInternal: "I cannot reach the server" and
	// "the server has a bug" are different situations to a caller, and the
	// desktop client shows a different screen for each.
	var derr *domain.Error
	require.ErrorAs(t, err, &derr)
	assert.Equal(t, domain.ErrCodeNetwork, derr.Code)
	assert.NotEmpty(t, derr.Message)
	assert.True(t, derr.Code.Retryable(), "an unreachable server is worth retrying")
}
