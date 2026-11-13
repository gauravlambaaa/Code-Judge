-- 003_leases.sql: Step 5, recovering jobs from dead workers.
-- Apply with:  psql "$DATABASE_URL" -f migrations/003_leases.sql

BEGIN;

-- A job that has been claimed too many times without ever finishing (it
-- keeps crashing workers, say) ends as 'failed' instead of looping forever.
ALTER TABLE submissions DROP CONSTRAINT submissions_status_check;
ALTER TABLE submissions ADD CONSTRAINT submissions_status_check
    CHECK (status IN ('pending', 'running', 'done', 'failed'));

-- Jobs left 'running' before leases existed (like the one from the Ctrl+C
-- experiment in Step 1) have no lease at all. Treat it as already expired,
-- so a worker picks them up again.
UPDATE submissions
SET lease_expires_at = now()
WHERE status = 'running' AND lease_expires_at IS NULL;

-- The claim query only ever looks at unfinished jobs. A partial index holds
-- only those rows, so it stays tiny even with millions of finished ones.
CREATE INDEX submissions_unfinished_idx ON submissions (created_at)
    WHERE status IN ('pending', 'running');

COMMIT;
