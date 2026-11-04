// Package app holds the service's business logic and, in this file, the
// ports it drives it through.
//
// The interfaces below are declared by the consumer, not the implementer:
// internal/postgres, internal/storage/localfs, internal/audio and
// internal/transcriber satisfy them structurally and never import this
// package. That keeps the dependency arrows pointing one way -- adapters
// depend on internal/domain alone -- so an adapter can be swapped, faked or
// deleted without app noticing, and so the fakes in these packages' tests
// stay trivial.
package app

import (
	"context"
	"io"
	"time"

	"github.com/pablofornell/helios-audio-transcription-service/internal/domain"
)

// ListFilter and Page are aliases for the domain types, not copies. They
// have to be: internal/postgres satisfies Repository without importing this
// package, so the filter and page types it names must be identical to the
// ones named here, and Go interface satisfaction has no structural fallback.
// See internal/domain/listing.go.
type ListFilter = domain.ListFilter

// Page is one page of a listing. See ListFilter.
type Page = domain.Page

// Repository is the durable record of every transcription. Its methods are
// the read/write half of persistence that the API layer drives; Queue is the
// half the worker drives, and both are satisfied by the same Postgres type.
type Repository interface {
	Create(ctx context.Context, t *domain.Transcription) error
	// Get returns ErrCodeNotFound when id does not exist.
	Get(ctx context.Context, id string) (*domain.Transcription, error)
	List(ctx context.Context, f ListFilter) (Page, error)
	Delete(ctx context.Context, id string) error
	// RequestCancel marks a job for cancellation. A pending job is canceled
	// outright; a running one is flagged so its worker notices at the next
	// heartbeat. Returns ErrCodeConflict when the job is already terminal.
	RequestCancel(ctx context.Context, id string) (*domain.Transcription, error)
	// Requeue puts a failed or canceled job back on the queue with a fresh
	// attempt count. Returns ErrCodeConflict from any other status.
	Requeue(ctx context.Context, id string) (*domain.Transcription, error)
	// Prioritize moves a pending or failed job to the head of the queue:
	// it resets the attempts, the error and the backoff, and sets
	// PriorityAt, so Claim takes the job before all others. Returns
	// ErrCodeConflict from any other status, and ErrCodeNotFound for an
	// unknown id.
	Prioritize(ctx context.Context, id string) (*domain.Transcription, error)
}

// Queue is the worker-facing half of persistence: a lease-based job queue
// backed by the same rows Repository reads.
type Queue interface {
	// Claim atomically takes ownership of one due job for lease and mints a
	// fresh LeaseToken for this claim (see domain.Transcription.LeaseToken).
	// owner is an opaque, per-process label carried for logs/ops only --
	// it is never a correctness guard. Claim returns (nil, nil) -- not an
	// error -- when there is nothing to do, because an idle queue is the
	// normal case and must not be logged as a failure. A job with
	// cancel_requested already set is never returned: see RequestCancel.
	Claim(ctx context.Context, owner string, lease time.Duration) (*domain.Transcription, error)
	// Heartbeat extends the lease on id identified by leaseToken -- the
	// value Claim minted for this specific attempt, not the caller's
	// LeaseOwner -- and reports whether cancellation has been requested in
	// the meantime. A lost lease (leaseToken stale or the row no longer
	// running) is ErrCodeConflict: the caller must stop rather than finish
	// work it no longer owns, even if it is, in fact, the same process that
	// re-claimed the row under a fresh token.
	Heartbeat(ctx context.Context, id, leaseToken string, lease time.Duration) (canceled bool, err error)
	// Finish applies a terminal transition, or schedules a retry when
	// req.Status is pending. It is lease-guarded by req.LeaseToken, exactly
	// like Heartbeat.
	Finish(ctx context.Context, req domain.FinishRequest) error
	// ReleaseExpired returns jobs whose lease outlived their worker to the
	// pending pool, and reports how many it moved. It also reaps any
	// pending row still flagged cancel_requested (a retryable failure that
	// landed before a worker's next heartbeat could see the flag) straight
	// to StatusCanceled, counted in the same total: it is the natural
	// ticker-driven place to do it, since such a row would otherwise sit
	// forever, unclaimable by Claim's `AND NOT cancel_requested` predicate
	// but never actually resolved either.
	ReleaseExpired(ctx context.Context) (int, error)
	// ReleaseForShutdown returns a claimed job to the queue WITHOUT
	// consuming the attempt it was claimed under, for when this process is
	// stopping rather than the job failing: an ordinary deploy must not
	// permanently fail a job that happened to be on its last attempt when
	// SIGTERM arrived. It is guarded by leaseToken exactly like Heartbeat
	// and Finish -- a lease already lost to a re-claim is ErrCodeConflict,
	// which the caller logs and moves on from, since another worker now
	// owns the row.
	ReleaseForShutdown(ctx context.Context, id, leaseToken string) error
	// PurgeTerminalBefore deletes terminal jobs completed before cutoff and
	// returns a light-weight record of the deleted rows, so the caller can
	// delete their stored audio -- the database row is not the only thing
	// retention has to reclaim. A single call purges at most an internal,
	// bounded batch size; a sweep needing more than one batch will pick up
	// the remainder on its next tick rather than hold one unbounded
	// transaction open.
	PurgeTerminalBefore(ctx context.Context, cutoff time.Time) ([]domain.PurgedTranscription, error)
}

