package domain

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestStatusValid(t *testing.T) {
	for _, s := range []Status{StatusPending, StatusRunning, StatusSucceeded, StatusFailed, StatusCanceled} {
		assert.True(t, s.Valid(), "status %q must be valid", s)
	}
	for _, s := range []Status{"", "queued", "Pending", "RUNNING"} {
		assert.False(t, Status(s).Valid(), "status %q must not be valid", s)
	}
}

func TestStatusTerminal(t *testing.T) {
	cases := map[Status]bool{
		StatusPending:   false,
		StatusRunning:   false,
		StatusSucceeded: true,
		StatusFailed:    true,
		StatusCanceled:  true,
	}
	for s, want := range cases {
		assert.Equal(t, want, s.Terminal(), "status %q", s)
	}
}

// TestStatusCanTransitionTo pins the exact machine every caller
// (internal/postgres's Claim/Finish, internal/app's Cancel/Retry) relies
// on: every legal edge must be allowed, and every other combination —
// including every self-transition and every terminal-to-non-pending edge —
// must be rejected.
func TestStatusCanTransitionTo(t *testing.T) {
	allowed := []struct {
		from, to Status
	}{
		{StatusPending, StatusRunning},
		{StatusRunning, StatusSucceeded},
		{StatusRunning, StatusFailed},
		{StatusRunning, StatusCanceled},
		{StatusRunning, StatusPending},
		{StatusFailed, StatusPending},
		{StatusCanceled, StatusPending},
	}
	for _, tc := range allowed {
		assert.True(t, tc.from.CanTransitionTo(tc.to), "%s -> %s must be allowed", tc.from, tc.to)
	}

	forbidden := []struct {
		from, to Status
	}{
		// Self-transitions.
		{StatusPending, StatusPending},
		{StatusRunning, StatusRunning},
		{StatusSucceeded, StatusSucceeded},
		{StatusFailed, StatusFailed},
		{StatusCanceled, StatusCanceled},
		// Pending cannot skip Running.
		{StatusPending, StatusSucceeded},
		{StatusPending, StatusFailed},
		{StatusPending, StatusCanceled},
		// Succeeded is terminal for good: no manual retry, unlike Failed
		// and Canceled.
		{StatusSucceeded, StatusPending},
		{StatusSucceeded, StatusRunning},
		{StatusSucceeded, StatusFailed},
		// Failed/Canceled can only go back to Pending, never straight to
		// Running or to each other.
		{StatusFailed, StatusRunning},
		{StatusFailed, StatusCanceled},
		{StatusFailed, StatusSucceeded},
		{StatusCanceled, StatusRunning},
		{StatusCanceled, StatusFailed},
		{StatusCanceled, StatusSucceeded},
		// An unknown status never transitions anywhere.
		{Status("bogus"), StatusPending},
	}
	for _, tc := range forbidden {
		assert.False(t, tc.from.CanTransitionTo(tc.to), "%s -> %s must be forbidden", tc.from, tc.to)
	}
}
