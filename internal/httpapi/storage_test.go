package httpapi_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pablofornell/helios-audio-transcription-service/internal/httpapi"
	"github.com/pablofornell/helios-audio-transcription-service/internal/observability"
)

type storageSpaceFake struct {
	free uint64
	err  error
}

func (s storageSpaceFake) FreeBytes() (uint64, error) { return s.free, s.err }

func TestStorageReportsServerStoreSpace(t *testing.T) {
	deps := testDeps(&fakeService{}, observability.NewRegistry(), false)
	deps.Storage = storageSpaceFake{free: 123456789}
	rec := httptest.NewRecorder()
	httpapi.NewRouter(deps).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/storage", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	var body struct {
		FreeBytes uint64 `json:"freeBytes"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.Equal(t, uint64(123456789), body.FreeBytes)
}

func TestStorageReadFailureDoesNotReportZero(t *testing.T) {
	deps := testDeps(&fakeService{}, observability.NewRegistry(), false)
	deps.Storage = storageSpaceFake{err: errors.New("volume unavailable")}
	rec := httptest.NewRecorder()
	httpapi.NewRouter(deps).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/storage", nil))
	require.Equal(t, http.StatusInternalServerError, rec.Code)
	require.NotContains(t, rec.Body.String(), "freeBytes")
	require.NotContains(t, rec.Body.String(), "volume unavailable")
}
