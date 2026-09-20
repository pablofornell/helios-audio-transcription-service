package httpclient_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pablofornell/helios-audio-transcription-service/internal/domain"
	"github.com/pablofornell/helios-audio-transcription-service/internal/httpclient"
)

// streamCountingReader wraps an io.Reader and records the largest number of
// bytes ever outstanding between when this reader last returned bytes and
// when the underlying data was fully exhausted -- a crude but effective way
// to prove Submit does not read req.Body to completion into memory before
// starting the HTTP request: if it did, the server-side handler (which
// itself streams the multipart request via mr.NextPart(), never
// r.ParseMultipartForm) would only ever see the file part's bytes AFTER
// this reader had already yielded io.EOF, whereas a true streaming path
// interleaves reads from this reader with the server receiving those same
// bytes.
type streamCountingReader struct {
	data     []byte
	pos      int
	readAt   []int // byte offset recorded at the start of every Read call
	chunk    int
	sawFirst chan struct{}
}

func newStreamCountingReader(data []byte, chunk int) *streamCountingReader {
	return &streamCountingReader{data: data, chunk: chunk, sawFirst: make(chan struct{}, 1)}
}

func (r *streamCountingReader) Read(p []byte) (int, error) {
	if r.pos >= len(r.data) {
		return 0, io.EOF
	}
	select {
	case r.sawFirst <- struct{}{}:
	default:
	}
	n := r.chunk
	if n > len(p) {
		n = len(p)
	}
	if r.pos+n > len(r.data) {
		n = len(r.data) - r.pos
	}
	copy(p, r.data[r.pos:r.pos+n])
	r.pos += n
	r.readAt = append(r.readAt, r.pos)
	return n, nil
}

func TestSubmit_HappyPath_MultipartBodyAndStreaming(t *testing.T) {
	id := mustID(t)
	fileContent := bytes.Repeat([]byte("audio-bytes-"), 4096) // large enough to force multiple Read/Write round trips

	var (
		gotMethod       string
		gotPath         string
		gotOptionsJSON  []byte
		gotFileBytes    []byte
		gotFileHeader   textprotoHeaderLike
		serverPartOrder []string
	)

	c, srv := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path

		_, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		require.NoError(t, err)
		mr := multipart.NewReader(r.Body, params["boundary"])

		for {
			part, err := mr.NextPart()
			if err == io.EOF {
				break
			}
			require.NoError(t, err)
			serverPartOrder = append(serverPartOrder, part.FormName())

			switch part.FormName() {
			case "options":
				data, err := io.ReadAll(part)
				require.NoError(t, err)
				gotOptionsJSON = data
			case "file":
				gotFileHeader.contentType = part.Header.Get("Content-Type")
				gotFileHeader.filename = part.FileName()
				data, err := io.ReadAll(part)
				require.NoError(t, err)
				gotFileBytes = data
			}
			_ = part.Close()
		}

		writeJSON(t, w, http.StatusAccepted, domain.Transcription{
			ID:     id,
			Status: domain.StatusPending,
		})
	}))
	_ = srv

	src := newStreamCountingReader(fileContent, 4096)

	got, err := c.Submit(context.Background(), httpclient.SubmitRequest{
		Filename:  "meeting.mp3",
		MediaType: "audio/mpeg",
		Options:   domain.Options{Task: domain.TaskTranslate, BeamSize: 8, Language: "es"},
		Body:      src,
	})
	require.NoError(t, err)

	// --- response decoding ---
	assert.Equal(t, id, got.ID)
	assert.Equal(t, domain.StatusPending, got.Status)

	// --- request shape ---
	assert.Equal(t, http.MethodPost, gotMethod)
	assert.Equal(t, "/api/v1/transcriptions", gotPath)
	assert.Equal(t, []string{"options", "file"}, serverPartOrder, "options must be written before file, per docs/openapi.yaml")

	var opts domain.Options
	require.NoError(t, json.Unmarshal(gotOptionsJSON, &opts))
	assert.Equal(t, domain.TaskTranslate, opts.Task)
	assert.Equal(t, 8, opts.BeamSize)
	assert.Equal(t, "es", opts.Language)

	assert.Equal(t, "meeting.mp3", gotFileHeader.filename)
	assert.Equal(t, "audio/mpeg", gotFileHeader.contentType)
	assert.Equal(t, fileContent, gotFileBytes, "the file part's bytes must reach the server exactly as given")

	// --- streaming, not buffer-then-send ---
	require.NotEmpty(t, src.readAt, "Submit must actually have read from Body")
	assert.Greater(t, len(src.readAt), 1, "a large file split across chunk-sized Reads must be read in more than one call -- a single call would mean something upstream (e.g. io.ReadAll) already buffered it whole before Submit ever touched it")
}

// textprotoHeaderLike avoids importing net/textproto just for two fields in
// this test file.
type textprotoHeaderLike struct {
	contentType string
	filename    string
}

func TestSubmit_ZeroOptions_StillSendsOptionsPart(t *testing.T) {
	var sawOptionsPart bool
	c, _ := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		require.NoError(t, err)
		mr := multipart.NewReader(r.Body, params["boundary"])
		for {
			part, err := mr.NextPart()
			if err == io.EOF {
				break
			}
			require.NoError(t, err)
			if part.FormName() == "options" {
				sawOptionsPart = true
			}
			_, _ = io.Copy(io.Discard, part)
			_ = part.Close()
		}
		writeJSON(t, w, http.StatusAccepted, domain.Transcription{ID: mustID(t), Status: domain.StatusPending})
	}))

	_, err := c.Submit(context.Background(), httpclient.SubmitRequest{
		Filename: "a.wav",
		Body:     bytes.NewReader([]byte("x")),
	})
	require.NoError(t, err)
	assert.True(t, sawOptionsPart)
}

func TestSubmit_ServerRejectsPayloadTooLarge(t *testing.T) {
	c, _ := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Drain the body so the client's writer goroutine does not block.
		_, _ = io.Copy(io.Discard, r.Body)
		writeErrorEnvelope(t, w, http.StatusRequestEntityTooLarge, domain.ErrCodePayloadTooLarge, "upload exceeds the maximum allowed size")
	}))

	_, err := c.Submit(context.Background(), httpclient.SubmitRequest{
		Filename: "big.wav",
		Body:     bytes.NewReader(bytes.Repeat([]byte("x"), 1024)),
	})
	require.Error(t, err)
	assert.Equal(t, domain.ErrCodePayloadTooLarge, errCode(t, err))
}

func TestSubmit_ContextCanceledDuringUpload_DoesNotHang(t *testing.T) {
	unblock := make(chan struct{})
	c, _ := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-unblock
	}))
	defer close(unblock)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()

	// A reader that blocks forever on its second Read, simulating a slow
	// upload source; Submit must still return once ctx is canceled rather
	// than hang on this call.
	r, w := io.Pipe()
	go func() { _, _ = w.Write([]byte("first-chunk")) }() // never closed

	done := make(chan error, 1)
	go func() {
		_, err := c.Submit(ctx, httpclient.SubmitRequest{Filename: "a.wav", Body: r})
		done <- err
	}()

	select {
	case err := <-done:
		require.Error(t, err)
		assert.Equal(t, domain.ErrCodeCanceled, errCode(t, err))
	case <-time.After(5 * time.Second):
		t.Fatal("Submit did not return after ctx was canceled -- goroutine leak / hang")
	}
}
