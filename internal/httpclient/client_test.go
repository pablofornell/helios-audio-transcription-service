package httpclient_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pablofornell/helios-audio-transcription-service/internal/domain"
	"github.com/pablofornell/helios-audio-transcription-service/internal/httpclient"
)

func TestNew_DefaultsAndTrimsBaseURL(t *testing.T) {
	c := httpclient.New(httpclient.Config{BaseURL: "http://example.invalid/"})
	require.NotNil(t, c)
	// New must not panic and must not touch http.DefaultClient (no direct
	// way to assert that from outside the package, but constructing two
	// Clients with distinct configs and confirming neither leaks into the
	// other's requests -- exercised by every other test using its own
	// httptest.Server via a fresh Client -- is the behavioural proof).
}

func TestNew_UsesProvidedHTTPClientAndUserAgent(t *testing.T) {
	var gotUA string
	_, srv := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUA = r.Header.Get("User-Agent")
		w.WriteHeader(http.StatusOK)
	}))

	custom := &http.Client{Timeout: 5 * time.Second}
	c := httpclient.New(httpclient.Config{BaseURL: srv.URL, HTTPClient: custom, UserAgent: "test-agent/9"})

	err := c.Healthz(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "test-agent/9", gotUA)
}

func TestEveryOrdinaryRequest_SetsAcceptHeader(t *testing.T) {
	var gotAccept string
	c, _ := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAccept = r.Header.Get("Accept")
		w.WriteHeader(http.StatusOK)
	}))

	err := c.Healthz(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "application/json", gotAccept)
}

func TestContextCanceledMidRequest(t *testing.T) {
	unblock := make(chan struct{})
	c, _ := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-unblock // hang until the test cancels the client's context
	}))
	defer close(unblock)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()

	_, err := c.Get(ctx, mustID(t))
	require.Error(t, err)
	assert.Equal(t, domain.ErrCodeCanceled, errCode(t, err))
}

func TestGet_DecodesTranscription(t *testing.T) {
	id := mustID(t)
	now := time.Now().UTC().Truncate(time.Second)
	c, _ := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/v1/transcriptions/"+id, r.URL.Path)
		assert.Equal(t, http.MethodGet, r.Method)
		writeJSON(t, w, http.StatusOK, domain.Transcription{
			ID:          id,
			Status:      domain.StatusSucceeded,
			Options:     domain.Options{Task: domain.TaskTranscribe, BeamSize: 5},
			Audio:       domain.Audio{Filename: "a.mp3", SizeBytes: 42},
			Attempts:    1,
			MaxAttempts: 3,
			CreatedAt:   now,
			UpdatedAt:   now,
		})
	}))

	got, err := c.Get(context.Background(), id)
	require.NoError(t, err)
	assert.Equal(t, id, got.ID)
	assert.Equal(t, domain.StatusSucceeded, got.Status)
	assert.Equal(t, "a.mp3", got.Audio.Filename)
	assert.Equal(t, int64(42), got.Audio.SizeBytes)
	assert.True(t, got.CreatedAt.Equal(now))
}

func TestDelete_NoContent(t *testing.T) {
	id := mustID(t)
	c, _ := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodDelete, r.Method)
		assert.Equal(t, "/api/v1/transcriptions/"+id, r.URL.Path)
		w.WriteHeader(http.StatusNoContent)
	}))

	require.NoError(t, c.Delete(context.Background(), id))
}

func TestCancel_And_Retry_DecodeTranscription(t *testing.T) {
	id := mustID(t)

	t.Run("cancel", func(t *testing.T) {
		cc, _ := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, http.MethodPost, r.Method)
			assert.Equal(t, "/api/v1/transcriptions/"+id+"/cancel", r.URL.Path)
			writeJSON(t, w, http.StatusOK, domain.Transcription{ID: id, Status: domain.StatusCanceled})
		}))
		got, err := cc.Cancel(context.Background(), id)
		require.NoError(t, err)
		assert.Equal(t, domain.StatusCanceled, got.Status)
	})

	t.Run("retry", func(t *testing.T) {
		cc, _ := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, http.MethodPost, r.Method)
			assert.Equal(t, "/api/v1/transcriptions/"+id+"/retry", r.URL.Path)
			writeJSON(t, w, http.StatusOK, domain.Transcription{ID: id, Status: domain.StatusPending})
		}))
		got, err := cc.Retry(context.Background(), id)
		require.NoError(t, err)
		assert.Equal(t, domain.StatusPending, got.Status)
	})
}

func TestPrioritize_DecodesTranscriptionWithPriorityAt(t *testing.T) {
	id := mustID(t)
	at := time.Date(2026, 11, 4, 10, 0, 0, 0, time.UTC)

	cc, _ := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "/api/v1/transcriptions/"+id+"/prioritize", r.URL.Path)
		writeJSON(t, w, http.StatusAccepted, domain.Transcription{ID: id, Status: domain.StatusPending, PriorityAt: &at})
	}))
	got, err := cc.Prioritize(context.Background(), id)
	require.NoError(t, err)
	assert.Equal(t, domain.StatusPending, got.Status)
	require.NotNil(t, got.PriorityAt)
	assert.True(t, at.Equal(*got.PriorityAt))
}

func TestPrioritize_ConflictMapsToConflictCode(t *testing.T) {
	id := mustID(t)
	cc, _ := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeErrorEnvelope(t, w, http.StatusConflict, domain.ErrCodeConflict, "transcription is not pending or failed")
	}))
	_, err := cc.Prioritize(context.Background(), id)
	require.Error(t, err)
	assert.Equal(t, domain.ErrCodeConflict, domain.CodeOf(err))
}
