package postgres

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/pablofornell/helios-audio-transcription-service/internal/domain"
)

// pgUniqueViolation is the SQLSTATE Postgres reports when an INSERT/UPDATE
// would duplicate a unique constraint (here, a re-INSERT of an id
// Repository.Create already used). See
// https://www.postgresql.org/docs/current/errcodes-appendix.html.
const pgUniqueViolation = "23505"

// pgInvalidTextRepresentation is the SQLSTATE Postgres reports when a value
// cannot be cast to its target type -- e.g. `'not-a-uuid'::uuid` in a
// keyset predicate built from a caller-supplied cursor. decodeCursor
// already validates the id half is a UUID before any query runs (see
// cursor.go), so this should be unreachable in practice; it is kept as a
// backstop so any other caller-controlled value that ends up cast in SQL
// fails as ErrCodeInvalidInput (400) rather than the ErrCodeInternal (500)
// default.
const pgInvalidTextRepresentation = "22P02"

// mapErr classifies a raw pgx/pgconn error into a *domain.Error, per the
// contract every Repository/Queue method promises: pgx.ErrNoRows becomes
// ErrCodeNotFound, a unique violation becomes ErrCodeConflict (the row it
// collides with is a conflicting piece of state, not a missing one), a
// caller-initiated context cancellation becomes ErrCodeCanceled, a context
// deadline becomes ErrCodeTimeout, and anything else is wrapped as
// ErrCodeInternal. It returns nil for a nil err, so it is safe to use as a
// one-line "if err != nil" replacement at the end of every query.
func mapErr(err error) error {
	if err == nil {
		return nil
	}

	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Wrap(domain.ErrCodeNotFound, "not found", err)
	}

	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case pgUniqueViolation:
			return domain.Wrap(domain.ErrCodeConflict, "already exists", err)
		case pgInvalidTextRepresentation:
			return domain.Wrap(domain.ErrCodeInvalidInput, "malformed input", err)
		}
	}

	// context.Canceled must be checked before context.DeadlineExceeded:
	// they are distinct sentinel values, and a caller-initiated
	// cancellation (graceful shutdown, an API-triggered Cancel unwinding a
	// Heartbeat/Finish call) is not the same failure as a deadline the
	// caller set and then exceeded. Mapping it to ErrCodeTimeout (or the
	// ErrCodeInternal default) would make domain.ErrorCode.Retryable treat
	// a routine shutdown as either an unclassified bug or worth retrying
	// with backoff, when it is neither: it was intentional.
	if errors.Is(err, context.Canceled) {
		return domain.Wrap(domain.ErrCodeCanceled, "database operation canceled", err)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return domain.Wrap(domain.ErrCodeTimeout, "database operation timed out", err)
	}

	return domain.Wrap(domain.ErrCodeInternal, "database error", err)
}
