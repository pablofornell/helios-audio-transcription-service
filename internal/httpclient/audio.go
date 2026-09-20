package httpclient

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/pablofornell/helios-audio-transcription-service/internal/domain"
)

// maxDiscardSkip bounds how far forward a Seek is served by discarding
// bytes on the currently open connection rather than opening a new Range
// request. Beyond this, discarding would itself cost as much network
// traffic as just re-requesting from the new position, so
// audioReadSeekCloser closes the old response (letting its connection
// return to the transport's pool once fully drained) and opens a fresh one
// instead.
const maxDiscardSkip = 1 << 20 // 1 MiB

// AudioInfo describes the audio OpenAudio just opened, reconstructed from
// the response's headers rather than from a domain.Audio row -- this
// client never sees one, only the bytes and headers GET .../audio sends.
type AudioInfo struct {
	// Filename is the basename http.ServeContent reported via
	// Content-Disposition -- best-effort, not guaranteed to equal the
	// stored domain.Audio.Filename exactly (see getAudio's own doc: the
	// server always sends one, since it always sets Content-Disposition
	// itself).
	Filename string
	// MediaType is the response's Content-Type -- the server's
	// safeAudioContentType-downgraded value, never the raw caller-supplied
	// one (see internal/httpapi's getAudio doc).
	MediaType string
	// SizeBytes is the audio file's total size, from Content-Range (for a
	// 206) or Content-Length (for a 200).
	SizeBytes int64
	// ModTime is the response's Last-Modified header, when present --
	// http.ServeContent sets it from the transcription's CreatedAt.
	ModTime time.Time
}

// OpenAudio implements GET /api/v1/transcriptions/{id}/audio, once
// immediately (to learn the file's size and content type) and lazily
// thereafter as the returned io.ReadSeekCloser is used.
//
// HTTP has no seek primitive, so the returned reader is built on Range
// requests: a Seek only updates a logical position, and the next Read
// issues (or reuses -- see maxDiscardSkip) a "Range: bytes=N-" request for
// wherever that position now is. docs/openapi.yaml documents this endpoint
// as always supporting Range (via net/http's ServeContent); if some
// intermediary in front of it ever did not honour Range (a 200 instead of
// a 206), this falls back to buffering the whole file in memory once and
// serving every further Read/Seek from that buffer.
//
// The returned reader's Read and Seek are bound to ctx -- the one
// OpenAudio was called with, captured at construction. Canceling (or
// letting expire) that ctx after OpenAudio itself returned successfully
// WILL cause later Read/Seek-triggered requests to fail; a caller that
// wants a handle it can keep reading from for the life of, say, a native
// audio player embed must pass OpenAudio a ctx that will outlive that use.
func (c *Client) OpenAudio(ctx context.Context, id string) (io.ReadSeekCloser, AudioInfo, error) {
	path := "/api/v1/transcriptions/" + url.PathEscape(id) + "/audio"

	resp, err := c.openRange(ctx, path, 0)
	if err != nil {
		return nil, AudioInfo{}, err
	}

	info := audioInfoFromHeaders(resp)

	switch resp.StatusCode {
	case http.StatusPartialContent:
		size := info.SizeBytes
		if size == 0 && resp.Header.Get("Content-Range") == "" {
			// Genuinely a zero-byte range response with no usable
			// Content-Range at all (should not happen against this
			// package's own server) -- treat as unknown rather than 0, so
			// the first Read does not spuriously see pos>=size.
			size = -1
		}
		rc := &audioReadSeekCloser{
			c:      c,
			ctx:    ctx,
			url:    path,
			size:   size,
			stream: resp.Body,
		}
		return rc, info, nil

	case http.StatusOK:
		// Server ignored our Range header: no Range support at all.
		data, rerr := io.ReadAll(resp.Body)
		resp.Body.Close()
		if rerr != nil {
			return nil, AudioInfo{}, classifyTransportErr(ctx, rerr)
		}
		info.SizeBytes = int64(len(data))
		rc := &audioReadSeekCloser{
			c:       c,
			ctx:     ctx,
			url:     path,
			size:    info.SizeBytes,
			noRange: true,
			buf:     bytes.NewReader(data),
		}
		return rc, info, nil

	case http.StatusRequestedRangeNotSatisfiable:
		// An empty file: "bytes=0-" is unsatisfiable against zero length.
		resp.Body.Close()
		rc := &audioReadSeekCloser{c: c, ctx: ctx, url: path, size: info.SizeBytes}
		return rc, info, nil

	default:
		body, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBodyBytes))
		resp.Body.Close()
		return nil, AudioInfo{}, classifyError(resp.StatusCode, body)
	}
}

