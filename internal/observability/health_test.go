package observability

import (
	"context"
	"errors"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeChecker is a test Checker with a scriptable behaviour: succeed, fail
// with an error, sleep for a while (to exercise timeouts and concurrency),
// or panic.
type fakeChecker struct {
	name  string
	sleep time.Duration
	err   error
	panic bool
}

func (f *fakeChecker) Name() string { return f.name }

func (f *fakeChecker) Check(ctx context.Context) error {
	if f.panic {
		panic("fakeChecker: simulated panic")
	}
	if f.sleep > 0 {
		select {
		case <-time.After(f.sleep):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return f.err
}

// slowIgnoresContext never respects ctx, to prove Registry's own timeout
// bounds the wall-clock time even against a checker that would otherwise
// hang forever.
type slowIgnoresContext struct {
	name  string
	sleep time.Duration
}

func (s *slowIgnoresContext) Name() string { return s.name }

func (s *slowIgnoresContext) Check(ctx context.Context) error {
	time.Sleep(s.sleep)
	return nil
}

func TestRegistry_AllHealthy(t *testing.T) {
	r := NewRegistry()
	require.NoError(t, r.Register(&fakeChecker{name: "a"}))
	require.NoError(t, r.Register(&fakeChecker{name: "b"}))

	report, err := r.Check(context.Background())
	require.NoError(t, err)
	assert.True(t, report.OK)
	require.Len(t, report.Checks, 2)
	assert.True(t, report.Checks["a"].OK)
	assert.True(t, report.Checks["b"].OK)
}

func TestRegistry_OneFailingCheckerFailsOverallOK(t *testing.T) {
	r := NewRegistry()
	require.NoError(t, r.Register(&fakeChecker{name: "postgres"}))
	require.NoError(t, r.Register(&fakeChecker{name: "storage", err: errors.New("disk full")}))

	report, err := r.Check(context.Background())
	require.NoError(t, err)
	assert.False(t, report.OK)
	assert.True(t, report.Checks["postgres"].OK)
	assert.False(t, report.Checks["storage"].OK)
	assert.Equal(t, "disk full", report.Checks["storage"].Detail)
}

// TestRegistry_RunsConcurrently is the load-bearing timing test: three
// 100ms checks must finish in well under 300ms (i.e. concurrently, not
// serially).
func TestRegistry_RunsConcurrently(t *testing.T) {
	r := NewRegistry()
	require.NoError(t, r.Register(&fakeChecker{name: "a", sleep: 100 * time.Millisecond}))
	require.NoError(t, r.Register(&fakeChecker{name: "b", sleep: 100 * time.Millisecond}))
	require.NoError(t, r.Register(&fakeChecker{name: "c", sleep: 100 * time.Millisecond}))

	start := time.Now()
	report, err := r.Check(context.Background())
	elapsed := time.Since(start)

	require.NoError(t, err)
	assert.True(t, report.OK)
	assert.Less(t, elapsed, 250*time.Millisecond, "checks should have run concurrently, not serially (would be ~300ms+)")
}

// TestRegistry_TimesOutHangingChecker proves a checker that ignores ctx and
// hangs is still bounded by Registry.Timeout, and is reported as a failed
// (not OK) check rather than hanging the whole Check call.
func TestRegistry_TimesOutHangingChecker(t *testing.T) {
	r := &Registry{Timeout: 50 * time.Millisecond}
	require.NoError(t, r.Register(&slowIgnoresContext{name: "wedged", sleep: 5 * time.Second}))
	require.NoError(t, r.Register(&fakeChecker{name: "fine"}))

	start := time.Now()
	report, err := r.Check(context.Background())
	elapsed := time.Since(start)

	require.NoError(t, err)
	assert.Less(t, elapsed, 1*time.Second, "must not wait out the hanging checker's full sleep")
	assert.False(t, report.OK)
	assert.False(t, report.Checks["wedged"].OK)
	assert.NotEmpty(t, report.Checks["wedged"].Detail)
	assert.True(t, report.Checks["fine"].OK)
}

// TestRegistry_SurvivesPanickingChecker proves a checker that panics is
// recovered into a failed CheckResult, not a crashed test process.
func TestRegistry_SurvivesPanickingChecker(t *testing.T) {
	r := NewRegistry()
	require.NoError(t, r.Register(&fakeChecker{name: "panics", panic: true}))
	require.NoError(t, r.Register(&fakeChecker{name: "fine"}))

	report, err := r.Check(context.Background())
	require.NoError(t, err)
	assert.False(t, report.OK)
	assert.False(t, report.Checks["panics"].OK)
	assert.Contains(t, report.Checks["panics"].Detail, "panic")
	assert.True(t, report.Checks["fine"].OK)
}

func TestRegistry_DefaultTimeoutUsedWhenZero(t *testing.T) {
	r := &Registry{}
	assert.Equal(t, defaultCheckTimeout, r.timeout())

	r.Timeout = 10 * time.Second
	assert.Equal(t, 10*time.Second, r.timeout())
}

func TestRegistry_NoCheckers(t *testing.T) {
	r := NewRegistry()
	report, err := r.Check(context.Background())
	require.NoError(t, err)
	assert.True(t, report.OK)
	assert.Empty(t, report.Checks)
}

// TestRegistry_Register_RejectsDuplicateName is the regression test for the
// exact collapse a shared-name registration would otherwise cause:
// registering a passing "database" and a failing "database" must not
// silently produce a report with only one of them, with no way for an
// operator to tell which survived.
func TestRegistry_Register_RejectsDuplicateName(t *testing.T) {
	r := NewRegistry()
	require.NoError(t, r.Register(&fakeChecker{name: "database"}))

	err := r.Register(&fakeChecker{name: "database", err: errors.New("disk full")})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "database")

	// The first registration is unaffected: exactly one "database" check
	// runs, and it is the passing one.
	report, err := r.Check(context.Background())
	require.NoError(t, err)
	require.Len(t, report.Checks, 1)
	assert.True(t, report.Checks["database"].OK)
}

// TestRegistry_HangingCheckerLeaksBoundedGoroutines is the regression test
// for a permanently leaked goroutine: thousands of Check calls against a
// checker that ignores its context and never returns (the shape a storage
// check might take against a wedged volume) must not grow NumGoroutine
// anywhere near linearly with the number of calls. Before single-flight,
// this would leak one goroutine per Check call (thousands); with it, the
// checker's own goroutine is started once and reused by every subsequent
// call.
func TestRegistry_HangingCheckerLeaksBoundedGoroutines(t *testing.T) {
	r := &Registry{Timeout: 10 * time.Millisecond}
	require.NoError(t, r.Register(&slowIgnoresContext{name: "wedged", sleep: 1 * time.Hour}))

	// Let the very first attempt's timeout fire once, so every subsequent
	// call in the loop below hits the "already have a last result" path
	// rather than the "wait out my own per-call timeout" path — otherwise
	// the loop itself would take call-count * Timeout to run.
	_, err := r.Check(context.Background())
	require.NoError(t, err)
	time.Sleep(20 * time.Millisecond)

	runtime.GC()
	before := runtime.NumGoroutine()

	const calls = 3000
	for i := 0; i < calls; i++ {
		_, err := r.Check(context.Background())
		require.NoError(t, err)
	}

	// Give the runtime a moment to settle any transient goroutines (e.g.
	// the timer callback) before sampling.
	time.Sleep(100 * time.Millisecond)
	runtime.GC()
	after := runtime.NumGoroutine()

	assert.Less(t, after-before, 10,
		"goroutine count grew by %d over %d calls against a permanently hanging checker; want a small, call-count-independent constant", after-before, calls)
}
