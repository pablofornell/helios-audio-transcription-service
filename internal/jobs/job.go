package jobs

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/pablofornell/helios-audio-transcription-service/internal/domain"
)

// wavRelPath is the store-relative path of the normalized WAV a job
// produces for id, on the claim identified by leaseToken. It is keyed on
// the lease token, not the attempt number, because the attempt number is
// NOT a reliable per-claim discriminator: Store.Requeue (a manual retry)
// resets Attempts to 0, so a retry's first attempt can collide with the
// attempt number of a still-live stale execution from before the job was
// ever requeued, and that stale worker's own cleanup (see runOne) could
// then unlink the live retry's WAV out from under it. leaseToken has no
// such gap -- app.Queue.Claim mints a fresh UUID on every single claim,
// requeue or not -- so it is the only value that is actually guaranteed
// unique per attempt. See runOne's lease-lost handling for the other half
// of the guarantee this path uniqueness depends on.
func wavRelPath(id, leaseToken string) string {
	return fmt.Sprintf("transcriptions/%s/audio-%s.wav", id, leaseToken)
}

// startJob registers t as in flight and launches the goroutine that runs it
// end to end, adding it to p.wg so Shutdown can wait for it. It reports
// whether the job was actually started: false means Shutdown had already
// set p.stopping by the time this call took p.mu, and the caller
// (claimAndStart) must leave the just-claimed job exactly as it is --
// running, its lease untouched -- for the next lease-expiry sweep to
// recover, rather than start or report on it.
//
// The stopping check and registration (p.inFlight, wg.Add(1)) happen
// together, in the SAME p.mu critical section Shutdown uses to set
// p.stopping (see Shutdown and the stopping field's own doc): that is what
// actually closes the race between a Claim already in flight when Shutdown
// begins and Shutdown's wg.Wait() returning too early. Without a shared
// critical section, Shutdown could observe stopping == true, call
// wg.Wait(), and have it return on a zero count while this very job's
// wg.Add(1) had not yet run -- reporting every job drained while one was,
// in fact, only just starting.
//
// The job context is deliberately derived from context.Background(), not
// from Run's ctx: Shutdown gives in-flight jobs a chance to finish on their
// own before it cancels anything (see Shutdown), which would be impossible
// if canceling Run's ctx canceled every job context as a side effect.
// jobHandle.cancel is the only thing that ever cancels it, whether that is
// a heartbeat that noticed a cancellation or a lost lease, or Shutdown
// forcing a drain deadline.
func (p *Pool) startJob(t *domain.Transcription) bool {
	jobCtx, cancel := context.WithTimeout(context.Background(), p.cfg.JobTimeout)
	handle := &jobHandle{cancel: cancel, done: make(chan struct{})}

	p.mu.Lock()
	if p.stopping.Load() {
		p.mu.Unlock()
		cancel()
		return false
	}
	p.inFlight[t.ID] = handle
	p.wg.Add(1)
	p.mu.Unlock()

	p.metrics.JobsInFlightInc()

	go func() {
		defer p.wg.Done()
		defer close(handle.done)
		defer cancel()
		defer func() {
			p.mu.Lock()
			delete(p.inFlight, t.ID)
			p.mu.Unlock()
			p.metrics.JobsInFlightDec()
		}()

		p.runOne(jobCtx, cancel, handle, t)
	}()
	return true
}

