package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/pablofornell/helios-audio-transcription-service/internal/app"
	"github.com/pablofornell/helios-audio-transcription-service/internal/domain"
	"github.com/pablofornell/helios-audio-transcription-service/internal/observability"
	"github.com/pablofornell/helios-audio-transcription-service/internal/transcript"
)

// handlers holds the dependencies every handler needs: the port they
// translate HTTP requests into calls on, the health registry /readyz
// aggregates, and the configured upload ceiling. No handler may hold
// anything else -- business logic lives in Service, not here.
type handlers struct {
	service        Service
	storage        interface{ FreeBytes() (uint64, error) }
	health         *observability.Registry
	maxUploadBytes int64
	// uploadWriteTimeout is how long submitTranscription extends the
	// response's write deadline to, from the moment it starts handling
	// the request -- see its own doc. It is set from RouterDeps.RouteTimeout
	// (sized off HTTP.ReadTimeout in wire.go, "long, uploads are large"),
	// the same bound chimw.Timeout already gives the handler to run in, so
	// the write deadline is never the thing that cuts an otherwise-allowed
	// upload short.
	//
	// getAudio, the mirror-image download route, does NOT reuse this value
	// for its OWN write deadline -- see deadlineResettingWriter's doc for
	// why it renews a stall deadline per chunk instead of extending a
	// single one sized off RouteTimeout. It does reuse RouteTimeout itself
	// for a different purpose: audioOpenTimeout, below.
	uploadWriteTimeout time.Duration
	// audioOpenTimeout bounds getAudio's Service.OpenSource call ONLY --
	// never the stream that follows it -- via a context.WithTimeout
	// derived from the request's own context, right before that one call.
	// See getAudio's own doc for why this exists (mounting the route
	// outside chimw.Timeout removed the bound chimw.Timeout used to put on
	// THIS phase, not just the streaming phase) and why canceling that
	// derived context the instant OpenSource returns cannot also cut the
	// stream short. Set from RouterDeps.RouteTimeout, the same value
	// chimw.Timeout would have bounded this call by had the route stayed
	// in its Group.
	audioOpenTimeout time.Duration
}

// maxOptionsPartBytes bounds how much of the multipart request's `options`
// part submitTranscription will read into memory. It is generous relative
// to any legitimate domain.Options JSON (domain.Options.Validate already
// caps InitialPrompt at 1024 bytes) and exists only so a caller cannot
// force an unbounded read before MaxBytesReader's much larger ceiling
// would ever trip.
const maxOptionsPartBytes = 1 << 20 // 1 MiB

// classifyBodyErr turns an *http.MaxBytesError -- raised when reading past
// RouterDeps.MaxUploadBytes anywhere along the request body, whether while
// decoding the `options` part or while Service.Submit streams the `file`
// part to storage -- into ErrCodePayloadTooLarge. MaxBytesReader operates
// below Service, which has no way to attach that classification itself
// (it only ever sees "the io.Reader it was given returned an error"), so
// this is the one place in the funnel that can. Anything else, including
// nil, passes through unchanged -- an err already classified deeper in the
// stack (e.g. a *domain.Error from Service.Submit) is left alone.
func classifyBodyErr(err error) error {
	if err == nil {
		return nil
	}
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		return domain.New(domain.ErrCodePayloadTooLarge, "upload exceeds the maximum allowed size")
	}
	return err
}