// openRange issues GET path with "Range: bytes=pos-", bound to ctx and
// nothing else -- deliberately not defaultRequestTimeout: an audio file's
// transfer time is unrelated to how quickly the server responds, and
// bounding it the same way would either cut off a large legitimate
// transfer or force this client to pick an arbitrary "big enough" number
// that is wrong for someone. The caller (OpenAudio, or an
// audioReadSeekCloser reconnecting) owns the response and must close it.
func (c *Client) openRange(ctx context.Context, path string, pos int64) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return nil, domain.Wrap(domain.ErrCodeInternal, "build request for GET "+path, err)
	}
	req.Header.Set("Accept", "*/*")
	req.Header.Set("User-Agent", c.userAgent)
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-", pos))

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, classifyTransportErr(ctx, err)
	}
	return resp, nil
}

// audioInfoFromHeaders reconstructs an AudioInfo from a
// GET .../audio response's headers. See OpenAudio's doc for which fields
// are exact and which are best-effort.
func audioInfoFromHeaders(resp *http.Response) AudioInfo {
	info := AudioInfo{MediaType: resp.Header.Get("Content-Type")}

	if cd := resp.Header.Get("Content-Disposition"); cd != "" {
		if _, params, err := mime.ParseMediaType(cd); err == nil {
			info.Filename = params["filename"]
		}
	}

	if lm := resp.Header.Get("Last-Modified"); lm != "" {
		if t, err := http.ParseTime(lm); err == nil {
			info.ModTime = t
		}
	}

	if resp.StatusCode == http.StatusPartialContent {
		// Content-Length here is the range chunk's length, not the whole
		// file's -- only Content-Range's total means "file size" for a
		// 206. docs/openapi.yaml's own server (http.ServeContent) always
		// sends a parseable one; if some intermediary ever did not,
		// SizeBytes is left at its zero value rather than reporting the
		// wrong number.
		if total, ok := parseContentRangeTotal(resp.Header.Get("Content-Range")); ok {
			info.SizeBytes = total
		}
		return info
	}
	if cl := resp.Header.Get("Content-Length"); cl != "" {
		if n, err := strconv.ParseInt(cl, 10, 64); err == nil {
			info.SizeBytes = n
		}
	}
	return info
}

// parseContentRangeTotal extracts the total resource size from a
// Content-Range header of the form "bytes X-Y/Z" or "bytes */Z". It
// returns ok=false for "bytes */*" (server declined to state a size) or
// anything it cannot parse, rather than guessing.
func parseContentRangeTotal(v string) (int64, bool) {
	v = strings.TrimSpace(v)
	v = strings.TrimPrefix(v, "bytes ")
	_, total, found := strings.Cut(v, "/")
	if !found || total == "*" {
		return 0, false
	}
	n, err := strconv.ParseInt(total, 10, 64)
	if err != nil {
		return 0, false
	}
	return n, true
}

// audioReadSeekCloser is an io.ReadSeekCloser over one transcription's
// audio endpoint, implemented with Range requests issued on demand. See
// OpenAudio's doc for the overall strategy; this type is its state
// machine. It is a straight port of the sibling ingestion service's
// content.go rangeReadSeekCloser, trimmed to this service's single audio
// endpoint (no equivalent of that service's separate per-file content
// route).
type audioReadSeekCloser struct {
	c   *Client
	ctx context.Context
	url string

	mu sync.Mutex

	size int64 // -1 if still unknown; always known in practice after OpenAudio (see its doc)
	pos  int64 // logical position; the next byte a Read will return

	// stream is the currently open response body for a Range request, when
	// there is one. Its next byte corresponds to absolute offset streamPos.
	stream    io.ReadCloser
	streamPos int64

	// noRange and buf implement the "server ignored Range" fallback: once
	// true, every Read/Seek is served from buf (the whole file, fetched
	// once) rather than the network.
	noRange bool
	buf     *bytes.Reader

	closed bool
}

// Read implements io.Reader.
func (r *audioReadSeekCloser) Read(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.closed {
		return 0, fs.ErrClosed
	}

	if r.buf == nil {
		if r.size >= 0 && r.pos >= r.size {
			return 0, io.EOF
		}
		if r.stream == nil || r.streamPos != r.pos {
			if err := r.repositionLocked(); err != nil {
				if errors.Is(err, io.EOF) {
					return 0, io.EOF
				}
				return 0, err
			}
		}
	}

	if r.buf != nil {
		n, err := r.buf.Read(p)
		r.pos += int64(n)
		return n, err
	}

	n, err := r.stream.Read(p)
	r.pos += int64(n)
	r.streamPos += int64(n)
	if err != nil {
		r.stream.Close()
		r.stream = nil
		if errors.Is(err, io.EOF) {
			if r.size < 0 {
				r.size = r.pos
			}
			return n, io.EOF
		}
		return n, classifyTransportErr(r.ctx, err)
	}
	return n, nil
}

