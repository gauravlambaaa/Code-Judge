-- 004_results.sql: Step 6, a verdict for every test case.
-- Apply with:  psql "$DATABASE_URL" -f migrations/004_results.sql
--
-- submissions.verdict stays the overall answer (AC, or the first failing
-- test's verdict). This table records each test that ran. Judging stops at
-- the first failure, so a submission that fails test 3 has rows 1, 2 and 3.
-- A compile error has no rows at all, because no test ever ran.

BEGIN;

CREATE TABLE submission_results (
    submission_id UUID NOT NULL REFERENCES submissions(id) ON DELETE CASCADE,
    test_number   INT  NOT NULL, -- matches test_cases.ordinal: 1, 2, 3, ...
    verdict       TEXT NOT NULL CHECK (verdict IN ('AC', 'WA', 'TLE', 'MLE', 'RE')),
    runtime_ms    INT  NOT NULL,
    PRIMARY KEY (submission_id, test_number)
);

COMMIT;