// submitTranscription implements POST /api/v1/transcriptions.
//
// The request is multipart/form-data with two parts: an optional
// `options` part (a JSON object matching domain.Options; absent or empty
// means defaults) and a required `file` part (the audio itself). The body
// is read via r.MultipartReader() rather than r.ParseMultipartForm, which
// would spool the entire upload to disk before this handler ever saw it --
// defeating the point of streaming a multi-gigabyte file straight into
// Service.Submit.
//
// Part order does not matter. When `options` arrives before `file` (as
// read here, in request order), it is parsed immediately, exactly as
// always. When `file` arrives first, it is wrapped in fileWithLateOptions
// (see its own doc) before being handed to Service.Submit as Body: the
// file's bytes still stream straight into the store the instant they
// arrive -- nothing is buffered -- but the moment that streaming hits the
// part's own EOF, the wrapper keeps walking mr for a trailing `options`
// part on Service.Submit's behalf, so a submission is never silently
// accepted with defaults just because the caller's client (curl's
// `-F file=@x -F options=...`, in particular -- see README) happened to
// send the parts in that order.
//
// The upload route's write deadline is extended right here, before
// anything else, for exactly the reason h.uploadWriteTimeout documents:
// the underlying *http.Server's WriteTimeout starts counting from the
// instant headers are read, which is long before a large, merely slow,
// upload has finished streaming.
func (h *handlers) submitTranscription(w http.ResponseWriter, r *http.Request) {
	if err := http.NewResponseController(w).SetWriteDeadline(time.Now().Add(h.uploadWriteTimeout)); err != nil {
		// Not every ResponseWriter supports a write deadline (notably
		// httptest.ResponseRecorder, used by this package's own unit
		// tests) -- best-effort only; the request still proceeds normally.
		observability.FromContext(r.Context()).Debug("submitTranscription: could not extend write deadline", "error", err)
	}

	r.Body = http.MaxBytesReader(w, r.Body, h.maxUploadBytes)

	mr, err := r.MultipartReader()
	if err != nil {
		writeError(w, r, domain.Wrap(domain.ErrCodeInvalidInput, "malformed multipart request", err))
		return
	}

	opts := domain.Options{}
	optionsSeen := false

	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			// Every part has been seen and none was named "file" -- the
			// loop below would otherwise never terminate any other way,
			// since the "file" case always returns.
			break
		}
		if err != nil {
			writeError(w, r, classifyBodyErr(domain.Wrap(domain.ErrCodeInvalidInput, "malformed multipart request", err)))
			return
		}

		switch part.FormName() {
		case "options":
			data, readErr := readOptionsPart(part)
			_ = part.Close()
			if readErr != nil {
				writeError(w, r, classifyBodyErr(readErr))
				return
			}
			if len(bytes.TrimSpace(data)) > 0 {
				if err := json.Unmarshal(data, &opts); err != nil {
					writeError(w, r, domain.Wrap(domain.ErrCodeInvalidInput, "malformed options JSON", err).WithDetails("field", "options"))
					return
				}
			}
			optionsSeen = true

		case "file":
			var body io.Reader = part
			if !optionsSeen {
				// `options` has not been seen yet -- it may simply be
				// absent, or it may be about to arrive right after this
				// part. Either way, opts here is only a placeholder
				// (domain.Options{}, always valid -- see
				// domain.Options.Validate's own doc); the wrapper is what
				// makes sure Service.Submit uses the real thing.
				body = &fileWithLateOptions{part: part, mr: mr}
			}

			t, err := h.service.Submit(r.Context(), app.SubmitRequest{
				Filename:  part.FileName(),
				MediaType: mediaTypeForPart(part),
				Options:   opts,
				Body:      body,
			})
			_ = part.Close()
			if err != nil {
				writeError(w, r, classifyBodyErr(err))
				return
			}

			w.Header().Set("Location", "/api/v1/transcriptions/"+t.ID)
			writeJSON(w, http.StatusAccepted, transcriptionFromDomain(t))
			return

		default:
			// An unrecognised part is drained and ignored rather than
			// rejected outright, so an extra field a future client adds
			// does not break every older handler build.
			_ = part.Close()
		}
	}

	// Reaching here means every part was consumed without ever finding
	// one named "file".
	writeError(w, r, domain.New(domain.ErrCodeInvalidInput, `multipart request is missing the required "file" part`).WithDetails("field", "file"))
}

// fileWithLateOptions wraps the `file` multipart part when it is
// encountered before any `options` part has been seen. Its Read delegates
// straight to the underlying part -- the upload still streams into the
// store one pass, nothing buffered -- but the instant the part reports
// io.EOF (every one of the file's bytes has already been handed to the
// caller, i.e. already written to the store by the time Service.Submit
// gets to look), it keeps walking mr for whatever part(s) follow, exactly
// as submitTranscription's own top-level loop would have done had it not
// already committed to `file`: an `options` part found there is parsed and
// stashed for Service.Submit to pick up via the lateOptionsProvider
// interface it implements below; anything else is drained and ignored,
// matching the loop's own "default" case. This only ever runs after the
// file's own bytes are fully drained -- never before, never concurrently
// -- so it never competes with anything else for mr.
type fileWithLateOptions struct {
	part *multipart.Part
	mr   *multipart.Reader

	scanned   bool
	late      domain.Options
	lateFound bool
	lateErr   error
}

