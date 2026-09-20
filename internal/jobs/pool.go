// Package jobs implements the in-process worker pool that drives Helios'
// transcription pipeline: it claims pending rows from an app.Queue, probes
// and normalizes the uploaded audio, hands the result to an app.Transcriber,
// keeps the claimed row's lease alive with heartbeats for as long as that
// takes, and reports the outcome back to the queue.
//
// Retry policy — whether a failed attempt is retried or permanently failed,
// and the backoff before a retry becomes claimable again — is decided here
// and nowhere else; app.Queue only ever executes the transition this
// package computes (see finish). Lease liveness (heartbeats, expired-lease
// recovery) and graceful shutdown also live here. See internal/app/ports.go
// for the Queue, Store, AudioProcessor and Transcriber contracts this
// package is built on.
package jobs

import (
	"context"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pablofornell/helios-audio-transcription-service/internal/app"
	"github.com/pablofornell/helios-audio-transcription-service/internal/domain"
	"github.com/pablofornell/helios-audio-transcription-service/internal/observability"
)

// finishTimeout bounds how long the final Queue.Finish write (or the
// logging around an abandoned job) may take once a job's own context is no
// longer trustworthy — its JobTimeout has elapsed, or shutdown forced a
// cancel. Finish still needs a live, uncancelled context to reach the
// database; deriving one from context.Background() with this bound means a
// slow-but-alive database gets a fair chance to accept the write, while a
// truly wedged one cannot block Pool.Run or Pool.Shutdown forever.
const finishTimeout = 10 * time.Second

// cleanupTimeout bounds the best-effort removal of a job's normalized WAV.
// It is deliberately its own, always-fresh context (never the job's own,
// possibly already-canceled or expired, jobCtx) — see execute's use of it —
// so cleanup still runs on every exit path, including the ones where jobCtx
// is the very reason the job stopped.
const cleanupTimeout = 10 * time.Second

// staleClaimLoopMultiple bounds how many PollIntervals may pass without a
// claim loop iteration before Check reports the pool unhealthy. 3 gives a
// slow-but-alive loop (a temporarily overloaded database, say) room to miss
// a beat or two without flapping /readyz, while still catching a genuinely
// wedged loop well before an operator would otherwise notice.
const staleClaimLoopMultiple = 3

// Config is the pool's tuning knobs, sourced from config.WorkerConfig (see
// CONTRACT.md section 4's HELIOS_WORKER_* variables).
type Config struct {
	// Concurrency caps how many jobs this pool runs at once.
	Concurrency int
	// PollInterval is how often the claim loop asks Queue.Claim for more
	// work when it is not already at Concurrency.
	PollInterval time.Duration
	// LeaseDuration is how long a claimed job's lease lasts before it
	// becomes eligible for another worker to reclaim, absent a heartbeat.
	// It is also the period of the lease-sweep loop (see sweepLoop).
	LeaseDuration time.Duration
	// HeartbeatInterval is how often a running job extends its lease and
	// checks for a requested cancellation. Must be less than LeaseDuration.
	HeartbeatInterval time.Duration
	// JobTimeout bounds a single job's whole probe+normalize+transcribe
	// pipeline.
	JobTimeout time.Duration
	// MaxAttempts is the fallback attempt ceiling used only when a claimed
	// row's own MaxAttempts is not set; see finish's own doc for why the
	// row's value is authoritative.
	MaxAttempts int
	// BackoffBase and BackoffMax bound the exponential, full-jitter retry
	// delay computed by backoff.
	BackoffBase time.Duration
	BackoffMax  time.Duration
}

