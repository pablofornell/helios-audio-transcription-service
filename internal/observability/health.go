package observability

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// defaultCheckTimeout is Registry.Timeout's default, used when a Registry
// is constructed via NewRegistry (or is used as a zero value) without an
// explicit Timeout.
const defaultCheckTimeout = 3 * time.Second

// Checker is the shape any subsystem (database, storage, transcriber, ...)
// must satisfy to be aggregated by Registry into a readiness report. It is
// declared locally, structurally, rather than imported from another
// internal package, so this leaf package stays importable by everything
// without pulling in app or domain.
type Checker interface {
	Name() string
	Check(ctx context.Context) error
}

// CheckResult is one checker's outcome as reported in a Report.
type CheckResult struct {
	OK     bool
	Detail string
}

// Report is the aggregate result of running every registered Checker.
type Report struct {
	OK     bool
	Checks map[string]CheckResult
}

// Registry aggregates every Checker Helios registers (database, storage,
// transcriber, ...) into a single Report for /readyz.
//
// Every checker runs concurrently, each bounded by Timeout, so one slow or
// wedged dependency can never hold up the whole readiness probe. A checker
// that panics is recovered into a failed CheckResult rather than crashing
// the process — a bad health check must never be worse than an honestly
// reported unhealthy one.
//
// Checker execution is single-flight per name: while a checker's Check is
// still outstanding, a new Registry.Check call joins that same execution
// (or, once it has already missed Timeout once, immediately gets the
// synthetic "timed out" result) instead of starting a second one. This
// matters because a checker that ignores its context and blocks in an
// uninterruptible syscall (e.g. a storage check against a wedged volume,
// exactly the failure the check exists to detect) cannot be reclaimed by Go
// once started; that goroutine is gone for the life of the process. Without
// single-flight, a readiness probe hitting such a checker every few seconds
// leaks one goroutine per probe forever. With it, a permanently wedged
// checker leaks exactly one goroutine, ever, no matter how many times Check
// is called: the single outstanding attempt is retained and every
// subsequent call reuses its (eventually synthetic-timeout) result instead
// of spawning another. If the checker later does return — it was merely
// slow, not truly wedged — Registry notices and starts a fresh attempt on
// the next Check call.
type Registry struct {
	// Timeout bounds a single checker's Check call. Zero means
	// defaultCheckTimeout.
	Timeout time.Duration

	mu       sync.Mutex
	checkers []Checker
	names    map[string]bool
	states   map[string]*checkState
}

// checkState is the single-flight state Registry keeps per checker name,
// across every Check call for the life of the Registry (not just one
// call): running, while non-nil, is the one outstanding attempt every
// concurrent or subsequent caller joins instead of duplicating; last is
// the most recent result available to hand back immediately to a caller
// that arrives after running's Timeout has already elapsed once.
type checkState struct {
	mu       sync.Mutex
	running  *inFlightCheck
	last     CheckResult
	haveLast bool
}

// inFlightCheck is one single-flight execution of a checker's Check,
// shared by every caller that joins it. done is closed at most once (via
// publish) with whichever comes first: the checker actually returning, or
// Registry.Timeout elapsing — in the latter case the checker's own
// goroutine is left running in the background (see Registry's doc comment)
// and done is still closed so joined callers don't wait past Timeout.
type inFlightCheck struct {
	done   chan struct{}
	once   sync.Once
	result CheckResult
}

// publish records result as in.result and closes in.done, if this is the
// first call to do so; a later call (whichever of the timeout or the
// checker actually returning loses the race) is a no-op.
func (in *inFlightCheck) publish(result CheckResult) {
	in.once.Do(func() {
		in.result = result
		close(in.done)
	})
}

// NewRegistry returns an empty Registry using defaultCheckTimeout.
func NewRegistry() *Registry {
	return &Registry{Timeout: defaultCheckTimeout}
}

// Register adds checker to r, or returns an error if a checker with the
// same Name is already registered. Two checkers sharing a name would
// otherwise collapse silently into a single map entry in the Report Check
// produces — whichever happens to write last wins, so an operator could
// see "not ready" with every listed check reported healthy, with no way to
// tell that a failing check's result was ever discarded. It is safe to call
// concurrently with Check, though in practice every checker is registered
// once at startup before traffic begins.
func (r *Registry) Register(checker Checker) error {
	name := checker.Name()

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.names == nil {
		r.names = make(map[string]bool)
	}
	if r.names[name] {
		return fmt.Errorf("observability: health checker %q is already registered", name)
	}
	r.names[name] = true
	r.checkers = append(r.checkers, checker)
	return nil
}

