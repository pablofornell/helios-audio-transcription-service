// Package retention implements the background sweep that makes the
// database purge itself: once a transcription has been terminal
// (succeeded, failed or canceled) for longer than Config.MaxAge, the sweep
// removes both its database row and the files it owns on disk, so operators
// do not have to do this by hand.
//
// Unlike internal/jobs' sweepLoop, which only ever recovers a lease, this
// package's sweep is the only place in Helios that deletes data outright —
// app.Queue.PurgeTerminalBefore does the database half atomically in one
// call (see its own doc on why the row is not the only thing retention has
// to reclaim), and this package does the file half afterward, best-effort:
// see sweepOnce for why a failure there is logged and skipped rather than
// retried or treated as reason to abort the rest of the batch.
package retention

import (
	"context"
	"log/slog"
	"time"

	"github.com/pablofornell/helios-audio-transcription-service/internal/app"
)

// transcriptionDir is the store-relative directory a transcription's files
// (its uploaded source and, while a job is running, its normalized WAV)
// live under. Fixed by CONTRACT.md section 5's storage layout.
func transcriptionDir(id string) string {
	return "transcriptions/" + id
}

// maxBatchesPerSweep bounds how many Queue.PurgeTerminalBefore batches one
// sweepOnce call may run. Queue.PurgeTerminalBefore purges at most an
// internal, bounded batch size per call (see its own doc); without this
// loop, a deployment completing more terminal jobs per Config.Interval than
// one batch's worth would never catch up at all -- its backlog, and the
// on-disk directories that go with it, would simply grow forever while
// Config.MaxAge was silently no longer honoured. The cap exists so that,
// symmetrically, a sweep that is very badly behind (retention disabled for a
// while, then re-enabled) cannot monopolize one tick indefinitely either;
// see sweepOnce for what happens when it is hit.
const maxBatchesPerSweep = 100

// Config is the sweep's tuning knobs, sourced from
// config.RetentionConfig's MaxAge and Interval (see CONTRACT.md section 4's
// HELIOS_RETENTION_* variables). Enabled is deliberately not modelled here:
// a disabled sweep is simply never started — see Run's own doc — which is
// the caller's decision to make, not this package's.
type Config struct {
	// MaxAge is how old a terminal transcription must be before the sweep
	// purges it.
	MaxAge time.Duration
	// Interval is how often the sweep runs.
	Interval time.Duration
}

// Deps bundles every dependency Sweeper needs. There are no optional
// fields: a Sweeper built with a nil Queue, Store, Clock or Logger is a
// construction bug, not a valid configuration.
type Deps struct {
	// Queue is where PurgeCandidates -- via PurgeTerminalBefore -- comes
	// from; it also deletes the purged rows as part of that same call.
	Queue app.Queue
	// Store owns the actual files a purged transcription's directory
	// contains; RemoveDir removes them.
	Store app.Store
	// Clock abstracts the current time used to compute the purge cutoff,
	// so tests can freeze it rather than depending on when they happen to
	// run.
	Clock app.Clock
	// Logger receives diagnostic logging: a summary after any sweep that
	// purged something (info), and a per-transcription file-removal
	// failure that did not abort the sweep (error).
	Logger *slog.Logger
}

// Sweeper is the retention sweep described in this package's doc. It is
// built once (New) and started with Run, exactly like jobs.Pool.
type Sweeper struct {
	queue  app.Queue
	store  app.Store
	clock  app.Clock
	logger *slog.Logger
	cfg    Config
}

// New builds a Sweeper from cfg and deps. It does not start sweeping until
// Run is called.
func New(cfg Config, deps Deps) *Sweeper {
	return &Sweeper{
		queue:  deps.Queue,
		store:  deps.Store,
		clock:  deps.Clock,
		logger: deps.Logger,
		cfg:    cfg,
	}
}

// Run drives the retention sweep until ctx is done, then returns nil. It
// runs one sweep immediately -- a service that has been down past several
// sweep intervals should not have to wait a full Config.Interval before its
// first one -- and then one more every Config.Interval after that. A failed
// individual sweep (Queue.PurgeTerminalBefore itself erroring) is logged
// and retried at the next tick, never fatal to the process; see sweepOnce.
//
// Run has no "disabled" mode of its own: when HELIOS_RETENTION_ENABLED is
// false, the caller simply never calls Run at all, which is a cleaner
// contract than a Config field this package would have to remember to
// check on every entry point.
func (s *Sweeper) Run(ctx context.Context) error {
	s.sweepOnce(ctx)

	ticker := time.NewTicker(s.cfg.Interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			s.sweepOnce(ctx)
		}
	}
}

// sweepOnce runs one purge pass to completion, not just one purge batch: it
// repeatedly asks Queue.PurgeTerminalBefore to delete transcriptions that
// have been terminal since before the cutoff -- each call is the database
// row's entire lifetime ending, atomically, in one step -- and, for each row
// a call deletes, removes that transcription's directory of files, until
// either a call comes back empty (this sweep is caught up: there is nothing
// left due before cutoff) or ctx is done. Looping here, rather than trusting
// one call per tick to be enough, is what lets retention actually catch up
// when a deployment completes more terminal jobs per Config.Interval than
// one batch's worth -- otherwise MaxAge would be silently no longer
// honoured and a full batch would look identical to "caught up" in the
// logs.
//
// A call returning fewer rows than Queue.PurgeTerminalBefore's own batch
// limit is not treated as a special "definitely done" signal -- this
// package deliberately does not know that limit, it is an implementation
// detail of the Queue -- so the loop simply keeps going until a call
// returns nothing at all. The extra, confirming empty call this costs on a
// partial final batch is cheap; not knowing when to stop at all would not
// be.
//
// maxBatchesPerSweep bounds the loop so a sweep that is very badly behind
// cannot run forever on one tick: hitting it is logged at WARN, naming how
// much this sweep alone purged, so an operator can see retention is behind
// rather than have it look identical to a normal, caught-up sweep.
func (s *Sweeper) sweepOnce(ctx context.Context) {
	cutoff := s.clock.Now().Add(-s.cfg.MaxAge)

	var totalPurged, totalFilesRemoved, batches int
	for {
		if ctx.Err() != nil {
			break
		}
		batches++

		purged, err := s.queue.PurgeTerminalBefore(ctx, cutoff)
		if err != nil {
			s.logger.ErrorContext(ctx, "retention: purge terminal transcriptions failed", "error", err)
			break
		}
		if len(purged) == 0 {
			break
		}

		totalPurged += len(purged)
		for _, t := range purged {
			dir := transcriptionDir(t.ID)
			if err := s.store.RemoveDir(ctx, dir); err != nil {
				s.logger.ErrorContext(ctx, "retention: remove transcription directory failed; its database row is already gone",
					"transcription_id", t.ID, "dir", dir, "error", err)
				continue
			}
			totalFilesRemoved++
		}

		if batches >= maxBatchesPerSweep {
			s.logger.WarnContext(ctx, "retention: hit the per-sweep batch ceiling with more terminal transcriptions likely still due for purge; retention is falling behind",
				"batches", batches, "rows_purged_this_sweep", totalPurged)
			break
		}
	}

	if totalPurged == 0 {
		return
	}
	s.logger.InfoContext(ctx, "retention: purged old transcriptions",
		"rows_purged", totalPurged, "directories_removed", totalFilesRemoved, "batches", batches)
}