// Deps bundles every dependency Pool needs. There are no optional fields: a
// worker pool with a nil Queue, Store, Audio, Transcriber, Clock, Logger or
// Metrics is a construction bug, not a valid configuration, so NewPool does
// not attempt to default any of them.
type Deps struct {
	// Queue owns Transcription lifecycle transitions: Claim, Heartbeat,
	// Finish, ReleaseExpired. See app.Queue.
	Queue app.Queue
	// Store resolves store-relative paths to absolute ones for the
	// subprocesses this package drives, and removes the normalized WAV
	// once a job is done with it. See app.Store.
	Store app.Store
	// Audio probes and normalizes the uploaded source. See
	// app.AudioProcessor.
	Audio app.AudioProcessor
	// Transcriber turns a normalized WAV into a domain.Result. See
	// app.Transcriber.
	Transcriber app.Transcriber
	// Clock abstracts the current time used to compute a scheduled retry's
	// NextAttemptAt and job run durations, so tests can freeze it.
	// Production wiring passes app.SystemClock{}.
	Clock app.Clock
	// Logger receives diagnostic logging: heartbeat and finish conflicts
	// (info), claim/sweep failures (error), drain and abandonment (warn).
	Logger *slog.Logger
	// Metrics receives the typed observability calls this package makes:
	// JobsInFlightInc/Dec, TranscriptionCompleted, TranscriptionDuration,
	// TranscriptionAudioSeconds.
	Metrics *observability.Metrics
}

// jobHandle is what Pool tracks for one in-flight job: how to cancel its
// job context, whether that cancel came from Shutdown forcing a drain
// deadline rather than a heartbeat noticing a cancellation or a lost lease
// (see forced's use in runOne), and a channel closed once its goroutine has
// fully returned (including its Finish call or explicit abandonment).
type jobHandle struct {
	cancel context.CancelFunc
	forced atomic.Bool
	done   chan struct{}
}

// Pool is the in-process worker pool driving Helios' transcription
// pipeline. It claims pending rows from Deps.Queue, runs each one through
// probe, normalize and transcribe, and is the sole owner of retry policy
// and lease liveness.
type Pool struct {
	queue       app.Queue
	store       app.Store
	audio       app.AudioProcessor
	transcriber app.Transcriber
	clock       app.Clock
	logger      *slog.Logger
	metrics     *observability.Metrics
	cfg         Config
	owner       string

	// rand is the jitter source backoff draws from. It defaults to
	// defaultRand{} (math/rand/v2's package-level, concurrency-safe
	// generator); tests in this package assign a deterministic Rand
	// directly to this unexported field so a computed delay can be
	// asserted exactly instead of only bounded.
	rand Rand

	mu       sync.Mutex
	inFlight map[string]*jobHandle
	wg       sync.WaitGroup

	// stopping is set by Shutdown, under p.mu (see Shutdown), before it does
	// anything else. claimAndStart also checks it, unlocked, before even
	// attempting a Claim round trip -- a cheap, non-authoritative fast path
	// that just avoids wasted work once shutdown is underway. The guarantee
	// Shutdown actually depends on is enforced elsewhere: startJob re-checks
	// stopping and registers the job (p.inFlight and wg.Add(1)) in the SAME
	// p.mu critical section Shutdown uses to set this flag. That is what
	// closes the real race -- a Claim already in flight when Shutdown begins
	// can still return a job after claimAndStart's fast-path check has
	// passed, and wg.Add(1) only happens later, inside startJob -- so
	// without that shared critical section, Shutdown's wg.Wait() could
	// observe a zero count and return while such a job was only just
	// starting. See startJob for exactly what happens to a job claimed in
	// that window: it is left running with its lease untouched, for the
	// next lease-expiry sweep to recover, rather than started or reported
	// on.
	stopping atomic.Bool

	// lastClaimTick is the UnixNano of the claim loop's most recent
	// iteration (successful or not — see claimLoop), read by Check to
	// decide whether the loop is still alive.
	lastClaimTick atomic.Int64
}

// compile-time assertion that Pool satisfies the health-check shape /readyz
// aggregates (app.HealthChecker).
var _ app.HealthChecker = (*Pool)(nil)

// NewPool builds a Pool from cfg and deps. It does not start claiming until
// Run is called.
func NewPool(cfg Config, deps Deps) *Pool {
	p := &Pool{
		queue:       deps.Queue,
		store:       deps.Store,
		audio:       deps.Audio,
		transcriber: deps.Transcriber,
		clock:       deps.Clock,
		logger:      deps.Logger,
		metrics:     deps.Metrics,
		cfg:         cfg,
		owner:       newOwner(),
		rand:        defaultRand{},
		inFlight:    make(map[string]*jobHandle),
	}
	p.lastClaimTick.Store(deps.Clock.Now().UnixNano())
	return p
}

