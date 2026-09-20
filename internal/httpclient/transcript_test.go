package httpclient_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pablofornell/helios-audio-transcription-service/internal/domain"
	"github.com/pablofornell/helios-audio-transcription-service/internal/transcript"
)

func TestGetTranscript_AllFourFormats(t *testing.T) {
	id := mustID(t)

	cases := []struct {
		format      transcript.Format
		contentType string
		body        string
	}{
		{transcript.FormatJSON, "application/json", `{"text":"hello world","language":"en","durationSeconds":1.5,"model":"base","task":"transcribe","segments":[]}`},
		{transcript.FormatText, "text/plain; charset=utf-8", "hello world\n"},
		{transcript.FormatSRT, "application/x-subrip; charset=utf-8", "1\r\n00:00:00,000 --> 00:00:01,500\r\nhello world\r\n\r\n"},
		{transcript.FormatVTT, "text/vtt; charset=utf-8", "WEBVTT\n\n00:00:00.000 --> 00:00:01.500\nhello world\n"},
	}

	for _, tc := range cases {
		t.Run(string(tc.format), func(t *testing.T) {
			var gotQuery string
			c, _ := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, "/api/v1/transcriptions/"+id+"/transcript", r.URL.Path)
				gotQuery = r.URL.Query().Get("format")
				w.Header().Set("Content-Type", tc.contentType)
				if tc.format != transcript.FormatJSON {
					w.Header().Set("Content-Disposition", `attachment; filename="`+id+"."+string(tc.format)+`"`)
				}
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(tc.body))
			}))

			body, err := c.GetTranscript(context.Background(), id, tc.format)
			require.NoError(t, err)
			assert.Equal(t, string(tc.format), gotQuery)
			assert.Equal(t, tc.body, string(body))
		})
	}
}

func TestGetTranscript_EmptyFormat_NoQueryParam(t *testing.T) {
	id := mustID(t)
	c, _ := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Empty(t, r.URL.RawQuery)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"text":"x","segments":[]}`))
	}))

	body, err := c.GetTranscript(context.Background(), id, "")
	require.NoError(t, err)
	assert.Equal(t, `{"text":"x","segments":[]}`, string(body))
}

func TestGetTranscriptResult_DecodesResult(t *testing.T) {
	id := mustID(t)
	c, _ := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "json", r.URL.Query().Get("format"))
		writeJSON(t, w, http.StatusOK, domain.Result{
			Text:            "hello world",
			Language:        "en",
			DurationSeconds: 1.5,
			Model:           "base",
			Task:            domain.TaskTranscribe,
			Segments: []domain.Segment{
				{ID: 0, Start: 0, End: 1.5, Text: "hello world"},
			},
		})
	}))

	result, err := c.GetTranscriptResult(context.Background(), id)
	require.NoError(t, err)
	assert.Equal(t, "hello world", result.Text)
	assert.Equal(t, "en", result.Language)
	assert.Equal(t, "base", result.Model)
	require.Len(t, result.Segments, 1)
	assert.Equal(t, "hello world", result.Segments[0].Text)
}

func TestGetTranscript_ConflictNotSucceeded(t *testing.T) {
	id := mustID(t)
	c, _ := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeErrorEnvelope(t, w, http.StatusConflict, domain.ErrCodeConflict, "transcription has not succeeded")
	}))

	_, err := c.GetTranscript(context.Background(), id, transcript.FormatJSON)
	require.Error(t, err)
	assert.Equal(t, domain.ErrCodeConflict, errCode(t, err))
}
