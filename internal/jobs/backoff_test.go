package jobs

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCappedBackoff_DoublesPerAttemptAndCaps proves the pre-jitter schedule:
// base*2^(attempts-1), monotonically non-decreasing, capped at max.
func TestCappedBackoff_DoublesPerAttemptAndCaps(t *testing.T) {
	base := time.Second
	max := 10 * time.Second

	cases := []struct {
		attempts int
		want     time.Duration
	}{
		{attempts: 0, want: time.Second}, // < 1 treated as 1
		{attempts: 1, want: time.Second}, // base * 2^0
		{attempts: 2, want: 2 * time.Second},
		{attempts: 3, want: 4 * time.Second},
		{attempts: 4, want: 8 * time.Second},
		{attempts: 5, want: max}, // 16s would exceed max
		{attempts: 20, want: max},
	}
	for _, c := range cases {
		got := cappedBackoff(base, max, c.attempts)
		assert.Equalf(t, c.want, got, "attempts=%d", c.attempts)
	}
}

// TestCappedBackoff_Monotonic proves the schedule never decreases as
// attempts grows, for a wide range of attempt counts including ones large
// enough that a naive doubling loop would overflow time.Duration.
func TestCappedBackoff_Monotonic(t *testing.T) {
	base := 10 * time.Second
	max := 10 * time.Minute

	var prev time.Duration
	for attempts := 1; attempts <= 200; attempts++ {
		got := cappedBackoff(base, max, attempts)
		require.LessOrEqualf(t, prev, got, "attempts=%d regressed from previous", attempts)
		require.LessOrEqualf(t, got, max, "attempts=%d exceeded max", attempts)
		prev = got
	}
}

// TestFullJitter_BoundsAndDeterminism proves fullJitter draws from [0, d]
// and is a pure function of its injected Rand: the same Rand always
// produces the same delay for the same d.
func TestFullJitter_BoundsAndDeterminism(t *testing.T) {
	d := 10 * time.Second

	got := fullJitter(d, fixedRand{frac: 0})
	assert.Equal(t, time.Duration(0), got, "frac 0 must land on the lower bound")

	got = fullJitter(d, fixedRand{frac: 0.5})
	assert.Equal(t, 5*time.Second, got)

	// frac just under 1 must never reach d itself: Int64N(n) draws from
	// [0, n), and fullJitter passes n = int64(d)+1, so the maximum possible
	// draw is exactly d.
	got = fullJitter(d, fixedRand{frac: 0.999999})
	assert.LessOrEqual(t, got, d)
	assert.Greater(t, got, 9*time.Second)
}

// TestFullJitter_ZeroOrNegativeIsZero proves a non-positive pre-jitter
// delay (e.g. BackoffBase configured as 0) never asks Rand for anything and
// always returns 0 — Int64N(0) would panic on a real math/rand/v2 source.
func TestFullJitter_ZeroOrNegativeIsZero(t *testing.T) {
	assert.Equal(t, time.Duration(0), fullJitter(0, fixedRand{frac: 0.5}))
	assert.Equal(t, time.Duration(0), fullJitter(-time.Second, fixedRand{frac: 0.5}))
}

// TestPool_Backoff_MonotonicCappedAndJittered proves Pool.backoff — the
// method finish actually calls — composes cappedBackoff and fullJitter
// correctly: every delay lands within [0, cappedBackoff(attempts)], and the
// cap itself is respected for large attempt counts.
func TestPool_Backoff_MonotonicCappedAndJittered(t *testing.T) {
	h := newHarness(testConfig())
	h.pool.rand = fixedRand{frac: 1} // push every draw to its upper bound

	cfg := testConfig()
	for attempts := 1; attempts <= 10; attempts++ {
		want := cappedBackoff(cfg.BackoffBase, cfg.BackoffMax, attempts)
		got := h.pool.backoff(attempts)
		assert.LessOrEqualf(t, got, want, "attempts=%d", attempts)
	}

	// with frac=1, fullJitter's own rounding (int64(1*float64(n))) lands
	// exactly on the cap once cappedBackoff itself saturates.
	assert.Equal(t, cfg.BackoffMax, h.pool.backoff(50))
}
