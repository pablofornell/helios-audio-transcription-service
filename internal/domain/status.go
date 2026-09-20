package domain

// Status is the lifecycle state of a Transcription. Transitions are
// enforced here (CanTransitionTo), not by callers re-deriving the machine:
// a fresh job starts Pending, a worker claim moves it to Running, Running
// resolves to one of the three terminal states or loops back to Pending on
// an automatic retry (backoff), and either terminal failure state can be
// moved back to Pending by a manual retry (internal/app's Retry).
type Status string

const (
	// StatusPending means the job is queued and waiting for a worker to
	// claim it (immediately, or at NextAttemptAt if this is a retry).
	StatusPending Status = "pending"
	// StatusRunning means a worker currently holds the lease and is
	// probing, normalizing or transcribing the audio.
	StatusRunning Status = "running"
	// StatusSucceeded means the job completed and its Result is final.
	StatusSucceeded Status = "succeeded"
	// StatusFailed means the job exhausted its attempts or hit a
	// non-retryable error. Error explains why.
	StatusFailed Status = "failed"
	// StatusCanceled means a caller canceled the job before it finished.
	StatusCanceled Status = "canceled"
)

// Valid reports whether s is one of the five defined Status values.
func (s Status) Valid() bool {
	switch s {
	case StatusPending, StatusRunning, StatusSucceeded, StatusFailed, StatusCanceled:
		return true
	default:
		return false
	}
}

// Terminal reports whether a Transcription in this Status will never change
// state again on its own — i.e. no worker will pick it up without a manual
// retry. Terminal states are Succeeded, Failed and Canceled.
func (s Status) Terminal() bool {
	switch s {
	case StatusSucceeded, StatusFailed, StatusCanceled:
		return true
	default:
		return false
	}
}

// CanTransitionTo reports whether moving from s to next is a legal step in
// the job lifecycle:
//
//   - Pending  -> Running                          (a worker claims it)
//   - Running  -> Succeeded | Failed | Canceled     (the job resolves)
//   - Running  -> Pending                           (automatic retry backoff)
//   - Failed   -> Pending                           (manual retry)
//   - Canceled -> Pending                           (manual retry)
//
// Succeeded has no outgoing transition: once a job has a final Result it is
// done for good, even via manual retry. Every other combination, including
// any self-transition, is rejected.
func (s Status) CanTransitionTo(next Status) bool {
	switch s {
	case StatusPending:
		return next == StatusRunning
	case StatusRunning:
		switch next {
		case StatusSucceeded, StatusFailed, StatusCanceled, StatusPending:
			return true
		default:
			return false
		}
	case StatusFailed, StatusCanceled:
		return next == StatusPending
	default:
		return false
	}
}