// runOne executes one claimed attempt end to end: it heartbeats t's lease
// for as long as p.execute is running, and reports the result to
// Queue.Finish — unless the lease was found to be lost, in which case
// jobCtx is canceled and Finish is deliberately never called (the row
// belongs to whoever holds the lease now). Writing any outcome for a row
// this process no longer holds the lease on would resurrect a job someone
// else has already moved on from. This is the single most important rule
// in this package.
//
// A forced shutdown cancel is handled differently: if execute nonetheless
// came back with a real result (runErr == nil), that result is NOT
// discarded — see the runErr == nil case below — because a completed
// transcription is a fact, and dropping it would otherwise leave the row
// stuck running until lease expiry records it as permanently failed for a
// job that in truth succeeded. An execute that was actually interrupted
// (runErr != nil, e.g. its context was the one Shutdown force-canceled) is
// abandoned instead -- but "abandoned" means giving the claim back via
// Queue.ReleaseForShutdown WITHOUT consuming its attempt (see
// releaseForShutdown), not merely leaving the row running for lease expiry
// to eventually recover: a job on its last attempt that was interrupted by
// a routine deploy, rather than a genuine failure, must not come out of
// that deploy permanently failed. Lease expiry is only the backstop for
// when that best-effort release itself does not land.
func (p *Pool) runOne(jobCtx context.Context, cancelJob context.CancelFunc, handle *jobHandle, t *domain.Transcription) {
	// A claimed row with no lease token is a programming error, not a
	// runtime condition to recover from: Queue.Claim's contract is that it
	// always mints one (see domain.Transcription.LeaseToken). Refuse to
	// run the job rather than heartbeat or Finish with an empty token,
	// which would either panic on dereference or -- far worse -- guard a
	// write with a token that matches nothing, defeating the whole point
	// of a per-claim guard. The row is simply left running; the next
	// lease-expiry sweep recovers it exactly as if this worker had
	// crashed right after claiming it.
	if t.LeaseToken == nil {
		p.logger.ErrorContext(context.Background(), "jobs: claimed row has a nil lease token, refusing to run it",
			"transcription_id", t.ID)
		return
	}
	leaseToken := *t.LeaseToken

	hbCtx, stopHeartbeat := context.WithCancel(context.Background())
	defer stopHeartbeat()

	var canceledByUser atomic.Bool
	var leaseLost atomic.Bool
	hbDone := make(chan struct{})
	go func() {
		defer close(hbDone)
		p.heartbeatLoop(hbCtx, cancelJob, t.ID, leaseToken, &canceledByUser, &leaseLost)
	}()

	start := p.clock.Now()
	result, audio, runErr := p.execute(jobCtx, t, leaseToken)

	stopHeartbeat()
	<-hbDone

	// The heartbeat loop has now fully stopped, so leaseLost's value here
	// is final: nothing can set it after this point. That is exactly why
	// the normalized WAV's removal is decided here rather than inside
	// execute's own defer — a lease-lost worker must never unlink the WAV
	// file a subsequent attempt owns (see wavRelPath and finding 4), and
	// this is the earliest point at which "lease lost" is known for sure.
	if leaseLost.Load() {
		p.logger.InfoContext(context.Background(), "jobs: abandoning job, lease no longer held by this worker",
			"transcription_id", t.ID)
		return
	}
	p.removeWAV(wavRelPath(t.ID, leaseToken))

	if handle.forced.Load() && runErr != nil {
		// A shutdown-forced cancel is not a failed attempt: give the claim
		// back via Queue.ReleaseForShutdown, WITHOUT consuming the attempt
		// it was claimed under, rather than silently leaving the row
		// running for lease expiry to recover. Leaving it for expiry alone
		// would be indistinguishable from a genuine crash, and
		// ReleaseExpired fails a row outright once its attempts are spent
		// (see its own doc) -- so a job on its last attempt that happened
		// to be running when an ordinary deploy sent SIGTERM would be
		// permanently failed for a routine restart, not a real defect. See
		// releaseForShutdown for why jobCtx (already canceled) cannot be
		// used directly here, and why a failure of the release itself is
		// not fatal: lease expiry remains the backstop.
		p.logger.WarnContext(context.Background(), "jobs: abandoning job, pool is shutting down",
			"transcription_id", t.ID)
		p.releaseForShutdown(jobCtx, t.ID, leaseToken)
		return
	}

	p.finish(jobCtx, t, leaseToken, result, audio, runErr, canceledByUser.Load(), p.clock.Now().Sub(start))
}

// releaseForShutdown gives leaseToken's claim on id back to the queue
// without consuming its attempt (see Queue.ReleaseForShutdown), using a
// context derived from jobCtx via context.WithoutCancel plus a short,
// fresh timeout -- exactly the same reasoning as finish's own finishCtx
// (see finish's doc): jobCtx is the reason this job stopped (a forced
// shutdown canceled it), so wrapping it directly would still be
// immediately Done and the write would never reach the database. A
// failure here -- including ErrCodeConflict, meaning another worker
// already re-claimed the row -- is logged and never propagated: the
// lease's natural expiry is still the backstop if this best-effort write
// does not land.
func (p *Pool) releaseForShutdown(jobCtx context.Context, id, leaseToken string) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(jobCtx), finishTimeout)
	defer cancel()

	if err := p.queue.ReleaseForShutdown(ctx, id, leaseToken); err != nil {
		if domain.CodeOf(err) == domain.ErrCodeConflict {
			p.logger.InfoContext(ctx, "jobs: release-for-shutdown conflict, lease no longer held by this worker",
				"transcription_id", id)
			return
		}
		p.logger.ErrorContext(ctx, "jobs: release-for-shutdown failed; leaving the lease for expiry to recover it instead",
			"transcription_id", id, "error", err)
	}
}

