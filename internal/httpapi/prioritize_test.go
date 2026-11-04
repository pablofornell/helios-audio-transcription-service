package httpapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pablofornell/helios-audio-transcription-service/internal/domain"
	"github.com/pablofornell/helios-audio-transcription-service/internal/httpapi"
	"github.com/pablofornell/helios-audio-transcription-service/internal/observability"
)

func postPrioritize(t *testing.T, svc *fakeService, id string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/transcriptions/"+id+"/prioritize", nil)
	httpapi.NewRouter(testDeps(svc, observability.NewRegistry(), false)).ServeHTTP(rec, req)
	return rec
}

func TestPrioritizeTranscription_AcceptedWithPriorityAt(t *testing.T) {
	at := time.Date(2026, 11, 4, 10, 0, 0, 0, time.UTC)
	var gotID string
	svc := &fakeService{prioritizeFn: func(ctx context.Context, id string) (*domain.Transcription, error) {
		gotID = id
		return &domain.Transcription{ID: id, Status: domain.StatusPending, PriorityAt: &at}, nil
	}}

	rec := postPrioritize(t, svc, "11111111-1111-4111-8111-111111111111")

	require.Equal(t, http.StatusAccepted, rec.Code)
	assert.Equal(t, "11111111-1111-4111-8111-111111111111", gotID)
	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, "pending", body["status"])
	assert.Equal(t, "2026-11-04T10:00:00Z", body["priorityAt"])
}

func TestPrioritizeTranscription_OmitsPriorityAtWhenUnset(t *testing.T) {
	svc := &fakeService{}
	rec := postPrioritize(t, svc, "11111111-1111-4111-8111-111111111111")

	require.Equal(t, http.StatusAccepted, rec.Code)
	assert.NotContains(t, rec.Body.String(), "priorityAt")
}

func TestPrioritizeTranscription_MapsErrors(t *testing.T) {
	cases := []struct {
		name string
		code domain.ErrorCode
		want int
	}{
		{"conflict", domain.ErrCodeConflict, http.StatusConflict},
		{"not found", domain.ErrCodeNotFound, http.StatusNotFound},
		{"invalid id", domain.ErrCodeInvalidInput, http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc := &fakeService{prioritizeFn: func(ctx context.Context, id string) (*domain.Transcription, error) {
				return nil, domain.New(tc.code, "nope")
			}}
			rec := postPrioritize(t, svc, "11111111-1111-4111-8111-111111111111")
			assert.Equal(t, tc.want, rec.Code)
			var env errorEnvelope
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &env))
			assert.Equal(t, string(tc.code), env.Error.Code)
		})
	}
}
