// Package httpclient is a typed Go client for HELIOS-ATS's own REST API
// (docs/openapi.yaml), for callers outside this process -- principally the
// desktop app, which talks to a running helios-transcription-api the same
// way any other caller of the documented HTTP contract would. It performs
// no business logic of its own: every method here does nothing but build a
// request, issue it, and translate the response (or a transport failure)
// into either the matching internal/domain type or a *domain.Error a
// caller can branch on by Code.
//
// Wire shapes: wherever internal/httpapi's DTOs are field-for-field
// identical to the internal/domain type they mirror (true for Options,
// Audio, Word, Segment, Result, Error, Transcription and Page -- see each
// domain type's json tags), this package decodes straight into the domain
// type instead of duplicating it as a client-local DTO. Only where the
// wire shape has no domain equivalent (the /readyz report) does this
// package declare its own.
package httpclient

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/pablofornell/helios-audio-transcription-service/internal/domain"
)

// defaultUserAgent is used when Config.UserAgent is empty.
const defaultUserAgent = "helios-ats-httpclient/1"

// defaultRequestTimeout bounds every ordinary (non-streaming) call this
// client makes -- Get, List, Delete, Cancel, Retry, GetTranscript, Healthz
// and Readyz -- on top of whatever deadline ctx already carries. It does
// NOT apply to Submit (an upload's transfer time is unrelated to server
// responsiveness, and is bounded by ctx alone -- see Submit's own doc) or
// to OpenAudio's Range requests, for the same reason; see those methods'
// doc comments.
const defaultRequestTimeout = 30 * time.Second

// maxErrorBodyBytes caps how much of a non-envelope error body (e.g. a
// proxy's HTML page) this client reads before giving up and classifying the
// failure -- a malformed or hostile response must never make a caller
// buffer an unbounded body just to report an error.
const maxErrorBodyBytes = 4096

// Config configures a Client.
type Config struct {
	// BaseURL is the helios-transcription-api's root, e.g.
	// "http://localhost:7462" -- NOT including "/api/v1". Per
	// docs/openapi.yaml, /healthz and /readyz are served at the root while
	// every other endpoint lives under /api/v1; Client builds both kinds
	// of path from this same root. A trailing slash is tolerated and
	// stripped.
	BaseURL string
	// HTTPClient is the transport used for every request. If nil, New
	// constructs a fresh *http.Client -- never http.DefaultClient, so one
	// Client's configuration (proxies, TLS, cookies, ...) can never leak
	// into another's.
	HTTPClient *http.Client
	// UserAgent is sent as the User-Agent header on every request. Defaults
	// to defaultUserAgent when empty.
	UserAgent string
}

// Client is a REST client for the HELIOS-ATS transcription API. It holds no
// business-logic state: every field is fixed configuration. A Client is
// safe for concurrent use by multiple goroutines.
type Client struct {
	baseURL   string
	http      *http.Client
	userAgent string
}

// New returns a ready-to-use Client for cfg.
func New(cfg Config) *Client {
	hc := cfg.HTTPClient
	if hc == nil {
		hc = &http.Client{}
	}
	ua := cfg.UserAgent
	if ua == "" {
		ua = defaultUserAgent
	}
	return &Client{
		baseURL:   strings.TrimRight(cfg.BaseURL, "/"),
		http:      hc,
		userAgent: ua,
	}
}

// newRequest builds a request against path (which must start with "/"),
// with Accept and User-Agent set per this package's contract, and a JSON
// body when reqBody is non-nil.
func (c *Client) newRequest(ctx context.Context, method, path string, reqBody any) (*http.Request, error) {
	var body io.Reader
	if reqBody != nil {
		b, err := json.Marshal(reqBody)
		if err != nil {
			return nil, domain.Wrap(domain.ErrCodeInternal, "encode request body", err)
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
	if err != nil {
		return nil, domain.Wrap(domain.ErrCodeInternal, "build request for "+method+" "+path, err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", c.userAgent)
	if reqBody != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return req, nil
}

// doJSON issues method/path (with reqBody, if any, JSON-encoded), bounded by
// defaultRequestTimeout on top of ctx, and decodes the response into out
// when the status is one of okStatuses and a body is present. Any other
// status is turned into a *domain.Error via classifyError; a transport
// failure (including ctx expiry/cancellation) is turned into one via
// classifyTransportErr. Callers never see a bare error.
func (c *Client) doJSON(ctx context.Context, method, path string, reqBody, out any, okStatuses ...int) error {
	ctx, cancel := context.WithTimeout(ctx, defaultRequestTimeout)
	defer cancel()

	req, err := c.newRequest(ctx, method, path, reqBody)
	if err != nil {
		return err
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return classifyTransportErr(ctx, err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return classifyTransportErr(ctx, err)
	}

	if !statusIn(resp.StatusCode, okStatuses) {
		return classifyError(resp.StatusCode, respBody)
	}

	if out != nil && len(respBody) > 0 {
		if err := json.Unmarshal(respBody, out); err != nil {
			return domain.Wrap(domain.ErrCodeInternal, "decode response body from "+method+" "+path, err)
		}
	}
	return nil
}

func statusIn(status int, okStatuses []int) bool {
	for _, s := range okStatuses {
		if status == s {
			return true
		}
	}
	return false
}
