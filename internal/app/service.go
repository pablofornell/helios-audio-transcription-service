package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/pablofornell/helios-audio-transcription-service/internal/domain"
	"github.com/pablofornell/helios-audio-transcription-service/internal/observability"
	"github.com/pablofornell/helios-audio-transcription-service/internal/storage/localfs"
)

// defaultListLimit and maxListLimit are List's paging policy. They live
// here, in app.Service, rather than in Repository: Repository.List's
// contract only promises "a page of transcriptions matching f" and leaves
// what counts as a reasonable page size to its caller, so there is exactly
// one place -- this one -- that decides what "reasonable" means.
// Repository implementations trust Limit as already sane.
const (
	defaultListLimit = 20
	maxListLimit     = 100
)

// submitCleanupTimeout bounds the best-effort store cleanup Submit performs
// when it must abandon an upload after already writing some bytes into the
// store. It deliberately does not reuse Submit's own ctx: that ctx may be
// exactly what got canceled (a client that hung up mid-upload), and a
// canceled context must never be the reason a half-written upload survives
// on disk with no database row to account for it.
const submitCleanupTimeout = 10 * time.Second

// maxSanitizedFilenameBytes caps the sanitized name stored on
// domain.Audio.Filename, mirroring localfs's own per-segment bound (see
// internal/storage/localfs's maxComponentBytes): the filename is never
// itself a path segment here (the stored file is always
// "source<ext>"), but an unbounded caller-supplied string is still a
// resource this service has no reason to carry around forever.
const maxSanitizedFilenameBytes = 255

// defaultUploadExt is used when the sanitized filename carries no usable
// extension, so the stored source file always has a name whisper.cpp's
// surrounding tooling (and a human operator) can look at.
const defaultUploadExt = ".bin"

// Deps bundles every dependency app.Service needs. Clock and Logger default
// to the production SystemClock and slog.Default() when left as the zero
// value (nil), so a caller only needs to supply a fake for whatever a given
// test actually exercises.
type Deps struct {
	// Repo persists Transcription rows and drives the caller-facing half of
	// their lifecycle (cancel, retry, delete). See Repository.
	Repo Repository
	// Queue is the worker-facing half of the same rows. Service itself
	// never calls it -- Cancel and Retry go through Repository, since
	// those are caller-initiated state changes, not lease-guarded worker
	// ones -- but it is threaded through Deps so callers wiring up
	// Service (see cmd/helios-transcription-api/wire) have a single place
	// to assemble every port a Postgres-backed deployment needs, rather
	// than constructing the same Repository/Queue pair twice.
	Queue Queue
	// Store reads and writes uploaded audio and derived artifacts. See
	// Store.
	Store Store
	// Clock abstracts the current time. Defaults to SystemClock.
	Clock Clock
	// IDs mints transcription IDs, injected for the same reason as Clock.
	IDs IDGenerator
	// Logger receives diagnostic logging (e.g. a failed best-effort
	// cleanup). Defaults to slog.Default().
	Logger *slog.Logger
	// Metrics records submission and completion counters. A nil Metrics
	// is a wiring bug in production, but Service does not defend against
	// it: every method that touches it is reached only after a
	// successful mutation, so a nil pointer panic there is loud and
	// immediate rather than a silently dropped metric.
	Metrics *observability.Metrics
	// DefaultModel is the whisper model name substituted for a
	// caller-supplied empty Options.Model, after validation -- see
	// Submit and domain.Options.Validate's own doc for why Validate
	// itself must not see or reject this substitution.
	DefaultModel string
	// AllowedModels restricts Options.Model to a fixed list, passed
	// straight through to domain.Options.Validate. Empty means "skip the
	// check", matching Validate's own contract.
	AllowedModels []string
	// MaxAttempts is the ceiling assigned to every newly submitted
	// Transcription (domain.Transcription.MaxAttempts). It mirrors
	// config.WorkerConfig.MaxAttempts; Service never reads config
	// directly, and does not otherwise consult it.
	MaxAttempts int
	// MaxUploadBytes is the largest upload body Submit accepts. A body
	// exceeding it fails with ErrCodePayloadTooLarge.
	MaxUploadBytes int64
	// MinFreeBytes is the minimum free space Store must report for Submit
	// to proceed at all -- see Submit's own doc for why this check runs
	// before anything is written.
	MinFreeBytes uint64
}

