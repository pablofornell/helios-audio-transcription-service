package httpclient_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pablofornell/helios-audio-transcription-service/internal/domain"
)

func TestList_EncodesFilterAsQueryParams(t *testing.T) {
	var gotQuery string
	c, _ := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/v1/transcriptions", r.URL.Path)
		gotQuery = r.URL.RawQuery
		writeJSON(t, w, http.StatusOK, domain.Page{})
	}))

	_, err := c.List(context.Background(), domain.ListFilter{
		Library: true,
		Status:  domain.StatusFailed,
		Cursor:  "some-opaque-cursor",
		Limit:   7,
	})
	require.NoError(t, err)

	q := gotQuery
	assert.Contains(t, q, "status=failed")
	assert.Contains(t, q, "cursor=some-opaque-cursor")
	assert.Contains(t, q, "limit=7")
	assert.Contains(t, q, "library=true")
}

func TestList_ZeroFilter_NoQueryString(t *testing.T) {
	var gotRawQuery string
	c, _ := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotRawQuery = r.URL.RawQuery
		writeJSON(t, w, http.StatusOK, domain.Page{})
	}))

	_, err := c.List(context.Background(), domain.ListFilter{})
	require.NoError(t, err)
	assert.Empty(t, gotRawQuery)
}

func TestList_DecodesPageWithNextCursor(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	c, _ := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, http.StatusOK, map[string]any{
			"items": []domain.Transcription{
				{ID: "id-1", Status: domain.StatusPending, CreatedAt: now, UpdatedAt: now},
				{ID: "id-2", Status: domain.StatusSucceeded, CreatedAt: now, UpdatedAt: now},
			},
			"nextCursor": "opaque-next-page-token",
		})
	}))

	page, err := c.List(context.Background(), domain.ListFilter{Limit: 2})
	require.NoError(t, err)
	require.Len(t, page.Items, 2)
	assert.Equal(t, "id-1", page.Items[0].ID)
	assert.Equal(t, "id-2", page.Items[1].ID)
	assert.Equal(t, "opaque-next-page-token", page.NextCursor)
}

func TestList_ExhaustedListing_EmptyNextCursor(t *testing.T) {
	c, _ := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, http.StatusOK, map[string]any{"items": []domain.Transcription{}})
	}))

	page, err := c.List(context.Background(), domain.ListFilter{})
	require.NoError(t, err)
	assert.Empty(t, page.Items)
	assert.Empty(t, page.NextCursor)
}

func TestList_InvalidInputError(t *testing.T) {
	c, _ := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeErrorEnvelope(t, w, http.StatusBadRequest, domain.ErrCodeInvalidInput, `cursor "x" is not a token this server issued`)
	}))

	_, err := c.List(context.Background(), domain.ListFilter{Cursor: "x"})
	require.Error(t, err)
	assert.Equal(t, domain.ErrCodeInvalidInput, errCode(t, err))
}
