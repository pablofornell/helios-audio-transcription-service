package httpclient_test

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pablofornell/helios-audio-transcription-service/internal/domain"
)

func TestOpenAudio_ServesViaRange(t *testing.T) {
	id := mustID(t)
	content := []byte("0123456789ABCDEF")

	c, _ := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/v1/transcriptions/"+id+"/audio", r.URL.Path)
		w.Header().Set("Content-Disposition", `attachment; filename="source.mp3"`)
		w.Header().Set("Content-Type", "audio/mpeg")
		http.ServeContent(w, r, "source.mp3", time.Now(), bytes.NewReader(content))
	}))

	rc, info, err := c.OpenAudio(context.Background(), id)
	require.NoError(t, err)
	defer rc.Close()

	assert.Equal(t, "source.mp3", info.Filename)
	assert.Equal(t, "audio/mpeg", info.MediaType)
	assert.EqualValues(t, len(content), info.SizeBytes)

	data, err := io.ReadAll(rc)
	require.NoError(t, err)
	assert.Equal(t, content, data)
}

func TestOpenAudio_SeekAndReadFromMiddle(t *testing.T) {
	id := mustID(t)
	content := []byte("0123456789ABCDEF")

	c, _ := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "audio/mpeg")
		http.ServeContent(w, r, "source.mp3", time.Now(), bytes.NewReader(content))
	}))

	rc, _, err := c.OpenAudio(context.Background(), id)
	require.NoError(t, err)
	defer rc.Close()

	pos, err := rc.Seek(10, io.SeekStart)
	require.NoError(t, err)
	assert.EqualValues(t, 10, pos)

	buf := make([]byte, 4)
	n, err := io.ReadFull(rc, buf)
	require.NoError(t, err)
	assert.Equal(t, 4, n)
	assert.Equal(t, "ABCD", string(buf))
}

func TestOpenAudio_NotFound(t *testing.T) {
	id := mustID(t)
	c, _ := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeErrorEnvelope(t, w, http.StatusNotFound, domain.ErrCodeNotFound, "no such transcription")
	}))

	_, _, err := c.OpenAudio(context.Background(), id)
	require.Error(t, err)
	assert.Equal(t, domain.ErrCodeNotFound, errCode(t, err))
}