// timeout returns r.Timeout, or defaultCheckTimeout if it is unset.
func (r *Registry) timeout() time.Duration {
	if r.Timeout > 0 {
		return r.Timeout
	}
	return defaultCheckTimeout
}

// stateFor returns the checkState for name, creating it if this is the
// first time name has been checked.
func (r *Registry) stateFor(name string) *checkState {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.states == nil {
		r.states = make(map[string]*checkState)
	}
	st, ok := r.states[name]
	if !ok {
		st = &checkState{}
		r.states[name] = st
	}
	return st
}

// Check runs every registered checker concurrently, each given its own
// context bounded by r.timeout(), and aggregates the results into a
// Report. Report.OK is true only if every checker passed. The returned
// error is always nil: an individual checker's failure is reported as a
// failed CheckResult in the report, not as Check's error — callers inspect
// Report.OK to decide whether to fail readiness.
func (r *Registry) Check(ctx context.Context) (Report, error) {
	r.mu.Lock()
	checkers := make([]Checker, len(r.checkers))
	copy(checkers, r.checkers)
	r.mu.Unlock()

	results := make(chan struct {
		name   string
		result CheckResult
	}, len(checkers))

	var wg sync.WaitGroup
	for _, checker := range checkers {
		wg.Add(1)
		go func(checker Checker) {
			defer wg.Done()
			name := checker.Name()
			results <- struct {
				name   string
				result CheckResult
			}{name, r.runOne(ctx, checker)}
		}(checker)
	}
	wg.Wait()
	close(results)

	report := Report{
		OK:     true,
		Checks: make(map[string]CheckResult, len(checkers)),
	}
	for res := range results {
		report.Checks[res.name] = res.result
		if !res.result.OK {
			report.OK = false
		}
	}
	return report, nil
}

// runOne reports checker's current health, joining its single-flight
// checkState instead of always starting a new execution: if no attempt is
// currently outstanding, it starts one; either way, it waits up to
// r.timeout() (bounded further by ctx) for that attempt's result, falling
// back to the checker's last known result — or a synthetic "timed out" —
// if the attempt is still outstanding when this call's own wait expires.
func (r *Registry) runOne(ctx context.Context, checker Checker) CheckResult {
	st := r.stateFor(checker.Name())

	st.mu.Lock()
	run := st.running
	if run == nil {
		run = &inFlightCheck{done: make(chan struct{})}
		st.running = run
		st.mu.Unlock()
		go r.execute(checker, st, run)
	} else {
		st.mu.Unlock()
	}

	checkCtx, cancel := context.WithTimeout(ctx, r.timeout())
	defer cancel()

	select {
	case <-run.done:
		return run.result
	case <-checkCtx.Done():
		st.mu.Lock()
		last, haveLast := st.last, st.haveLast
		st.mu.Unlock()
		if haveLast {
			return last
		}
		return CheckResult{OK: false, Detail: fmt.Sprintf("timed out after %s", r.timeout())}
	}
}

// execute runs checker.Check exactly once for the single-flight attempt
// run, recovering a panic into a failed CheckResult. It gives the checker a
// context bounded by r.timeout(), so a context-aware checker always
// returns promptly and frees its goroutine — but it does not itself return
// until checker.Check does, however long that takes: if the checker
// ignores its context and blocks in an uninterruptible syscall, this
// goroutine (and the one Check spawned for it) is permanently leaked,
// which is a property of Go, not a bug in this function. What execute does
// about it is publish a synthetic timeout to run.done as soon as
// r.timeout() elapses (via the timer below), so no caller of Check ever
// waits past Timeout, and leave st.running set until this goroutine
// eventually completes (if ever), so no second attempt is ever started for
// the same name while this one remains outstanding.
func (r *Registry) execute(checker Checker, st *checkState, run *inFlightCheck) {
	timeout := r.timeout()
	timer := time.AfterFunc(timeout, func() {
		run.publish(CheckResult{OK: false, Detail: fmt.Sprintf("timed out after %s", timeout)})
	})
	defer timer.Stop()

	runCtx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	result := func() (result CheckResult) {
		defer func() {
			if p := recover(); p != nil {
				result = CheckResult{OK: false, Detail: fmt.Sprintf("panic: %v", p)}
			}
		}()
		if err := checker.Check(runCtx); err != nil {
			return CheckResult{OK: false, Detail: err.Error()}
		}
		return CheckResult{OK: true}
	}()

	run.publish(result) // no-op if the timer already published a timeout

	st.mu.Lock()
	st.last, st.haveLast = result, true
	st.running = nil
	st.mu.Unlock()
}
