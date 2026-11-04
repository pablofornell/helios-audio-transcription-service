package postgres

import (
	"github.com/jackc/pgx/v5"

	"github.com/pablofornell/helios-audio-transcription-service/internal/domain"
)

// transcriptionColumns is the fixed column list (and order) every plain
// SELECT that scans a full transcriptions row uses, so scanTranscription's
// Scan calls always line up with the query. id is cast to text: pgx's
// binary uuid codec only scans into pgtype.UUID or [16]byte, never a plain
// string, but domain.Transcription.ID is a plain string (it is the
// caller-facing UUIDv4, see internal/domain/transcription.go) -- casting at
// the SQL level keeps every call site an ordinary Scan into *string instead
// of every caller converting a pgtype.UUID back to one by hand.
const transcriptionColumns = `
	id::text, status, options, audio, source_path, result, error,
	attempts, max_attempts, lease_owner, lease_token::text, lease_expires_at, next_attempt_at,
	created_at, updated_at, started_at, completed_at, priority_at`

// claimReturningColumns is transcriptionColumns qualified with the "t."
// alias Store.Claim's UPDATE ... FROM candidate ... RETURNING clause needs:
// both transcriptions and the candidate CTE have an "id" column in scope
// there, so an unqualified reference would be ambiguous. The column order
// matches transcriptionColumns exactly, so scanTranscription serves both.
const claimReturningColumns = `
	t.id::text, t.status, t.options, t.audio, t.source_path, t.result, t.error,
	t.attempts, t.max_attempts, t.lease_owner, t.lease_token::text, t.lease_expires_at, t.next_attempt_at,
	t.created_at, t.updated_at, t.started_at, t.completed_at, t.priority_at`

// scanTranscription scans one row shaped like transcriptionColumns (or
// claimReturningColumns -- the same columns in the same order) into a fresh
// domain.Transcription. result and error are nullable JSONB columns;
// scanning directly into &t.Result and &t.Error (both already pointer
// fields) lets pgx's JSON codec leave them nil on SQL NULL instead of this
// package having to special-case that itself.
func scanTranscription(row pgx.Row) (*domain.Transcription, error) {
	var t domain.Transcription
	err := row.Scan(
		&t.ID, &t.Status, &t.Options, &t.Audio, &t.SourcePath, &t.Result, &t.Error,
		&t.Attempts, &t.MaxAttempts, &t.LeaseOwner, &t.LeaseToken, &t.LeaseExpiresAt, &t.NextAttemptAt,
		&t.CreatedAt, &t.UpdatedAt, &t.StartedAt, &t.CompletedAt, &t.PriorityAt,
	)
	if err != nil {
		return nil, err
	}
	return &t, nil
}
