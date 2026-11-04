-- +goose Up
-- priority_at marks a transcription the user asked to process first (see
-- Store.Prioritize). NULL means no priority. Claim takes rows with a value
-- first, newest first (priority_at DESC NULLS LAST), and then uses its
-- normal order: next_attempt_at, created_at, id.
--
-- Store.Prioritize sets it. Every terminal transition clears it: Finish
-- (succeeded, failed or canceled), RequestCancel and ReleaseExpired when no
-- attempt remains. An automatic retry keeps it, so a priority row keeps its
-- place when its own backoff ends. max_attempts bounds those retries, so a
-- priority row cannot hold the head of the queue for ever.
ALTER TABLE transcriptions ADD COLUMN priority_at TIMESTAMPTZ NULL;

-- Claim sorts by priority_at first. The older transcriptions_claim_idx
-- cannot give that order, so without this index every claim sorts all the
-- due pending rows. With it, LIMIT stops the scan at the first row that
-- passes the filters.
CREATE INDEX transcriptions_pending_priority_claim_idx
    ON transcriptions (priority_at DESC NULLS LAST, next_attempt_at, created_at, id)
    WHERE status = 'pending';

-- +goose Down
DROP INDEX transcriptions_pending_priority_claim_idx;
ALTER TABLE transcriptions DROP COLUMN priority_at;
