-- +goose Up
CREATE TABLE transcriptions (
    id              UUID PRIMARY KEY,
    status          TEXT        NOT NULL,
    options         JSONB       NOT NULL,
    audio           JSONB       NOT NULL,
    source_path     TEXT        NOT NULL,
    result          JSONB,
    error           JSONB,
    attempts        INTEGER     NOT NULL DEFAULT 0,
    max_attempts    INTEGER     NOT NULL,
    lease_owner     TEXT,
    lease_expires_at TIMESTAMPTZ,
    -- NOT NULL: Claim's predicate is `next_attempt_at <= now()`, and
    -- `NULL <= now()` is NULL, not true -- a nullable column here would
    -- leave every freshly submitted transcription pending forever, silently.
    next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- Set by Queue.RequestCancel; read back by Queue.Heartbeat so a worker
    -- already running a job discovers a cancellation without polling for it.
    cancel_requested BOOLEAN    NOT NULL DEFAULT FALSE,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    started_at      TIMESTAMPTZ,
    completed_at    TIMESTAMPTZ
);

-- Queue.Claim's own index: `SELECT ... WHERE status = 'pending' AND
-- next_attempt_at <= now() ORDER BY next_attempt_at, created_at FOR UPDATE
-- SKIP LOCKED`. Partial so it stays small regardless of how many terminal
-- rows accumulate.
CREATE INDEX transcriptions_claim_idx ON transcriptions (next_attempt_at)
    WHERE status = 'pending';

-- Queue.ReleaseExpired scans running rows with a lapsed lease, on every
-- worker, every poll interval. Without this it is a sequential scan of the
-- whole table forever.
CREATE INDEX transcriptions_lease_idx ON transcriptions (lease_expires_at)
    WHERE status = 'running';

-- Repository.List, most-recently-created first. The id tiebreak is what
-- makes keyset pagination correct: rows can share created_at to the
-- microsecond, and a cursor on the timestamp alone would skip or repeat
-- them at a page boundary.
CREATE INDEX transcriptions_list_idx ON transcriptions (created_at DESC, id DESC);

-- +goose Down
DROP TABLE transcriptions;