// Service is the single implementation of the business logic every other
// package (internal/httpapi, internal/jobs, internal/retention) drives
// through the ports declared in ports.go. Every returned error is a
// *domain.Error, classified at the point of detection, so callers never
// need to guess what a bare error means.
type Service struct {
	repo   Repository
	queue  Queue
	store  Store
	clock  Clock
	ids    IDGenerator
	logger *slog.Logger

	metrics *observability.Metrics

	defaultModel   string
	allowedModels  []string
	maxAttempts    int
	maxUploadBytes int64
	minFreeBytes   uint64
}

// NewService constructs a Service from deps, applying production defaults
// for Clock and Logger when left unset.
func NewService(deps Deps) *Service {
	clock := deps.Clock
	if clock == nil {
		clock = SystemClock{}
	}
	logger := deps.Logger
	if logger == nil {
		logger = slog.Default()
	}

	return &Service{
		repo:   deps.Repo,
		queue:  deps.Queue,
		store:  deps.Store,
		clock:  clock,
		ids:    deps.IDs,
		logger: logger,

		metrics: deps.Metrics,

		defaultModel:   deps.DefaultModel,
		allowedModels:  deps.AllowedModels,
		maxAttempts:    deps.MaxAttempts,
		maxUploadBytes: deps.MaxUploadBytes,
		minFreeBytes:   deps.MinFreeBytes,
	}
}

// SubmitRequest is what a caller hands Submit: the raw, untrusted upload
// metadata and body.
type SubmitRequest struct {
	// Filename is the caller-supplied original filename. Untrusted --
	// Submit sanitizes it into domain.Audio.Filename before it is stored
	// or persisted anywhere. See sanitizeFilename.
	Filename string
	// MediaType is the caller-supplied content type (e.g. from a
	// multipart part's Content-Type header). Stored as-is; it is a label,
	// not something Submit validates against the bytes actually
	// uploaded -- ffprobe does that, later, in the worker.
	MediaType string
	Options   domain.Options
	// Body is the upload's bytes. Submit reads at most MaxUploadBytes+1
	// of it -- see Submit's own doc.
	Body io.Reader
}

// lateOptionsProvider is an optional capability req.Body may implement
// when the caller cannot know the final domain.Options until after Body
// has been fully read. internal/httpapi needs this for exactly one
// reason: a multipart request's `options` part can legally arrive AFTER
// its `file` part, and by the time that is discovered the file's bytes
// must already have been streamed straight into the store (re-ordering
// them would mean buffering a potentially multi-gigabyte upload, or
// silently ignoring the options -- both unacceptable, see
// internal/httpapi's submitTranscription doc). A req.Body that does not
// implement it -- the common case, and every other caller of Submit -- is
// unaffected: Submit never even performs the type assertion until after
// Body's bytes are already safely on disk.
type lateOptionsProvider interface {
	// LateOptions returns options discovered strictly after Body reported
	// io.EOF. found is false when nothing overrides req.Options (Body's
	// caller-supplied options, still the value opts started from, were
	// already final).
	LateOptions() (domain.Options, bool, error)
}