// repositionLocked makes r.stream (or r.buf) ready to serve a Read starting
// at r.pos, either by fast-forwarding the existing connection (small
// forward seeks, within maxDiscardSkip) or by opening a fresh Range
// request. Called with r.mu held.
func (r *audioReadSeekCloser) repositionLocked() error {
	if r.stream != nil {
		delta := r.pos - r.streamPos
		if delta >= 0 && delta <= maxDiscardSkip {
			if _, err := io.CopyN(io.Discard, r.stream, delta); err == nil {
				r.streamPos = r.pos
				return nil
			}
			// The connection died while fast-forwarding; fall through and
			// open a new one instead of returning that error, since a
			// fresh Range request may well still succeed.
		}
		r.stream.Close()
		r.stream = nil
	}

	resp, err := r.c.openRange(r.ctx, r.url, r.pos)
	if err != nil {
		return err
	}

	switch resp.StatusCode {
	case http.StatusPartialContent:
		if total, ok := parseContentRangeTotal(resp.Header.Get("Content-Range")); ok {
			r.size = total
		}
		r.stream = resp.Body
		r.streamPos = r.pos
		return nil

	case http.StatusRequestedRangeNotSatisfiable:
		if total, ok := parseContentRangeTotal(resp.Header.Get("Content-Range")); ok {
			r.size = total
		} else if r.size < 0 {
			r.size = r.pos
		}
		resp.Body.Close()
		return io.EOF

	case http.StatusOK:
		// The server has started (or resumed, mid-file) ignoring Range.
		// Buffer what it sent and splice it in at r.pos: what came back is
		// the WHOLE file from byte 0, per HTTP semantics for an ignored
		// Range, not just the tail from r.pos.
		data, rerr := io.ReadAll(resp.Body)
		resp.Body.Close()
		if rerr != nil {
			return classifyTransportErr(r.ctx, rerr)
		}
		r.noRange = true
		r.size = int64(len(data))
		r.buf = bytes.NewReader(data)
		if _, serr := r.buf.Seek(r.pos, io.SeekStart); serr != nil {
			return serr
		}
		return nil

	default:
		body, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBodyBytes))
		resp.Body.Close()
		return classifyError(resp.StatusCode, body)
	}
}

// Seek implements io.Seeker. It never itself makes a request: it only
// updates the logical position, deferring to the next Read (or, for
// io.SeekEnd against an unknown size, ensureSizeLocked) to talk to the
// network.
func (r *audioReadSeekCloser) Seek(offset int64, whence int) (int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.closed {
		return 0, fs.ErrClosed
	}

	var base int64
	switch whence {
	case io.SeekStart:
		base = 0
	case io.SeekCurrent:
		base = r.pos
	case io.SeekEnd:
		if err := r.ensureSizeLocked(); err != nil {
			return 0, err
		}
		base = r.size
	default:
		return 0, fmt.Errorf("httpclient: Seek: invalid whence %d", whence)
	}

	target := base + offset
	if target < 0 {
		return 0, fmt.Errorf("httpclient: Seek: negative position")
	}

	if r.buf != nil {
		pos, err := r.buf.Seek(target, io.SeekStart)
		if err == nil {
			r.pos = pos
		}
		return pos, err
	}

	r.pos = target
	return r.pos, nil
}

// ensureSizeLocked determines r.size when it is not already known. In
// practice OpenAudio always learns the size up front (see its doc), so
// this only matters defensively -- e.g. a server that sent a 206 with an
// unparsable Content-Range. It costs one extra request, at most once per
// audioReadSeekCloser. Called with r.mu held.
func (r *audioReadSeekCloser) ensureSizeLocked() error {
	if r.size >= 0 {
		return nil
	}

	resp, err := r.c.openRange(r.ctx, r.url, 0)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusPartialContent:
		if total, ok := parseContentRangeTotal(resp.Header.Get("Content-Range")); ok {
			r.size = total
			return nil
		}
	case http.StatusOK:
		if cl := resp.Header.Get("Content-Length"); cl != "" {
			if n, perr := strconv.ParseInt(cl, 10, 64); perr == nil {
				r.size = n
				return nil
			}
		}
	}
	return domain.New(domain.ErrCodeInternal, "could not determine audio content size")
}

// Close implements io.Closer. It is idempotent and safe to call after only
// a partial read.
func (r *audioReadSeekCloser) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.closed {
		return nil
	}
	r.closed = true
	if r.stream != nil {
		err := r.stream.Close()
		r.stream = nil
		return err
	}
	return nil
}
