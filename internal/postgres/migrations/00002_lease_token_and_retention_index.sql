-- +goose Up
-- lease_token is the per-claim guard Store.Claim mints fresh on every
-- claim (see queue.go). lease_owner stays as an operator-facing "who holds
-- it" label; it must never again be used to guard a Heartbeat/Finish write,
-- because it is per-process, not per-claim, and a re-claim by the same
-- process after a lease expiry would otherwise satisfy its own stale
-- execution's guard.
ALTER TABLE transcriptions ADD COLUMN lease_token UUID;

-- internal/retention's sweep (Queue.PurgeTerminalBefore) filters on exactly
-- this predicate every Config.Interval; without a matching partial index it
-- is a sequential scan of the whole table.
CREATE INDEX transcriptions_retention_idx ON transcriptions (completed_at)
    WHERE status IN ('succeeded', 'failed', 'canceled');

-- +goose Down
DROP INDEX transcriptions_retention_idx;
ALTER TABLE transcriptions DROP COLUMN lease_token;
