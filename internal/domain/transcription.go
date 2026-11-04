package domain

import "time"

// Audio is the metadata of the uploaded source. Filename, MediaType and
// SizeBytes are known at upload time; DurationSeconds, SampleRateHz,
// Channels and Codec are zero until the worker's ffprobe step fills them
// in.
type Audio struct {
	// Filename is the sanitized original name, base name only — never a
	// path, never the caller's raw (possibly hostile) input.
	Filename  string `json:"filename"`
	MediaType string `json:"mediaType"`
	SizeBytes int64  `json:"sizeBytes"`
	// DurationSeconds is 0 until probed.
	DurationSeconds float64 `json:"durationSeconds"`
	// SampleRateHz is 0 until probed.
	SampleRateHz int `json:"sampleRateHz"`
	// Channels is 0 until probed.
	Channels int `json:"channels"`
	// Codec is "" until probed.
	Codec string `json:"codec"`
}

// Transcription is the aggregate root of the whole service: one row per
// submitted audio file, its current lifecycle Status (see Status), the
// Options it was submitted with, what is known about its Audio, and —
// once the job resolves — either a Result or an Error.
type Transcription struct {
	// ID uniquely identifies this transcription. UUIDv4.
	ID      string  `json:"id"`
	Status  Status  `json:"status"`
	Options Options `json:"options"`
	Audio   Audio   `json:"audio"`
	// SourcePath is the store-relative path of the uploaded original (see
	// internal/app's fixed "transcriptions/<id>/source<ext>" layout). Never
	// exposed to API callers directly — GET .../audio streams it instead.
	SourcePath string `json:"-"`
	// Result is set once Status is StatusSucceeded.
	Result *Result `json:"result,omitempty"`
	// Error is set once Status is StatusFailed (and may still be present,
	// as the most recent failure, on a StatusPending row awaiting retry).
	Error *Error `json:"error,omitempty"`
	// Attempts counts how many times a worker has tried to run this job.
	Attempts int `json:"attempts"`
	// MaxAttempts caps Attempts before the job is marked permanently
	// StatusFailed regardless of Error.Code.Retryable.
	MaxAttempts int       `json:"maxAttempts"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
	// StartedAt is when a worker first began running this job. Nil until
	// then.
	StartedAt *time.Time `json:"startedAt,omitempty"`
	// CompletedAt is when the job reached a terminal Status. Nil until
	// then.
	CompletedAt *time.Time `json:"completedAt,omitempty"`
	// PriorityAt is when the user asked to process this job first
	// (Repository.Prioritize). Nil means no priority. Queue.Claim takes jobs
	// with a PriorityAt before all others, newest first. Every terminal
	// transition clears it.
	PriorityAt *time.Time `json:"priorityAt,omitempty"`
	// NextAttemptAt is when this job becomes eligible to be claimed. Never
	// exposed to API callers: it is a queue implementation detail.
	NextAttemptAt *time.Time `json:"-"`
	// LeaseOwner identifies the worker instance currently holding the
	// claim lease, if any. Nil when unclaimed. This is for logs and ops
	// visibility only -- it is per-process, not per-claim, so it must
	// never guard a Heartbeat/Finish write (see LeaseToken).
	LeaseOwner *string `json:"-"`
	// LeaseToken is THE lease guard: a fresh UUID minted by every Claim
	// and required, verbatim, by the Heartbeat/Finish call that ends this
	// particular claim. Unlike LeaseOwner it is per-claim, not per-worker,
	// so a stale execution left running past a lease expiry (its own
	// process re-claimed the row, possibly under the same LeaseOwner)
	// carries the previous claim's token and is rejected with
	// ErrCodeConflict rather than allowed to finish or heartbeat a job it
	// no longer owns.
	LeaseToken *string `json:"-"`
	// LeaseExpiresAt is when the current lease, if any, expires and
	// becomes eligible for another worker to reclaim.
	LeaseExpiresAt *time.Time `json:"-"`
}

// PurgedTranscription is the light-weight record Queue.PurgeTerminalBefore
// returns for each row it deletes: internal/retention's sweeper only ever
// needs ID (to remove the transcription's storage directory) and
// SourcePath (documenting what was deleted), so returning a full
// Transcription there would mean scanning and marshalling columns --
// including the potentially large Result and Error JSONB -- that nothing
// reads.
type PurgedTranscription struct {
	ID         string
	SourcePath string
}

// TranscribeRequest is what internal/jobs hands to a Transcriber
// implementation for a single job.
type TranscribeRequest struct {
	// AudioPath is the ABSOLUTE path to the normalized 16 kHz mono s16 WAV
	// (internal/audio's Normalize output), not the original upload.
	AudioPath string
	Options   Options
}

// FinishRequest is what a worker hands to Queue.Finish once a job has
// resolved — successfully, unsuccessfully, or back to pending for another
// attempt.
type FinishRequest struct {
	ID string
	// LeaseToken is the claim this outcome belongs to -- it must match the
	// Transcription.LeaseToken minted by the Claim that produced this
	// attempt, not merely the worker's own identity (see
	// Transcription.LeaseToken for why LeaseOwner cannot be used here).
	LeaseToken string
	// Status is the job's outcome: succeeded, failed, canceled, or pending
	// when this Finish call represents a scheduled retry rather than a
	// terminal state.
	Status Status
	// Result is set when Status == StatusSucceeded.
	Result *Result
	// Err is set when Status == StatusFailed.
	Err *Error
	// Audio carries probed metadata to persist. Nil leaves the row's
	// existing Audio unchanged.
	Audio *Audio
	// NextAttemptAt is set when Status == StatusPending, scheduling the
	// retry.
	NextAttemptAt *time.Time
}