// Submit validates req, allocates a new transcription and streams req.Body
// into the store as its source file, in this exact order:
//
//  1. Normalize then Validate req.Options against the configured allowed
//     models -- an empty Options.Model always passes Validate (see its own
//     doc) and is resolved to Deps.DefaultModel only afterwards, since
//     Validate has no business rejecting a substitution it cannot see.
//  2. Check Store.FreeBytes against Deps.MinFreeBytes, before writing
//     anything -- refusing an upload up front is cheap; discovering
//     ENOSPC mid-write after minting an ID and opening a file is not.
//  3. Mint an ID, sanitize req.Filename and derive its extension.
//  4. Stream req.Body into transcriptions/<id>/source<ext>, bounded by an
//     io.LimitReader capped at MaxUploadBytes+1 so an oversized upload is
//     caught after reading one byte past the limit rather than after
//     buffering the whole (potentially unbounded) body.
//  5. If req.Body implements lateOptionsProvider, ask it for options
//     discovered after its own EOF and, when it found any, re-run step 1
//     against them -- see lateOptionsProvider's own doc for why this
//     exists. A validation failure discovered only here is reported and
//     cleaned up exactly like one caught in step 1; the only difference is
//     that, in this path, it is caught after the bytes were already
//     written, which is the unavoidable cost of not requiring `file` to be
//     any particular part of a multipart request.
//  6. Insert the row as StatusPending and record
//     Metrics.TranscriptionSubmitted.
//
// Any failure from step 3 onward -- a write failure, an oversized body,
// options rejected at step 5, or Repo.Create itself failing -- removes the
// whole transcriptions/<id> directory before returning, using a context
// independent of ctx (see submitCleanupTimeout), so a half-written upload
// with no database row never lingers as a disk leak.
func (s *Service) Submit(ctx context.Context, req SubmitRequest) (t *domain.Transcription, err error) {
	opts := req.Options.Normalize()
	if verr := opts.Validate(s.allowedModels); verr != nil {
		return nil, verr
	}
	if opts.Model == "" {
		opts.Model = s.defaultModel
	}

	free, ferr := s.store.FreeBytes()
	if ferr != nil {
		return nil, domain.Wrap(domain.ErrCodeInternal, "submit: check free storage space", ferr)
	}
	if free < s.minFreeBytes {
		return nil, domain.New(domain.ErrCodeDiskFull, "insufficient free storage space for this upload").
			WithDetails("freeBytes", free).
			WithDetails("minFreeBytes", s.minFreeBytes)
	}

	id := s.ids.NewID()
	dir := transcriptionDir(id)
	defer func() {
		if err != nil {
			s.cleanupUpload(id, dir, err)
		}
	}()

	filename, ext := sanitizeFilename(req.Filename)
	sourcePath := path.Join(dir, "source"+ext)

	limited := io.LimitReader(req.Body, s.maxUploadBytes+1)
	n, perr := s.store.Put(ctx, sourcePath, limited)
	if perr != nil {
		err = s.classifyStoreErr(perr, "submit: write upload")
		return nil, err
	}
	if n > s.maxUploadBytes {
		err = domain.New(domain.ErrCodePayloadTooLarge, "upload exceeds the maximum allowed size").
			WithDetails("maxUploadBytes", s.maxUploadBytes)
		return nil, err
	}

	if lp, ok := req.Body.(lateOptionsProvider); ok {
		late, found, lerr := lp.LateOptions()
		if lerr != nil {
			err = lerr
			return nil, err
		}
		if found {
			resolved := late.Normalize()
			if verr := resolved.Validate(s.allowedModels); verr != nil {
				err = verr
				return nil, err
			}
			if resolved.Model == "" {
				resolved.Model = s.defaultModel
			}
			opts = resolved
		}
	}

	now := s.clock.Now()
	row := &domain.Transcription{
		ID:      id,
		Status:  domain.StatusPending,
		Options: opts,
		Audio: domain.Audio{
			Filename:  filename,
			MediaType: req.MediaType,
			SizeBytes: n,
		},
		SourcePath:  sourcePath,
		MaxAttempts: s.maxAttempts,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if cerr := s.repo.Create(ctx, row); cerr != nil {
		s.logger.ErrorContext(ctx, "submit: create row failed",
			"transcription_id", id, "error", cerr)
		err = cerr
		return nil, err
	}

	s.metrics.TranscriptionSubmitted()
	return row, nil
}

// cleanupUpload best-effort removes dir (transcriptions/<id>) after Submit
// has to abandon an upload partway through. See Submit's own doc for why it
// runs on its own timeout-bound context rather than Submit's ctx. A
// cleanup failure is logged, not returned: the caller already has the real
// error (cause) to report, and a leaked directory here is an operator's
// problem, not the API caller's.
func (s *Service) cleanupUpload(id, dir string, cause error) {
	ctx, cancel := context.WithTimeout(context.Background(), submitCleanupTimeout)
	defer cancel()
	if err := s.store.RemoveDir(ctx, dir); err != nil {
		s.logger.Error("submit: failed to clean up upload directory after aborted submission",
			"transcription_id", id, "cause", cause, "error", err)
	}
}

// Get returns the transcription with the given id.
func (s *Service) Get(ctx context.Context, id string) (*domain.Transcription, error) {
	if err := validateID(id); err != nil {
		return nil, err
	}
	return s.repo.Get(ctx, id)
}

// List returns one page of transcriptions matching f, applying List's
// paging policy (see defaultListLimit/maxListLimit) before delegating to
// Repository.
func (s *Service) List(ctx context.Context, f ListFilter) (Page, error) {
	switch {
	case f.Limit <= 0:
		f.Limit = defaultListLimit
	case f.Limit > maxListLimit:
		f.Limit = maxListLimit
	}
	return s.repo.List(ctx, f)
}

// Delete removes both the database row and the transcription's stored
// files. The row goes first: once it is gone, a retried Delete (or a
// concurrent Get) must see ErrCodeNotFound rather than racing a
// half-completed cleanup. Deleting a still-running job is allowed --
// Repository.Delete does not require a terminal status -- since an
// operator asking to delete something is not obliged to cancel it first.
//
// A failure removing the stored files is logged rather than returned:
// once the row is gone, returning an error here would invite a caller to
// retry a call that will now always fail with ErrCodeNotFound instead, and
// leaked bytes with no row pointing at them are retention's problem to
// eventually notice, not this caller's to fix by hand.
func (s *Service) Delete(ctx context.Context, id string) error {
	if err := validateID(id); err != nil {
		return err
	}
	if err := s.repo.Delete(ctx, id); err != nil {
		return err
	}
	if err := s.store.RemoveDir(ctx, transcriptionDir(id)); err != nil {
		s.logger.ErrorContext(ctx, "delete: failed to remove stored files",
			"transcription_id", id, "error", err)
	}
	return nil
}

// Cancel requests cancellation of the transcription with the given id,
// delegating to Repository.RequestCancel and surfacing its ErrCodeConflict
// unchanged when the job is already terminal.
func (s *Service) Cancel(ctx context.Context, id string) (*domain.Transcription, error) {
	if err := validateID(id); err != nil {
		return nil, err
	}
	return s.repo.RequestCancel(ctx, id)
}

// Retry manually requeues the transcription with the given id, delegating
// to Repository.Requeue and surfacing its ErrCodeConflict unchanged unless
// the job is failed or canceled.
func (s *Service) Retry(ctx context.Context, id string) (*domain.Transcription, error) {
	if err := validateID(id); err != nil {
		return nil, err
	}
	return s.repo.Requeue(ctx, id)
}

// Prioritize puts the transcription with the given id first in the queue,
// delegating to Repository.Prioritize. The job starts a new attempt when a
// worker slot is free. Prioritize surfaces ErrCodeConflict unchanged unless
// the job is pending or failed.
func (s *Service) Prioritize(ctx context.Context, id string) (*domain.Transcription, error) {
	if err := validateID(id); err != nil {
		return nil, err
	}
	return s.repo.Prioritize(ctx, id)
}

// OpenSource returns a seekable reader over the transcription's originally
// uploaded source file, alongside the transcription itself so the caller
// (internal/httpapi) can set a filename and content type without a second
// Get. It returns ErrCodeNotFound both when the row does not exist and
// when the row exists but its stored file is gone -- from an API caller's
// point of view those are the same fact: there is nothing to download.
func (s *Service) OpenSource(ctx context.Context, id string) (io.ReadSeekCloser, *domain.Transcription, error) {
	if err := validateID(id); err != nil {
		return nil, nil, err
	}
	row, err := s.repo.Get(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	rc, _, err := s.store.Open(ctx, row.SourcePath)
	if err != nil {
		return nil, nil, s.classifyStoreErr(err, "open source: open stored file")
	}
	return rc, row, nil
}

// validateID rejects a malformed id before it ever reaches a database
// round-trip -- a garbage path segment (e.g. from a hand-crafted URL) is
// invalid input, not a lookup that happens to find nothing.
func validateID(id string) error {
	if _, err := uuid.Parse(id); err != nil {
		return domain.New(domain.ErrCodeInvalidInput, fmt.Sprintf("invalid transcription id %q", id)).
			WithDetails("field", "id")
	}
	return nil
}

// transcriptionDir is the stable, derived storage directory for a
// transcription with the given id: transcriptions/<id>. It is the one
// place this layout is spelled out, so Submit (which writes into it) and
// Delete (which removes it wholesale) can never drift apart.
func transcriptionDir(id string) string {
	return path.Join("transcriptions", id)
}

// classifyStoreErr turns an error returned by Store into a *domain.Error
// suitable to hand back to a caller. Store implementations are not
// required to return *domain.Error themselves (internal/storage/localfs,
// the production implementation, is a leaf package that cannot import
// domain -- see its own package doc) -- so this is the one place Service
// recognises localfs's exported sentinels (ErrDiskFull, ErrNotExist) via
// errors.Is and maps them to the matching ErrorCode. Anything already a
// *domain.Error (e.g. from a future or test Store implementation) passes
// through unchanged; anything else becomes ErrCodeInternal, since an
// unclassified storage failure is a bug to investigate, not something to
// paper over with a guess.
func (s *Service) classifyStoreErr(err error, msg string) error {
	if err == nil {
		return nil
	}
	var derr *domain.Error
	if errors.As(err, &derr) {
		return derr
	}
	if errors.Is(err, localfs.ErrDiskFull) {
		return domain.Wrap(domain.ErrCodeDiskFull, "disk full while writing to storage", err)
	}
	if errors.Is(err, localfs.ErrNotExist) {
		return domain.Wrap(domain.ErrCodeNotFound, "stored file not found", err)
	}
	// Checked before the ErrCodeInternal fallback below, not after: an
	// ordinary client disconnect mid-upload (Ctrl-C, a dropped connection)
	// surfaces here as localfs wrapping ctx.Err() with fmt.Errorf("%w", ...)
	// -- errors.Is still finds the sentinel through that wrapping, but it
	// is not a *domain.Error, so without this branch it would fall through
	// to ErrCodeInternal below and domain.CodeOf would never reach its own
	// context.Canceled/context.DeadlineExceeded handling (CodeOf stops at
	// the first *domain.Error it finds via errors.As). That turned a
	// routine cancellation into a logged ERROR and a counted 500 instead of
	// the 499/504 it actually is.
	if errors.Is(err, context.Canceled) {
		return domain.Wrap(domain.ErrCodeCanceled, "upload canceled", err)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return domain.Wrap(domain.ErrCodeTimeout, "upload deadline exceeded", err)
	}
	return domain.Wrap(domain.ErrCodeInternal, msg, err)
}

// sanitizeFilename turns a caller-supplied, hostile-by-default filename
// into (a) the base name stored on domain.Audio.Filename and (b) the
// lowercased extension used to name the stored source file
// (transcriptions/<id>/source<ext>). It strips any directory component --
// forward or backward slash, so "../../etc/passwd" collapses to "passwd"
// regardless of which the caller's platform used -- along with any
// remaining separator and control characters, and caps the result's
// length (see maxSanitizedFilenameBytes) so neither the filesystem nor the
// database is ever handed an unbounded value. name is never empty on
// return: an input that sanitizes away to nothing becomes "upload".
func sanitizeFilename(name string) (filename, ext string) {
	name = strings.ReplaceAll(strings.TrimSpace(name), `\`, "/")
	name = path.Base(name)
	if name == "" || name == "." || name == "/" {
		name = "upload"
	}

	var b strings.Builder
	for _, r := range name {
		if r < 0x20 || r == 0x7f || r == '/' || r == '\\' {
			continue // strip control characters and any residual separator
		}
		b.WriteRune(r)
	}
	cleaned := b.String()
	if cleaned == "" || cleaned == "." || cleaned == ".." {
		cleaned = "upload"
	}
	if len(cleaned) > maxSanitizedFilenameBytes {
		cleaned = cleaned[:maxSanitizedFilenameBytes]
	}

	ext = strings.ToLower(fileExt(cleaned))
	if ext == "" {
		ext = defaultUploadExt
	}
	return cleaned, ext
}

// fileExt returns name's extension including its leading dot, or "" when
// name has none. Unlike path.Ext, a leading dot with no other dot (a
// dotfile like ".gitignore") and a bare trailing dot both count as "no
// extension" rather than as an extension equal to the whole name -- the
// distinction that matters here, since an "extension" that swallowed the
// entire sanitized filename would be a useless, unbounded suffix on the
// stored source file's name.
func fileExt(name string) string {
	i := strings.LastIndexByte(name, '.')
	if i <= 0 || i == len(name)-1 {
		return ""
	}
	return name[i:]
}
