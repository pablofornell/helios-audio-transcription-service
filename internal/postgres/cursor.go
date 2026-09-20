package postgres

import (
	"encoding/base64"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/pablofornell/helios-audio-transcription-service/internal/domain"
)

// encodeCursor builds the opaque pagination token Store.List hands back as
// Page.NextCursor: a base64url encoding of "<created_at RFC3339Nano>|<id>",
// the (created_at, id) pair the next page's keyset predicate resumes from.
// RFC3339Nano round-trips a timestamptz without losing precision, which
// matters because rows in tests routinely share created_at to the
// microsecond.
func encodeCursor(createdAt time.Time, id string) string {
	raw := createdAt.UTC().Format(time.RFC3339Nano) + "|" + id
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

// decodeCursor reverses encodeCursor, rejecting anything malformed with a
// *domain.Error carrying ErrCodeInvalidInput -- the cursor is opaque to
// callers, but this package must still not trust it. In particular the id
// half must parse as a UUID: List's keyset predicate casts it with
// `$2::uuid`, and a non-UUID string would fail there instead, at
// rows.Err() with SQLSTATE 22P02, which the mapErr default path turns into
// ErrCodeInternal -- a 500 for what is really a bad request. Rejecting it
// here, before any query runs, is what keeps that a 400.
func decodeCursor(cursor string) (time.Time, string, error) {
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return time.Time{}, "", domain.Wrap(domain.ErrCodeInvalidInput, "malformed cursor", err)
	}

	parts := strings.SplitN(string(raw), "|", 2)
	if len(parts) != 2 || parts[1] == "" {
		return time.Time{}, "", domain.New(domain.ErrCodeInvalidInput, "malformed cursor")
	}

	createdAt, err := time.Parse(time.RFC3339Nano, parts[0])
	if err != nil {
		return time.Time{}, "", domain.Wrap(domain.ErrCodeInvalidInput, "malformed cursor", err)
	}

	if _, err := uuid.Parse(parts[1]); err != nil {
		return time.Time{}, "", domain.Wrap(domain.ErrCodeInvalidInput, "malformed cursor", err)
	}

	return createdAt, parts[1], nil
}