// newOwner builds a stable identity for this process's lease ownership:
// hostname, pid and a random suffix. The suffix is what keeps two processes
// that happen to restart with the same pid on the same host (a real
// occurrence in containers) from ever appearing to be the same lease owner.
func newOwner() string {
	host, err := os.Hostname()
	if err != nil || host == "" {
		host = "unknown-host"
	}
	return fmt.Sprintf("%s-%d-%08x", host, os.Getpid(), rand.Uint32())
}

// Run drives both of the pool's background loops — claiming and the
// lease-expiry sweep — until ctx is done, then returns nil. Run does not
// itself wait for in-flight jobs to finish: a job's context is deliberately
// not derived from ctx (see startJob), so canceling ctx stops new claims
// without touching work already underway. Callers that need to wait for
// that work to finish (or force it to stop) call Shutdown, typically after
// canceling the ctx passed here.
func (p *Pool) Run(ctx context.Context) error {
	sweepDone := make(chan struct{})
	go func() {
		defer close(sweepDone)
		p.sweepLoop(ctx)
	}()

	p.claimLoop(ctx)
	<-sweepDone
	return nil
}

// claimLoop claims eligible jobs immediately, then every Config.PollInterval,
// until ctx is done, at which point it returns without claiming again. It is
// the only place Queue.Claim is called from.
func (p *Pool) claimLoop(ctx context.Context) {
	ticker := time.NewTicker(p.cfg.PollInterval)
	defer ticker.Stop()

	if ctx.Err() == nil {
		p.claimAndStart(ctx)
	}

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if ctx.Err() != nil {
				return
			}
			p.claimAndStart(ctx)
		}
	}
}

// claimAndStart marks this iteration of the claim loop as alive (for
// Check), then claims one job at a time — Queue.Claim returns at most a
// single row per call, see app.Queue's doc — and starts it, until either
// free capacity runs out or Claim reports there is nothing left to do
// ((nil, nil), which is the normal idle case and is never logged as an
// error).
//
// A job successfully claimed after Shutdown has already set p.stopping is
// deliberately NOT started: startJob's own p.mu-guarded check is what
// actually enforces this (see the stopping field's doc); the loop condition
// below is only a cheap, best-effort early-out. A job startJob refuses this
// way is also not released or reported on here: this worker holds its lease
// until the lease expires and the next sweep (see sweepLoop) makes it
// claimable again, exactly as if this worker had crashed right after
// claiming it.
func (p *Pool) claimAndStart(ctx context.Context) {
	p.lastClaimTick.Store(p.clock.Now().UnixNano())

	for !p.stopping.Load() && p.freeSlots() > 0 {
		t, err := p.queue.Claim(ctx, p.owner, p.cfg.LeaseDuration)
		if err != nil {
			p.logger.ErrorContext(ctx, "jobs: claim failed", "error", err)
			return
		}
		if t == nil {
			return
		}
		if !p.startJob(t) {
			p.logger.WarnContext(ctx, "jobs: claimed a job after shutdown began; leaving it for lease expiry",
				"transcription_id", t.ID)
			return
		}
	}
}

// freeSlots reports how many more jobs this pool may claim right now:
// Config.Concurrency minus how many are currently in flight, never
// negative.
func (p *Pool) freeSlots() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	free := p.cfg.Concurrency - len(p.inFlight)
	if free < 0 {
		return 0
	}
	return free
}

// sweepLoop calls Queue.ReleaseExpired every Config.LeaseDuration until ctx
// is done. LeaseDuration is a sensible period: any lease abandoned by a
// dead worker becomes recoverable no more than one lease window late.
func (p *Pool) sweepLoop(ctx context.Context) {
	ticker := time.NewTicker(p.cfg.LeaseDuration)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			p.sweepOnce(ctx)
		}
	}
}

