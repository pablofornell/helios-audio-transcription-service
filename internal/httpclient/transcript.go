package httpclient

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"

	"github.com/pablofornell/helios-audio-transcription-service/internal/domain"
	"github.com/pablofornell/helios-audio-transcription-service/internal/transcript"
)

// GetTranscript implements GET /api/v1/transcriptions/{id}/transcript?format=,
// returning the response body verbatim -- the plain-text transcript for
// transcript.FormatText, a SubRip file for transcript.FormatSRT, a WebVTT
// file for transcript.FormatVTT, or the JSON-encoded domain.Result for
// transcript.FormatJSON (an empty format also means json, matching
// docs/openapi.yaml's default). Passing an unrecognised format is not
// rejected client-side -- the server validates it and this call surfaces
// whatever *domain.Error{Code: ErrCodeInvalidInput} it returns, exactly as
// for any other invalid request.
//
// For FormatJSON, GetTranscriptResult decodes the body for you; this
// method exists for every format because txt/srt/vtt have no domain type
// to decode into -- a caller wanting to save them to disk wants exactly
// these bytes.
func (c *Client) GetTranscript(ctx context.Context, id string, format transcript.Format) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, defaultRequestTimeout)
	defer cancel()

	path := "/api/v1/transcriptions/" + url.PathEscape(id) + "/transcript"
	if format != "" {
		path += "?" + url.Values{"format": {string(format)}}.Encode()
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return nil, domain.Wrap(domain.ErrCodeInternal, "build request for GET "+path, err)
	}
	// Unlike doJSON's requests, Accept is deliberately "*/*": the response
	// may be text/plain, application/x-subrip, text/vtt or application/json
	// depending on format, and the server selects among them from the
	// `format` query parameter alone (see internal/httpapi's getTranscript),
	// never by content negotiation.
	req.Header.Set("Accept", "*/*")
	req.Header.Set("User-Agent", c.userAgent)

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, classifyTransportErr(ctx, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, classifyTransportErr(ctx, err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, classifyError(resp.StatusCode, body)
	}
	return body, nil
}

// GetTranscriptResult is a convenience wrapper around GetTranscript for
// transcript.FormatJSON: it decodes the response straight into a
// *domain.Result, whose json tags already match what GET
// .../transcript?format=json returns field for field (see
// internal/httpapi's resultDTO).
func (c *Client) GetTranscriptResult(ctx context.Context, id string) (*domain.Result, error) {
	body, err := c.GetTranscript(ctx, id, transcript.FormatJSON)
	if err != nil {
		return nil, err
	}
	var r domain.Result
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, domain.Wrap(domain.ErrCodeInternal, "decode transcript result", err)
	}
	return &r, nil
}