// Store is the object store holding uploaded audio and the normalized WAV
// derived from it. Paths are store-relative and hardened against traversal
// by the implementation.
type Store interface {
	Put(ctx context.Context, relPath string, r io.Reader) (int64, error)
	Open(ctx context.Context, relPath string) (io.ReadSeekCloser, int64, error)
	// AbsPath resolves a store-relative path for handing to a subprocess.
	// ffmpeg and whisper-cli take filenames, not file descriptors, which is
	// the only reason this escape hatch exists.
	AbsPath(relPath string) (string, error)
	Remove(ctx context.Context, relPath string) error
	RemoveDir(ctx context.Context, relDir string) error
	// FreeBytes reports free space on the volume backing the store, so a
	// submission can be refused before it fills the disk rather than after.
	FreeBytes() (uint64, error)
}

// Transcriber turns a normalized WAV into a transcript. The production
// implementation shells out to whisper-cli; the fake one does not, which is
// what lets the whole pipeline be tested without model weights.
type Transcriber interface {
	Transcribe(ctx context.Context, req domain.TranscribeRequest) (*domain.Result, error)
	// Available reports whether the engine could run right now -- binary
	// resolvable, model directory readable. It backs the readiness probe,
	// so it must be cheap and must not actually transcribe anything.
	Available(ctx context.Context) error
}

// AudioProcessor inspects and conditions uploaded audio before transcription.
type AudioProcessor interface {
	// Probe fills in the decoded metadata of an upload: duration, sample
	// rate, channels, codec. It is also the gate that rejects files with no
	// audio stream or more audio than the service will accept.
	Probe(ctx context.Context, absPath string) (domain.Audio, error)
	// Normalize writes srcAbs to dstAbs as 16 kHz mono signed-16-bit WAV,
	// the only format whisper.cpp accepts.
	Normalize(ctx context.Context, srcAbs, dstAbs string) error
}

// HealthChecker is one named dependency probe aggregated by /readyz.
type HealthChecker interface {
	Name() string
	Check(ctx context.Context) error
}

// Clock is wall-clock time, injected so tests can make time deterministic
// rather than sleeping.
type Clock interface {
	Now() time.Time
}

// IDGenerator mints transcription IDs, injected for the same reason as Clock.
type IDGenerator interface {
	NewID() string
}

// SystemClock is the real implementation of Clock.
type SystemClock struct{}

// Now returns the current wall-clock time in UTC. UTC is deliberate: every
// timestamp this service stores or serves is UTC, and normalizing at the
// source is cheaper than remembering to do it at each call site.
func (SystemClock) Now() time.Time { return time.Now().UTC() }
