package jobs

import (
	"math/rand/v2"
	"time"
)

// Rand is the source of randomness backoff draws jitter from. Production
// code uses defaultRand (math/rand/v2's top-level, concurrency-safe
// generator); tests assign a deterministic Rand directly to Pool's
// unexported rand field so a computed delay can be asserted exactly instead
// of only bounded.
type Rand interface {
	// Int64N returns a pseudo-random number in [0, n). n is always > 0.
	Int64N(n int64) int64
}

// defaultRand is the production Rand, delegating to math/rand/v2's
// package-level generator.
type defaultRand struct{}

func (defaultRand) Int64N(n int64) int64 { return rand.Int64N(n) }

// backoff returns the retry delay to use after t.Attempts failed attempts
// (see finish), drawn from cappedBackoff's exponential schedule with full
// jitter applied.
func (p *Pool) backoff(attempts int) time.Duration {
	return fullJitter(cappedBackoff(p.cfg.BackoffBase, p.cfg.BackoffMax, attempts), p.rand)
}

// cappedBackoff computes the pre-jitter retry delay for the attempts-th
// attempt (attempts is t.Attempts, i.e. 1 for the first failure): base *
// 2^(attempts-1), doubling once per attempt, capped at max. attempts < 1 is
// treated as 1, and the exponent loop bails out the moment it would exceed
// max, so a pathologically large MaxAttempts can never wrap a
// time.Duration around to something negative or tiny.
func cappedBackoff(base, max time.Duration, attempts int) time.Duration {
	if attempts < 1 {
		attempts = 1
	}
	if base <= 0 {
		return 0
	}

	d := base
	for i := 0; i < attempts-1; i++ {
		d *= 2
		if d <= 0 || d > max {
			return max
		}
	}
	if d > max {
		return max
	}
	return d
}

// fullJitter draws a delay uniformly from [0, d] via r. Full jitter —
// rather than a narrower +/-25% window centred on d — is the deliberate
// choice here: under a correlated failure (every in-flight job's ffmpeg or
// whisper-cli hitting the same transient condition within the same second)
// it spreads the resulting retries far more evenly across the retry window
// than a window centred on the capped value would, trading away a tighter
// minimum retry delay for a much lower chance that every retry lands in the
// same narrow band and reproduces the thundering herd it was meant to
// avoid. MaxAttempts still bounds how many times that trade can be made for
// a single job.
func fullJitter(d time.Duration, r Rand) time.Duration {
	if d <= 0 {
		return 0
	}
	return time.Duration(r.Int64N(int64(d) + 1))
}
