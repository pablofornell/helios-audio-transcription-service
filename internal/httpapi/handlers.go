package httpapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
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
	health         *observability.Registry
	maxUploadBytes int64
	// uploadWriteTimeout is how long submitTranscription extends the
	// response's write deadline to, from the moment it starts handling
	// the request -- see its own doc. It is set from RouterDeps.RouteTimeout
	// (sized off HTTP.ReadTimeout in wire.go, "long, uploads are large"),
	// the same bound chimw.Timeout already gives the handler to run in, so
	// the write deadline is never the thing that cuts an otherwise-allowed
	// upload short.
	uploadWriteTimeout time.Duration
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
func (h *handlers) getTranscript(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	format, err := transcript.ParseFormat(r.URL.Query().Get("format"))
	if err != nil {
		writeError(w, r, err)
		return
	}

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
	if format != transcript.FormatJSON {
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s.%s"`, id, format))
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
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
func (h *handlers) getAudio(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	rc, t, err := h.service.OpenSource(r.Context(), id)
	if err != nil {
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
	http.ServeContent(w, r, name, t.CreatedAt, rc)
}

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
