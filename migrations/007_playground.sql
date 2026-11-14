-- 007_playground.sql: the page's Playground (run any code, no problem).
-- Apply with:  psql "$DATABASE_URL" -f migrations/007_playground.sql

BEGIN;

-- A playground run doesn't belong to any problem, so problem_id may be NULL
-- now, but only for runs: anything that gets judged still needs a problem
-- (that's where its test cases come from).
ALTER TABLE submissions ALTER COLUMN problem_id DROP NOT NULL;
ALTER TABLE submissions ADD CONSTRAINT judged_submissions_need_a_problem
    CHECK (mode = 'run' OR problem_id IS NOT NULL);

-- Problems the scripts create for their own testing shouldn't show up in
-- the page's problem list.
ALTER TABLE problems ADD COLUMN listed BOOLEAN NOT NULL DEFAULT true;
UPDATE problems SET listed = false WHERE title = 'Sum (slow is fine)';

COMMIT;