// heartbeatLoop extends t's lease every Config.HeartbeatInterval until hbCtx
// is done. leaseToken is the per-claim token minted by the Claim that
// produced this attempt (see domain.Transcription.LeaseToken) -- it, not
// p.owner, is what Heartbeat guards on: p.owner is shared by every claim
// this process ever makes, so guarding on it would let a stale execution
// from a previous claim of the same row extend a lease that is no longer
// its own (finding 1). The instant a Heartbeat call reports cancellation
// was requested, or fails with domain.ErrCodeConflict (a lost lease --
// including a re-claim by this same process, under this same owner), it
// stores the corresponding flag and cancels the job context: this is the
// ONLY channel by which a cancel request or a lost lease reaches a job
// that is already running, per app.Queue.Heartbeat's contract. Any other
// heartbeat error is treated as transient (logged, retried next tick): the
// lease may simply expire and be reclaimed, which the next heartbeat will
// then observe as a conflict.
func (p *Pool) heartbeatLoop(hbCtx context.Context, cancelJob context.CancelFunc, id, leaseToken string, canceledByUser, leaseLost *atomic.Bool) {
	ticker := time.NewTicker(p.cfg.HeartbeatInterval)
	defer ticker.Stop()

	for {
		select {
		case <-hbCtx.Done():
			return
		case <-ticker.C:
			canceled, err := p.queue.Heartbeat(hbCtx, id, leaseToken, p.cfg.LeaseDuration)
			if err != nil {
				if domain.CodeOf(err) == domain.ErrCodeConflict {
					leaseLost.Store(true)
					cancelJob()
					return
				}
				p.logger.WarnContext(hbCtx, "jobs: heartbeat error, will retry",
					"transcription_id", id, "error", err)
				continue
			}
			if canceled {
				canceledByUser.Store(true)
				cancelJob()
				return
			}
		}
	}
}

// execute runs the per-job pipeline described in CONTRACT.md section 1
// steps 2-6: resolve the stored source, probe it, normalize it to a 16 kHz
// mono WAV, and transcribe that WAV. Unlike an earlier version of this
// method, the normalized WAV is deliberately NOT removed here: runOne
// removes it after execute returns, and only once it knows for certain
// whether this attempt's lease was lost (see runOne) — removing it
// unconditionally here raced a lease-lost worker's cleanup against a
// subsequent attempt's own use of its (now per-claim, see wavRelPath) WAV
// path.
//
// The returned *domain.Audio is the upload-known Audio (Filename,
// MediaType, SizeBytes) merged with whatever Probe learned, or nil if Probe
// itself never completed; the caller persists it via FinishRequest.Audio
// regardless of whether execute ultimately succeeds, since a duration and
// codec probed just before a later step failed is still true and still
// worth keeping.
func (p *Pool) execute(ctx context.Context, t *domain.Transcription, leaseToken string) (*domain.Result, *domain.Audio, error) {
	wavRel := wavRelPath(t.ID, leaseToken)

	srcAbs, err := p.store.AbsPath(t.SourcePath)
	if err != nil {
		return nil, nil, domain.Wrap(domain.ErrCodeInternal, "jobs: resolve source path", err)
	}

	probed, err := p.audio.Probe(ctx, srcAbs)
	if err != nil {
		return nil, nil, err
	}
	audio := t.Audio
	audio.DurationSeconds = probed.DurationSeconds
	audio.SampleRateHz = probed.SampleRateHz
	audio.Channels = probed.Channels
	audio.Codec = probed.Codec

	wavAbs, err := p.store.AbsPath(wavRel)
	if err != nil {
		return nil, &audio, domain.Wrap(domain.ErrCodeInternal, "jobs: resolve normalized wav path", err)
	}

	if err := p.audio.Normalize(ctx, srcAbs, wavAbs); err != nil {
		return nil, &audio, err
	}

	result, err := p.transcriber.Transcribe(ctx, domain.TranscribeRequest{AudioPath: wavAbs, Options: t.Options})
	if err != nil {
		return nil, &audio, err
	}

	return result, &audio, nil
}

