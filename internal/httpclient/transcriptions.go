package httpclient

import (
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"strconv"
	"strings"

	"github.com/pablofornell/helios-audio-transcription-service/internal/domain"
)

// filenameQuoteEscaper escapes '\' and '"' in a multipart Content-
// Disposition filename parameter, exactly as mime/multipart's own
// (unexported) CreateFormFile does -- reproduced here because this method
// needs a custom part header (to also set Content-Type from
// req.MediaType), so it cannot call CreateFormFile itself.
var filenameQuoteEscaper = strings.NewReplacer("\\", "\\\\", `"`, "\\\"")

// SubmitRequest is what a caller hands Submit: the raw upload metadata and
// body. It deliberately mirrors internal/app.SubmitRequest's shape (minus
// its Body's optional lateOptionsProvider capability, an internal/httpapi
// implementation detail of how a multipart request can legally arrive with
// its parts in either order -- Submit always writes `options` before
// `file`, so there is nothing for a remote caller to discover late).
type SubmitRequest struct {
	// Filename is the audio's original filename, sent as the `file` part's
	// filename. The server sanitizes it before storing or persisting it
	// anywhere; this client sends it as given.
	Filename string
	// MediaType is sent as the `file` part's Content-Type. Empty is fine --
	// the server falls back to sniffing by Filename's extension, and
	// finally to application/octet-stream.
	MediaType string
	// Options configures the job. The zero value is valid and applies
	// domain.DefaultOptions() server-side.
	Options domain.Options
	// Body is the audio file's bytes. Submit streams it directly into the
	// request -- it is never buffered in memory, whatever its size.
	Body io.Reader
}

// Submit implements POST /api/v1/transcriptions: it streams req.Body as the
// `file` part of a multipart/form-data request, preceded by an `options`
// part encoding req.Options as JSON -- docs/openapi.yaml requires `options`
// come before `file` when both are sent, and this client always sends
// both, so it always does.
//
// The multipart body is generated on the fly through an io.Pipe rather
// than built in a buffer first: req.Body's bytes flow straight from the
// caller's reader, through the multipart.Writer, into the HTTP request as
// http.Client reads it, so this method's memory use does not grow with the
// audio file's size.
//
// Unlike Get, List, Delete, Cancel and Retry, Submit does NOT bound the
// request with defaultRequestTimeout on top of ctx: an upload's transfer
// time is unrelated to how quickly the server responds and is often the
// single longest call this client makes, so the caller's own ctx is the
// only deadline that applies -- pass one sized for the file being sent, or
// context.Background() with a caller-managed cancellation, not
// context.TODO().
func (c *Client) Submit(ctx context.Context, req SubmitRequest) (*domain.Transcription, error) {
	pr, pw := io.Pipe()
	mw := multipart.NewWriter(pw)

	// stop stops the ctx-watcher goroutine below once this call is done
	// with pw, whether or not ctx ever fires -- without it, a Submit call
	// made with a long-lived or never-canceled ctx (context.Background())
	// would leak a goroutine parked on <-ctx.Done() forever.
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		select {
		case <-ctx.Done():
			// Unblocks a pw.Write currently stalled because nothing is
			// reading pr any more (e.g. the transport gave up on this
			// request once ctx ended) -- without this, the writer
			// goroutine below could otherwise block forever.
			_ = pw.CloseWithError(ctx.Err())
		case <-stop:
		}
	}()

	go func() {
		err := writeSubmitBody(mw, req)
		_ = pw.CloseWithError(err) // CloseWithError(nil) behaves like Close.
	}()

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/api/v1/transcriptions", pr)
	if err != nil {
		return nil, domain.Wrap(domain.ErrCodeInternal, "build request for POST /api/v1/transcriptions", err)
	}
	httpReq.Header.Set("Accept", "application/json")
	httpReq.Header.Set("User-Agent", c.userAgent)
	httpReq.Header.Set("Content-Type", mw.FormDataContentType())

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, classifyTransportErr(ctx, err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, classifyTransportErr(ctx, err)
	}

	if resp.StatusCode != http.StatusAccepted {
		return nil, classifyError(resp.StatusCode, respBody)
	}

	var t domain.Transcription
	if err := json.Unmarshal(respBody, &t); err != nil {
		return nil, domain.Wrap(domain.ErrCodeInternal, "decode response body from POST /api/v1/transcriptions", err)
	}
	return &t, nil
}