func (f *fileWithLateOptions) Read(p []byte) (int, error) {
	n, err := f.part.Read(p)
	if err == io.EOF && !f.scanned {
		f.scanned = true
		f.scanForLateOptions()
		// The scan never changes whether the file's own bytes are
		// exhausted -- err stays io.EOF regardless of what, if anything,
		// followed them.
	}
	return n, err
}

// scanForLateOptions walks whatever parts remain after `file`, looking
// for exactly one named `options`. It is called at most once, from Read,
// the instant the file part itself is exhausted.
func (f *fileWithLateOptions) scanForLateOptions() {
	for {
		part, err := f.mr.NextPart()
		if err == io.EOF {
			return
		}
		if err != nil {
			f.lateErr = classifyBodyErr(domain.Wrap(domain.ErrCodeInvalidInput, "malformed multipart request", err))
			return
		}
		if part.FormName() != "options" {
			_ = part.Close()
			continue
		}

		data, readErr := readOptionsPart(part)
		_ = part.Close()
		if readErr != nil {
			f.lateErr = classifyBodyErr(readErr)
			return
		}
		if len(bytes.TrimSpace(data)) > 0 {
			if jerr := json.Unmarshal(data, &f.late); jerr != nil {
				f.lateErr = domain.Wrap(domain.ErrCodeInvalidInput, "malformed options JSON", jerr).WithDetails("field", "options")
				return
			}
		}
		f.lateFound = true
		return
	}
}

// LateOptions implements app.Service.Submit's unexported lateOptionsProvider
// interface -- see its own doc there for why this exists.
func (f *fileWithLateOptions) LateOptions() (domain.Options, bool, error) {
	return f.late, f.lateFound, f.lateErr
}

// readOptionsPart reads part's whole content, bounded by
// maxOptionsPartBytes so a caller cannot force an unbounded in-memory read
// via the `options` field.
func readOptionsPart(part *multipart.Part) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(part, maxOptionsPartBytes))
	if err != nil {
		return nil, domain.Wrap(domain.ErrCodeInvalidInput, "failed to read options part", err)
	}
	return data, nil
}

// mediaTypeForPart resolves a multipart file part's media type: the
// part's own Content-Type header when present, falling back to sniffing
// by the uploaded filename's extension, and finally to a generic binary
// type when neither is available.
func mediaTypeForPart(part *multipart.Part) string {
	if ct := part.Header.Get("Content-Type"); ct != "" {
		return ct
	}
	ext := strings.ToLower(filepath.Ext(part.FileName()))
	if mt := mime.TypeByExtension(ext); mt != "" {
		return mt
	}
	return "application/octet-stream"
}

// listTranscriptions implements GET /api/v1/transcriptions. A non-numeric
// `limit` is rejected as ErrCodeInvalidInput; a well-formed but
// out-of-range one is passed through, since Service.List itself clamps
// the value into [1,100] -- see app.ListFilter's doc.
func (h *handlers) listTranscriptions(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()

	filter := app.ListFilter{
		Cursor: q.Get("cursor"),
	}

	if values, present := q["library"]; present {
		if len(values) != 1 || (values[0] != "true" && values[0] != "false") {
			writeError(w, r, domain.New(domain.ErrCodeInvalidInput, "library must be true or false").WithDetails("field", "library"))
			return
		}
		filter.Library = values[0] == "true"
	}

	if raw := q.Get("status"); raw != "" {
		status := domain.Status(raw)
		if !status.Valid() {
			writeError(w, r, domain.New(domain.ErrCodeInvalidInput, fmt.Sprintf("invalid status %q", raw)).WithDetails("field", "status"))
			return
		}
		filter.Status = status
	}

	if raw := q.Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil {
			writeError(w, r, domain.Wrap(domain.ErrCodeInvalidInput, fmt.Sprintf("limit %q is not a valid integer", raw), err).WithDetails("field", "limit"))
			return
		}
		filter.Limit = n
	}

	page, err := h.service.List(r.Context(), filter)
	if err != nil {
		writeError(w, r, err)
		return
	}

	writeJSON(w, http.StatusOK, listTranscriptionsResponseFromItems(page.Items, page.NextCursor))
}

