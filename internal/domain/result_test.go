package domain

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestResultJSONRoundTrip covers the shape persisted as the `result` JSONB
// column: every field round-trips, and Segment.Words is omitted entirely
// (not emitted as `"words":null`) when word timestamps were not requested.
func TestResultJSONRoundTrip(t *testing.T) {
	original := Result{
		Text:            "Hello there. General Kenobi.",
		Language:        "en",
		DurationSeconds: 4.2,
		Model:           "base",
		Task:            TaskTranscribe,
		Segments: []Segment{
			{ID: 0, Start: 0, End: 2.1, Text: "Hello there.", Words: []Word{
				{Word: "Hello", Start: 0, End: 0.5, Probability: 0.97},
				{Word: "there.", Start: 0.5, End: 2.1, Probability: 0.91},
			}},
			{ID: 1, Start: 2.1, End: 4.2, Text: "General Kenobi."},
		},
	}

	data, err := json.Marshal(original)
	require.NoError(t, err)

	assert.Contains(t, string(data), `"words"`, "segment with words must include the key")

	var decoded Result
	require.NoError(t, json.Unmarshal(data, &decoded))
	assert.Equal(t, original, decoded)
}

func TestSegmentWordsOmitEmpty(t *testing.T) {
	seg := Segment{ID: 0, Start: 0, End: 1, Text: "hello there"}

	data, err := json.Marshal(seg)
	require.NoError(t, err)

	assert.NotContains(t, string(data), "words", "empty Words must be omitted, not null")
}

func TestResultJSONFieldNames(t *testing.T) {
	// Pins the exact wire field names the contract fixes, since a typo here
	// would silently break the `result` JSONB round trip and any client
	// that has already coded against them.
	r := Result{DurationSeconds: 1, Segments: []Segment{}}
	data, err := json.Marshal(r)
	require.NoError(t, err)

	var m map[string]any
	require.NoError(t, json.Unmarshal(data, &m))
	for _, key := range []string{"text", "language", "durationSeconds", "model", "task", "segments"} {
		assert.Contains(t, m, key)
	}
}

// TestTranscriptionJSONHidesInternalFields exercises the aggregate's own
// json tags: SourcePath, NextAttemptAt, LeaseOwner and LeaseExpiresAt are
// queue/storage implementation details (`json:"-"`) that must never reach
// an API response, while everything else round-trips.
func TestTranscriptionJSONHidesInternalFields(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	owner := "worker-1"
	tr := Transcription{
		ID:             "8f14e45f-ceea-467e-b0e9-5cd2e3e9e5a2",
		Status:         StatusRunning,
		Options:        DefaultOptions(),
		Audio:          Audio{Filename: "source.mp3", MediaType: "audio/mpeg", SizeBytes: 1024},
		SourcePath:     "transcriptions/8f14e45f/source.mp3",
		Attempts:       1,
		MaxAttempts:    3,
		CreatedAt:      now,
		UpdatedAt:      now,
		StartedAt:      &now,
		NextAttemptAt:  &now,
		LeaseOwner:     &owner,
		LeaseExpiresAt: &now,
	}

	data, err := json.Marshal(tr)
	require.NoError(t, err)

	var m map[string]any
	require.NoError(t, json.Unmarshal(data, &m))
	for _, hidden := range []string{"sourcePath", "SourcePath", "nextAttemptAt", "NextAttemptAt", "leaseOwner", "LeaseOwner", "leaseExpiresAt", "LeaseExpiresAt"} {
		assert.NotContains(t, m, hidden)
	}
	for _, visible := range []string{"id", "status", "options", "audio", "attempts", "maxAttempts", "createdAt", "updatedAt", "startedAt"} {
		assert.Contains(t, m, visible)
	}
	// Result, Error and CompletedAt are all nil/zero here and omitempty.
	assert.NotContains(t, m, "result")
	assert.NotContains(t, m, "error")
	assert.NotContains(t, m, "completedAt")
}
