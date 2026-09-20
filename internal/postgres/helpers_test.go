package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/pablofornell/helios-audio-transcription-service/internal/domain"
)

// truncateAll resets the table between tests so they can share one
// container without one test's rows leaking into the next.
func truncateAll(t *testing.T) {
	t.Helper()
	_, err := testPool.Exec(context.Background(), `TRUNCATE transcriptions`)
	require.NoError(t, err)
}

// newTranscription builds a minimal, valid pending Transcription ready for
// Store.Create, with maxAttempts defaulted to 3.
func newTranscription(id string, at time.Time) *domain.Transcription {
	return newTranscriptionWithMaxAttempts(id, at, 3)
}

func newTranscriptionWithMaxAttempts(id string, at time.Time, maxAttempts int) *domain.Transcription {
	at = at.Truncate(time.Microsecond)
	return &domain.Transcription{
		ID:      id,
		Options: domain.Options{}.Normalize(),
		Audio: domain.Audio{
			Filename:  "source.mp3",
			MediaType: "audio/mpeg",
			SizeBytes: 1024,
		},
		SourcePath:  "transcriptions/" + id + "/source.mp3",
		MaxAttempts: maxAttempts,
		CreatedAt:   at,
		UpdatedAt:   at,
	}
}

// newID mints a fresh UUIDv4 string, matching domain.Transcription.ID's
// documented shape.
func newID() string {
	return uuid.NewString()
}