// sweepOnce runs one Queue.ReleaseExpired pass and logs how many rows it
// recovered.
func (p *Pool) sweepOnce(ctx context.Context) {
	n, err := p.queue.ReleaseExpired(ctx)
	if err != nil {
		p.logger.ErrorContext(ctx, "jobs: release expired failed", "error", err)
		return
	}
	if n > 0 {
		p.logger.InfoContext(ctx, "jobs: recovered expired leases", "count", n)
	}
}

// Shutdown drains every in-flight job rather than abandoning it mid-write:
// it stops claimAndStart from starting anything new, then waits for
// Config.JobTimeout has a chance to expire on its own — first passively,
// bounded by ctx, giving healthy jobs the chance to finish (or notice a
// heartbeat conflict) without any extra intervention. If ctx is done before
// that happens, Shutdown force-cancels every still-tracked job's context
// (see forceCancelAll) and waits once more, this time bounded by
// Config.JobTimeout, on the theory that a canceled context makes any
// well-behaved subprocess call (ffmpeg, whisper-cli, all invoked via
// exec.CommandContext by their respective packages) return promptly. An
// interrupted job is not simply abandoned at that point either: runOne
// attempts Queue.ReleaseForShutdown so its claim (and, critically, its
// attempt count) is given back rather than left for lease expiry to
// eventually fail outright (see runOne's doc). If jobs are still not done
// after the second wait, Shutdown logs loudly and returns ctx.Err() anyway
// — an unresponsive job's release attempt may not have landed either, in
// which case lease expiry remains the last-resort backstop.
//
// p.stopping is set under p.mu, not merely stored, because startJob checks
// it and registers the job (p.inFlight, wg.Add(1)) inside that very same
// critical section: that is what stops wg.Wait() below from ever observing
// a zero count while a job claimed just as Shutdown began is only just
// starting (see the stopping field's doc).
func (p *Pool) Shutdown(ctx context.Context) error {
	p.mu.Lock()
	p.stopping.Store(true)
	p.mu.Unlock()

	drained := make(chan struct{})
	go func() {
		p.wg.Wait()
		close(drained)
	}()

	select {
	case <-drained:
		return nil
	case <-ctx.Done():
	}

	p.logger.Warn("jobs: shutdown deadline exceeded before every in-flight job returned on its own; canceling them now")
	p.forceCancelAll()

	select {
	case <-drained:
		return nil
	case <-time.After(p.cfg.JobTimeout):
		p.logger.Error("jobs: in-flight jobs did not respond to cancellation; giving up waiting for them")
		return ctx.Err()
	}
}

// forceCancelAll marks and cancels the job context of every currently
// tracked in-flight job, without waiting for any of them to return. Marking
// forced before canceling is what lets runOne tell a shutdown-forced cancel
// apart from a heartbeat-driven one (see jobHandle.forced and finish's own
// doc): a job canceled this way must never be recorded as failed.
func (p *Pool) forceCancelAll() {
	p.mu.Lock()
	handles := make([]*jobHandle, 0, len(p.inFlight))
	for _, h := range p.inFlight {
		handles = append(handles, h)
	}
	p.mu.Unlock()

	for _, h := range handles {
		h.forced.Store(true)
		h.cancel()
	}
}

// Name identifies this checker in a /readyz report.
func (p *Pool) Name() string { return "worker_pool" }

// Check reports whether the claim loop is still alive: unhealthy when more
// than staleClaimLoopMultiple*Config.PollInterval has passed since its last
// iteration, which is what a wedged pool (e.g. a claim loop goroutine that
// panicked without crashing the process, or a database call that never
// returns and never times out) looks like from the outside.
func (p *Pool) Check(ctx context.Context) error {
	last := time.Unix(0, p.lastClaimTick.Load())
	stale := time.Duration(staleClaimLoopMultiple) * p.cfg.PollInterval
	if age := p.clock.Now().Sub(last); age > stale {
		return domain.New(domain.ErrCodeInternal,
			fmt.Sprintf("jobs: claim loop has not run in over %s", stale)).
			WithDetails("last_seen", last).WithDetails("age", age.String())
	}
	return nil
}
