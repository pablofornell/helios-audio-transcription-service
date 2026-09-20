package httpclient_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pablofornell/helios-audio-transcription-service/internal/domain"
)

func TestHealthz_OK(t *testing.T) {
	c, _ := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/healthz", r.URL.Path)
		w.WriteHeader(http.StatusOK)
	}))

	require.NoError(t, c.Healthz(context.Background()))
}

func TestReadyz_OK(t *testing.T) {
	c, _ := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/readyz", r.URL.Path)
		writeJSON(t, w, http.StatusOK, map[string]any{
			"ok": true,
			"checks": map[string]any{
				"database": map[string]any{"ok": true, "detail": "connected"},
			},
		})
	}))

	report, err := c.Readyz(context.Background())
	require.NoError(t, err)
	assert.True(t, report.OK)
	assert.True(t, report.Checks["database"].OK)
	assert.Equal(t, "connected", report.Checks["database"].Detail)
}

func TestReadyz_NotReady_StillDecodesReport(t *testing.T) {
	c, _ := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, http.StatusServiceUnavailable, map[string]any{
			"ok": false,
			"checks": map[string]any{
				"database": map[string]any{"ok": false, "detail": "connection refused"},
			},
		})
	}))

	report, err := c.Readyz(context.Background())
	require.NoError(t, err, "a 503 with a valid ReadyReport body is not an error -- report.OK carries the signal")
	assert.False(t, report.OK)
	assert.False(t, report.Checks["database"].OK)
}

func TestHealthz_TransportFailure(t *testing.T) {
	c, srv := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	srv.Close()

	err := c.Healthz(context.Background())
	require.Error(t, err)
	// An unreachable server is a network failure, not an internal one --
	// this is the signal the desktop connection indicator polls for.
	assert.Equal(t, domain.ErrCodeNetwork, errCode(t, err))
}
