package e2e

import (
	"bytes"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pablofornell/helios-audio-transcription-service/internal/domain"
)

// TestPayloadTooLarge validates the http.MaxBytesError -> domain error
// bridge through the REAL service and store (see
// internal/httpapi/handlers.go's classifyBodyErr doc): a dedicated stack
// with a tiny HTTP.MaxUploadBytes, an upload larger than it, and a 413
// with code payload_too_large. This is specifically the seam a unit test
// against a faked httpapi.Service could only fake -- here MaxBytesReader
// really does trip while app.Service.Submit is really streaming into a
// real localfs.Store.
func TestPayloadTooLarge(t *testing.T) {
	requireDocker(t)
	requireFFmpeg(t)

	const tinyMaxUpload = 1024 // 1 KiB: smaller than fixtureGoodMP3.
	ts := newTestServer(t, withMaxUploadBytes(tinyMaxUpload))

	oversized := bytes.Repeat([]byte("x"), tinyMaxUpload*4)
	contentType, body := buildUpload(t, "oversized.mp3", "audio/mpeg", oversized, nil)

	req, err := http.NewRequest(http.MethodPost, ts.base+"/api/v1/transcriptions", bytes.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", contentType)

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	require.Equal(t, http.StatusRequestEntityTooLarge, resp.StatusCode)
	derr := decodeError(t, resp)
	assert.Equal(t, domain.ErrCodePayloadTooLarge, derr.Code)
}

// TestPayloadTooLarge_WithinLimitStillSucceeds is TestPayloadTooLarge's
// negative control: the same tiny-limit stack, but a body that fits, still
// runs the real pipeline to completion -- proving the tiny MaxUploadBytes
// alone did not break the happy path.
func TestPayloadTooLarge_WithinLimitStillSucceeds(t *testing.T) {
	requireDocker(t)
	requireFFmpeg(t)

	// fixtureGoodMP3 is a real, if small, MP3; give it comfortable headroom
	// well above its own size but still far below a real deployment's
	// default (2 GiB).
	const smallButSufficient = 256 << 10 // 256 KiB
	ts := newTestServer(t, withMaxUploadBytes(smallButSufficient))

	created := submitFile(t, ts.base, fixtureGoodMP3, "audio/mpeg", nil)
	final := waitForStatus(t, ts.base, created.ID, domain.StatusSucceeded, terminalDeadline)
	require.NotNil(t, final.Result)
}

// Exercise the real HTTP handler and service with the production model policy.
func TestUnsupportedModelsRejected(t *testing.T) {
	requireDocker(t)
	ts := newTestServer(t)
	for _, model := range []string{"base", "small", "medium", "large-v3", "large-v3-turbo"} {
		t.Run(model, func(t *testing.T) {
			contentType, body := buildUpload(t, "audio.wav", "audio/wav", []byte("audio"), &domain.Options{Model: model})
			req, err := http.NewRequest(http.MethodPost, ts.base+"/api/v1/transcriptions", bytes.NewReader(body))
			require.NoError(t, err)
			req.Header.Set("Content-Type", contentType)
			resp, err := http.DefaultClient.Do(req)
			require.NoError(t, err)
			require.Equal(t, http.StatusBadRequest, resp.StatusCode)
			derr := decodeError(t, resp)
			assert.Equal(t, domain.ErrCodeInvalidInput, derr.Code)
			assert.Equal(t, "model", derr.Details["field"])
		})
	}
}