// getTranscription implements GET /api/v1/transcriptions/{id}.
func (h *handlers) getTranscription(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	t, err := h.service.Get(r.Context(), id)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, transcriptionFromDomain(t))
}

// deleteTranscription implements DELETE /api/v1/transcriptions/{id}.
func (h *handlers) deleteTranscription(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	if err := h.service.Delete(r.Context(), id); err != nil {
		writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// cancelTranscription implements POST /api/v1/transcriptions/{id}/cancel.
func (h *handlers) cancelTranscription(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	t, err := h.service.Cancel(r.Context(), id)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, transcriptionFromDomain(t))
}

// retryTranscription implements POST /api/v1/transcriptions/{id}/retry.
func (h *handlers) retryTranscription(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	t, err := h.service.Retry(r.Context(), id)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, transcriptionFromDomain(t))
}

// getTranscript implements GET /api/v1/transcriptions/{id}/transcript.
// The requested format is validated before the transcription is even
// looked up, so an unknown `format` is rejected the same way regardless of
// whether id exists.
//
// txt/srt/vtt always carry Content-Disposition: attachment, since a
// browser would otherwise try to render a subtitle file inline. json does
// not, by default, since it is the format most callers consume
// programmatically rather than download -- but a caller that does want to
// save it can opt in with `?download=1` (or `download=true`), which forces
// the same attachment header json would otherwise never get. The param has
// no effect on txt/srt/vtt, which are already attachments regardless.
func (h *handlers) getTranscript(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	q := r.URL.Query()

	format, err := transcript.ParseFormat(q.Get("format"))
	if err != nil {
		writeError(w, r, err)
		return
	}
	download := parseDownloadQuery(q.Get("download"))

	t, err := h.service.Get(r.Context(), id)
	if err != nil {
		writeError(w, r, err)
		return
	}
	if t.Status != domain.StatusSucceeded {
		writeError(w, r, domain.New(domain.ErrCodeConflict, fmt.Sprintf("transcription %q has not succeeded", id)))
		return
	}

	body, err := transcript.Render(t.Result, format)
	if err != nil {
		writeError(w, r, err)
		return
	}

	w.Header().Set("Content-Type", format.ContentType())
	if format != transcript.FormatJSON || download {
		w.Header().Set("Content-Disposition", contentDispositionAttachment(transcriptDownloadName(t.Audio.Filename, format)))
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

// transcriptDownloadName keeps the uploaded audio's base name while replacing
// its extension with the requested transcript format. Leave room for the
// suffix under common 255-byte filesystem component limits and avoid a
// leading dot, which would make the download hidden on POSIX systems.
func transcriptDownloadName(filename string, format transcript.Format) string {
	base := path.Base(strings.ReplaceAll(filename, `\`, "/"))
	if ext := filepath.Ext(base); ext != base {
		base = strings.TrimSuffix(base, ext)
	}
	base = strings.Trim(base, ". ")
	if len(base) > 200 {
		cut := 0
		for i := range base {
			if i > 200 {
				break
			}
			cut = i
		}
		base = strings.TrimRight(base[:cut], ". ")
	}
	if base == "" {
		base = "transcription"
	}
	return base + "-transcription." + string(format)
}

// parseDownloadQuery interprets the `download` query parameter's raw
// string value: "1" or "true" opt in, anything else -- including an empty
// string (absent) and any value this does not recognise -- is treated as
// not set. An unrecognised value is deliberately ignored rather than
// rejected as ErrCodeInvalidInput, unlike `format` or `status` elsewhere in
// this package: it is a pure opt-in nicety with an unambiguous default
// (unset), so there is nothing a caller could get "wrong" that is worth a
// 400 for.
func parseDownloadQuery(raw string) bool {
	return raw == "1" || raw == "true"
}

// getAudio implements GET /api/v1/transcriptions/{id}/audio: it streams
// the original uploaded source via http.ServeContent, so Range requests
// (seeking within a large audio file) work for free, and always closes
// the opened reader once ServeContent returns.
//
// The stored MediaType is whatever Content-Type the uploading caller's
// multipart part happened to carry -- untrusted, and never checked against
// the bytes actually uploaded (ffprobe does that, later, in the worker,
// and only for the codec, not for anything script-shaped). Echoing it
// verbatim with no further defense would let an uploaded HTML/SVG payload
// execute as script on this service's own origin (which also serves
// /metrics). Three defenses apply unconditionally, regardless of what
// MediaType claims to be: X-Content-Type-Options: nosniff (stop a browser
// from re-sniffing past a deliberately-wrong declared type),
// Content-Disposition: attachment (never rendered inline, whatever the
// Content-Type), and downgrading anything outside audio/* or video/* to
// application/octet-stream -- see safeAudioContentType.
//
// This handler is the mirror image of submitTranscription's own upload
// problem: a large file, streamed to a merely slow (not stuck) client over
// a slow link, must not be cut off by any of the three things that would
// otherwise bound it.
//
// First, the streaming phase's total duration: mountAPI does not wrap this
// route in chimw.Timeout at all, so the request context http.ServeContent
// runs under is never given a fixed-duration deadline by chi's own
// middleware stack, however small RouteTimeout is configured. (net/http's
// own request context is a separate matter: it is still tied to the
// underlying connection and is canceled when the client drops it.
// HTTP_READ_TIMEOUT does not cancel it mid-download: for a body-less GET,
// net/http clears the read deadline before the handler starts. localfs's
// OpenSource returns a plain *os.File, whose Read and Seek never consult
// ctx at all -- a future storage backend whose reader DOES check context
// per-Read would need this route's ctx handling looked at again.)
//
// Second, the streaming phase's staleness: the underlying *http.Server's
// WriteTimeout, which starts counting from the instant headers are read
// and would otherwise cut the transfer off partway through, is answered by
// wrapping w in deadlineResettingWriter below: unlike submitTranscription,
// which extends its write deadline once to a bound sized off RouteTimeout
// (a value this package already has in hand, chosen for the upload's total
// size), getAudio has no equivalent size hint to bound a download by -- the
// original file can be arbitrarily large, and a single fixed deadline
// picked without one is just as likely to be too short for some download
// as it is to leave a genuinely stalled connection (and the goroutine,
// file handle and Service.OpenSource reader behind it) open indefinitely,
// which a first version of this fix did and a reviewer caught: a client
// that simply never reads its response body kept this handler blocked well
// past any sane bound. deadlineResettingWriter renews the deadline on
// every chunk actually written instead, so only a stall -- no forward
// progress at all for AudioWriteIdleTimeout -- ends the connection, never
// a merely slow-but-moving transfer.
//
// Third, the phase BEFORE any of the above even starts: mounting this
// route outside chimw.Timeout removed the bound chimw.Timeout used to put
// on Service.OpenSource itself, not just on the streaming that follows
// it -- a reviewer caught this too, pointing out that OpenSource reaches
// the database (to look id up) before it ever reaches local disk, and a
// hung database call would now hold this handler's goroutine (and
// whatever connection-pool slot it is waiting on) open forever, same as
// before this fix but now with nothing bounding it at all. h.audioOpenTimeout
// (sized off the same RouteTimeout chimw.Timeout would have used) answers
// that with a context.WithTimeout scoped to the OpenSource call alone,
// below -- cancel runs the instant OpenSource returns, success or not,
// which is safe precisely because it runs BEFORE http.ServeContent ever
// starts reading from rc: canceling a context already returned from does
// nothing to a plain io.ReadSeekCloser like localfs's *os.File, whose Read
// and Seek take no context at all, so the stream itself, and everything
// after this point in the handler, is untouched by openCtx's fate.
func (h *handlers) getAudio(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	openCtx, cancel := context.WithTimeout(r.Context(), h.audioOpenTimeout)
	rc, t, err := h.service.OpenSource(openCtx, id)
	cancel()
	if err != nil {
		// A bare context.DeadlineExceeded from openCtx's own timeout (as
		// opposed to one domain.Wrap already turned into a *domain.Error,
		// e.g. by internal/postgres's own query-cancellation handling) is
		// classified by domain.CodeOf itself -- see its own doc -- into
		// ErrCodeTimeout, the same code a hung handler still inside
		// chimw.Timeout's Group would have produced, so writeError maps
		// this the same way it would have mapped that: 504 Gateway Timeout,
		// matching chimw.Timeout's own StatusGatewayTimeout response.
		writeError(w, r, err)
		return
	}
	defer rc.Close()

	name := t.Audio.Filename
	if name == "" {
		name = id
	}

	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Type", safeAudioContentType(t.Audio.MediaType))
	w.Header().Set("Content-Disposition", contentDispositionAttachment(name))

	// Content-Type is already set above, so http.ServeContent -- which
	// only sniffs/sets it when the header is still empty -- never
	// overrides safeAudioContentType's classification with a sniff of its
	// own.
	//
	// dw is constructed here, after OpenSource has already returned,
	// rather than at the top of the handler: OpenSource opens a local
	// file (see localfs), never a caller-controlled duration, so it is
	// left to finish inside whatever write deadline the connection
	// already inherited from before this handler ran, exactly as an
	// ordinary, non-streaming handler would -- only the transfer
	// http.ServeContent is about to drive needs AudioWriteIdleTimeout's
	// renewed-per-chunk deadline instead of a single fixed one.
	dw := newDeadlineResettingWriter(w)
	http.ServeContent(dw, r, name, t.CreatedAt, rc)
}

// AudioWriteIdleTimeout bounds how long getAudio's connection may go
// without a successful write to the client -- how STALE it may get, not
// how long the download as a whole may take (mountAPI already leaves the
// download's total duration unbounded by chimw.Timeout -- see that func's
// own doc). A truly large file over a genuinely slow (but still moving)
// connection can take far longer than ten minutes to finish; ten minutes
// with NO forward progress at all, by contrast, means the peer has stopped
// reading (the kernel's send buffer is full and staying full) or the
// connection is simply gone, and holding onto the opened source file and
// this goroutine any longer than that buys nothing -- this is exactly the
// failure a reviewer of an earlier version of this fix (which cleared the
// write deadline outright) reproduced: a client that opens the request and
// never reads its body kept the handler, and everything it holds open,
// blocked indefinitely.
//
// A package-level var rather than an unexported constant purely so this
// package's own tests (httpapi_test, an external package) can shrink it
// to something a test can wait out in milliseconds; production callers
// have no reason to ever touch it.
var AudioWriteIdleTimeout = 10 * time.Minute

// deadlineResettingWriter wraps an http.ResponseWriter so every successful
// Write renews the underlying connection's write deadline by
// AudioWriteIdleTimeout, via http.NewResponseController -- see that var's
// own doc for the reasoning. http.ServeContent's own copy loop already
// drives Write repeatedly (a chunk at a time for a plain io.ReadSeeker
// source, since neither side here implements the WriterTo/ReaderFrom fast
// path that would skip Write's call chain), so this renews on very nearly
// every chunk reaching the wire without getAudio needing a copy loop of
// its own.
//
// http.NewResponseController walks an Unwrap() http.ResponseWriter chain
// to find the real, connection-backed ResponseWriter underneath whatever
// middleware already wrapped w before getAudio ever saw it
// (chimw.NewWrapResponseWriter in newRequestLogger, otelhttp's own
// wrapper) -- both support Unwrap, so the controller built at construction
// time already reaches the same deadline the transport itself would
// otherwise silently enforce. deadlineResettingWriter implements Unwrap
// itself too, in turn, so anything that wraps IT further (nothing in this
// package does today) can still see through to the real writer rather than
// dead-ending here.
//
// SetWriteDeadline failing -- http.ErrNotSupported under
// httptest.ResponseRecorder, used by this package's own non-streaming
// tests, or any other error -- is deliberately swallowed: a ResponseWriter
// with no underlying connection deadline to renew has nothing for this
// wrapper to protect against either, and there is no reasonable response
// to an inability to *shorten* a timeout that was never going to fire.
type deadlineResettingWriter struct {
	http.ResponseWriter
	controller *http.ResponseController
}

// newDeadlineResettingWriter wraps w and renews the write deadline once
// immediately, before the caller writes anything, so the deadline inherited
// from before ServeContent started (see getAudio's own doc on why that
// deadline is left as-is up to this point) is replaced by
// AudioWriteIdleTimeout before the first byte of the body is due.
func newDeadlineResettingWriter(w http.ResponseWriter) *deadlineResettingWriter {
	d := &deadlineResettingWriter{ResponseWriter: w, controller: http.NewResponseController(w)}
	d.renewWriteDeadline()
	return d
}

func (d *deadlineResettingWriter) renewWriteDeadline() {
	_ = d.controller.SetWriteDeadline(time.Now().Add(AudioWriteIdleTimeout))
}

func (d *deadlineResettingWriter) Write(p []byte) (int, error) {
	d.renewWriteDeadline()
	return d.ResponseWriter.Write(p)
}

// Unwrap satisfies the http.ResponseController convention -- see
// deadlineResettingWriter's own doc.
func (d *deadlineResettingWriter) Unwrap() http.ResponseWriter { return d.ResponseWriter }

// safeAudioContentType returns mediaType unchanged when its base type
// (ignoring parameters like charset) is audio/* or video/*, and
// "application/octet-stream" otherwise -- including when mediaType is
// empty or fails to parse as a media type at all. Only a stored type this
// service already expects a client to play is ever passed through as-is;
// anything else (most importantly text/html, image/svg+xml and the like)
// is downgraded so no browser ever has a reason to render it. See getAudio's
// own doc.
func safeAudioContentType(mediaType string) string {
	if mediaType == "" {
		return "application/octet-stream"
	}
	base, _, err := mime.ParseMediaType(mediaType)
	if err != nil {
		return "application/octet-stream"
	}
	if strings.HasPrefix(base, "audio/") || strings.HasPrefix(base, "video/") {
		return mediaType
	}
	return "application/octet-stream"
}

// contentDispositionAttachment returns a Content-Disposition header value
// forcing name to download as an attachment, quoted per RFC 6266's
// quoted-string syntax so a name containing '"' or '\' cannot break out of
// the filename parameter and forge extra ones. Control characters --
// already stripped from a freshly-uploaded name by Service.sanitizeFilename,
// but not something this handler should trust blindly for an older row --
// are dropped outright rather than escaped, since none has any legitimate
// reason to appear in a filename.
func contentDispositionAttachment(name string) string {
	// Two parameters, deliberately. HTTP header values are ISO-8859-1, so a
	// non-ASCII rune written straight into filename= is read back byte by
	// byte as Latin-1: "grabación.ogg" reaches the user as
	// "grabaciÃ³n.ogg". RFC 6266 answers this with filename*, an RFC 5987
	// UTF-8 percent-encoding, which every current browser prefers when both
	// are present. The plain filename= stays as the ASCII-only fallback for
	// anything that does not understand filename*, so a Spanish or Japanese
	// upload downloads under a sane name in either case.
	var ascii strings.Builder
	needsExtended := false
	for _, r := range name {
		if r < 0x20 || r == 0x7f {
			continue
		}
		if r > 0x7e {
			// Outside ASCII: represent it in the fallback with '_' and let
			// filename* carry the real characters.
			needsExtended = true
			ascii.WriteByte('_')
			continue
		}
		if r == '"' || r == '\\' {
			ascii.WriteByte('\\')
		}
		ascii.WriteRune(r)
	}

	fallback := ascii.String()
	if strings.Trim(fallback, "_") == "" {
		// A name that is entirely non-ASCII would collapse to "____".
		// Give the fallback something a human can recognise instead.
		fallback = "download"
	}

	v := `attachment; filename="` + fallback + `"`
	if needsExtended {
		v += `; filename*=UTF-8''` + rfc5987Escape(name)
	}
	return v
}

// rfc5987Escape percent-encodes name per RFC 5987's ext-value: attr-char
// (ALPHA / DIGIT / "!#$&+-.^_`|~") survives, everything else -- including
// the space, the quote and every byte of a multi-byte rune -- is escaped.
// net/url's escapers are all wrong here: QueryEscape turns a space into
// "+", and PathEscape leaves several characters attr-char does not allow.
func rfc5987Escape(name string) string {
	const attrExtra = "!#$&+-.^_`|~"
	var b strings.Builder
	for i := 0; i < len(name); i++ {
		c := name[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9',
			strings.IndexByte(attrExtra, c) >= 0:
			b.WriteByte(c)
		default:
			b.WriteString(fmt.Sprintf("%%%02X", c))
		}
	}
	return b.String()
}
