-- 006_runs.sql: "Run with custom input" (the page's Run button).
-- Apply with:  psql "$DATABASE_URL" -f migrations/006_runs.sql
--
-- A run is a row in the same submissions table, so it travels through the
-- same queue, the same workers and the same sandbox as a normal submission.
-- The only differences: the worker runs the code once on the user's own
-- input instead of on the test cases, and saves what it printed.

BEGIN;

-- 'judge': run against the problem's test cases and give a verdict.
-- 'run':   run once on custom_input and save the output.
ALTER TABLE submissions ADD COLUMN mode TEXT NOT NULL DEFAULT 'judge'
    CHECK (mode IN ('judge', 'run'));

ALTER TABLE submissions ADD COLUMN custom_input TEXT; -- only for runs
ALTER TABLE submissions ADD COLUMN stdout TEXT;       -- only for runs (cut to 64 KB)

-- A run that finishes normally gets 'OK': it ran without crashing. There's
-- no expected output to compare with, so it can't be AC or WA.
ALTER TABLE submissions DROP CONSTRAINT submissions_verdict_check;
ALTER TABLE submissions ADD CONSTRAINT submissions_verdict_check
    CHECK (verdict IN ('AC', 'WA', 'TLE', 'MLE', 'RE', 'CE', 'OK'));

COMMIT;