// removeWAV best-effort deletes the normalized WAV at relPath, using its
// own fresh, bounded context rather than the job's — which by the time
// runOne calls this (after execute has returned, possibly via a canceled
// or expired jobCtx) may no longer be usable at all. A failure here is
// logged and never propagated: it must not turn a successful transcription
// into a failed job, and the file is at worst a modest amount of wasted
// disk that the next occupant of this path will simply overwrite. Callers
// must never invoke this for an attempt whose lease was lost — see
// runOne's doc.
func (p *Pool) removeWAV(relPath string) {
	ctx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
	defer cancel()
	if err := p.store.Remove(ctx, relPath); err != nil {
		p.logger.Error("jobs: remove normalized wav failed", "path", relPath, "error", err)
	}
}

// finish turns runErr (and canceledByUser) into a domain.FinishRequest and
// reports it via Queue.Finish, then records the attempt's metrics.
//
// finishCtx is derived from jobCtx via context.WithoutCancel, then given
// its own short, fresh timeout — NOT jobCtx itself, and NOT a plain
// context.WithTimeout(jobCtx, ...). By the time finish runs, jobCtx may
// already be canceled or expired (its own JobTimeout elapsed, a heartbeat
// noticed a cancellation, or — per runOne's B2 fix — Shutdown force-
// canceled it while a real result was still in hand); wrapping an already-
// canceled context in a timeout would still be immediately Done, so the
// write would silently never reach the database. context.WithoutCancel
// strips that cancellation (while preserving any values jobCtx carries)
// before the bounded timeout is applied, so the write still has to reach
// the database, but Finish's own lease guard is what keeps it safe to
// attempt regardless of how jobCtx ended.
//
// Retry eligibility is decided against t's own Attempts and MaxAttempts —
// set on t by Queue.Claim and, originally, by the submission that created
// the row (see app.Service.Submit) — rather than Config.MaxAttempts,
// because MaxAttempts is per-transcription domain state, not a pool-wide
// constant: it is what CONTRACT.md's FinishRequest and Transcription types
// both model it as. Config.MaxAttempts exists purely so every other worker
// knob lives in one place; it is not consulted here.
func (p *Pool) finish(jobCtx context.Context, t *domain.Transcription, leaseToken string, result *domain.Result, audio *domain.Audio, runErr error, canceledByUser bool, elapsed time.Duration) {
	finishCtx, cancel := context.WithTimeout(context.WithoutCancel(jobCtx), finishTimeout)
	defer cancel()

	req := domain.FinishRequest{
		ID:         t.ID,
		LeaseToken: leaseToken,
		Audio:      audio,
	}

	switch {
	case runErr == nil:
		req.Status = domain.StatusSucceeded
		req.Result = result
	case canceledByUser:
		req.Status = domain.StatusCanceled
		req.Err = domain.AsError(runErr)
	default:
		req.Err = domain.AsError(runErr)
		if code := domain.CodeOf(runErr); code.Retryable() && t.Attempts < t.MaxAttempts {
			req.Status = domain.StatusPending
			next := p.clock.Now().Add(p.backoff(t.Attempts))
			req.NextAttemptAt = &next
		} else {
			req.Status = domain.StatusFailed
		}
	}

	if err := p.queue.Finish(finishCtx, req); err != nil {
		if domain.CodeOf(err) == domain.ErrCodeConflict {
			// The lease was lost between our last successful heartbeat and
			// this call (e.g. it expired right as execute returned).
			// Exactly as unexceptional as a heartbeat conflict — the row is
			// no longer ours to have finished.
			p.logger.InfoContext(finishCtx, "jobs: finish conflict, lease no longer held by this worker",
				"transcription_id", t.ID)
		} else {
			p.logger.ErrorContext(finishCtx, "jobs: finish failed", "transcription_id", t.ID, "error", err)
		}
		return
	}

	p.recordMetrics(req.Status, result, audio, elapsed)
}

// recordMetrics reports status to Metrics.TranscriptionCompleted for every
// terminal outcome (succeeded, failed or canceled — never the non-terminal
// StatusPending a scheduled retry represents, per domain.Status.Terminal),
// and, only on success, the run's duration and the audio's probed length.
func (p *Pool) recordMetrics(status domain.Status, result *domain.Result, audio *domain.Audio, elapsed time.Duration) {
	if status.Terminal() {
		p.metrics.TranscriptionCompleted(string(status))
	}
	if status != domain.StatusSucceeded {
		return
	}
	model := ""
	if result != nil {
		model = result.Model
	}
	p.metrics.TranscriptionDuration(model, elapsed)
	if audio != nil {
		p.metrics.TranscriptionAudioSeconds(audio.DurationSeconds)
	}
}