// writeSubmitBody writes req's `options` part (always, even when
// req.Options is the zero value -- an empty JSON object is exactly what
// docs/openapi.yaml documents as "applies domain.DefaultOptions()") and
// then its `file` part, copying req.Body straight into the multipart
// writer, and finally the multipart trailer. It is run on its own
// goroutine by Submit, writing into mw (backed by an io.Pipe) as
// http.Client reads the other end.
func writeSubmitBody(mw *multipart.Writer, req SubmitRequest) error {
	optionsJSON, err := json.Marshal(req.Options)
	if err != nil {
		return domain.Wrap(domain.ErrCodeInternal, "encode options part", err)
	}
	optionsPart, err := mw.CreateFormField("options")
	if err != nil {
		return domain.Wrap(domain.ErrCodeInternal, "open options part", err)
	}
	if _, err := optionsPart.Write(optionsJSON); err != nil {
		return domain.Wrap(domain.ErrCodeInternal, "write options part", err)
	}

	h := make(textproto.MIMEHeader)
	h.Set("Content-Disposition", `form-data; name="file"; filename="`+filenameQuoteEscaper.Replace(req.Filename)+`"`)
	if req.MediaType != "" {
		h.Set("Content-Type", req.MediaType)
	}
	filePart, err := mw.CreatePart(h)
	if err != nil {
		return domain.Wrap(domain.ErrCodeInternal, "open file part", err)
	}
	if req.Body != nil {
		if _, err := io.Copy(filePart, req.Body); err != nil {
			return domain.Wrap(domain.ErrCodeInternal, "stream file part", err)
		}
	}

	return mw.Close()
}

// Get implements GET /api/v1/transcriptions/{id}.
func (c *Client) Get(ctx context.Context, id string) (*domain.Transcription, error) {
	var t domain.Transcription
	if err := c.doJSON(ctx, http.MethodGet, "/api/v1/transcriptions/"+url.PathEscape(id), nil, &t, http.StatusOK); err != nil {
		return nil, err
	}
	return &t, nil
}

// List implements GET /api/v1/transcriptions with f's fields encoded as
// query parameters, per docs/openapi.yaml's listTranscriptions parameters.
func (c *Client) List(ctx context.Context, f domain.ListFilter) (domain.Page, error) {
	q := url.Values{}
	if f.Status != "" {
		q.Set("status", string(f.Status))
	}
	if f.Limit > 0 {
		q.Set("limit", strconv.Itoa(f.Limit))
	}
	if f.Cursor != "" {
		q.Set("cursor", f.Cursor)
	}

	path := "/api/v1/transcriptions"
	if encoded := q.Encode(); encoded != "" {
		path += "?" + encoded
	}

	var page domain.Page
	if err := c.doJSON(ctx, http.MethodGet, path, nil, &page, http.StatusOK); err != nil {
		return domain.Page{}, err
	}
	return page, nil
}

// Delete implements DELETE /api/v1/transcriptions/{id}. Per
// docs/openapi.yaml, deleting an id that does not exist is not an error --
// this returns nil either way, as long as id is well-formed.
func (c *Client) Delete(ctx context.Context, id string) error {
	return c.doJSON(ctx, http.MethodDelete, "/api/v1/transcriptions/"+url.PathEscape(id), nil, nil, http.StatusNoContent)
}

// Cancel implements POST /api/v1/transcriptions/{id}/cancel.
func (c *Client) Cancel(ctx context.Context, id string) (*domain.Transcription, error) {
	var t domain.Transcription
	if err := c.doJSON(ctx, http.MethodPost, "/api/v1/transcriptions/"+url.PathEscape(id)+"/cancel", nil, &t, http.StatusOK); err != nil {
		return nil, err
	}
	return &t, nil
}

// Retry implements POST /api/v1/transcriptions/{id}/retry.
func (c *Client) Retry(ctx context.Context, id string) (*domain.Transcription, error) {
	var t domain.Transcription
	if err := c.doJSON(ctx, http.MethodPost, "/api/v1/transcriptions/"+url.PathEscape(id)+"/retry", nil, &t, http.StatusOK); err != nil {
		return nil, err
	}
	return &t, nil
}
