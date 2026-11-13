-- 002_claims.sql: a log of every time a worker claims a submission.
-- Apply with:  psql "$DATABASE_URL" -f migrations/002_claims.sql
--
-- Judging doesn't need this table; it's evidence. If two workers ever claim
-- the same submission at the same time, that submission shows up here twice:
--
--   SELECT submission_id, count(*) FROM claims
--   GROUP BY submission_id HAVING count(*) > 1;

BEGIN;

CREATE TABLE claims (
    id            BIGSERIAL PRIMARY KEY,
    submission_id UUID NOT NULL REFERENCES submissions(id),
    worker_id     TEXT NOT NULL,
    claimed_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX claims_submission_idx ON claims (submission_id);

COMMIT;
