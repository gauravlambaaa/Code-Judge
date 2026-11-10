-- 001_init.sql: the initial schema.
-- Apply with:  psql "$DATABASE_URL" -f migrations/001_init.sql
--
-- Postgres can run CREATE TABLE inside a transaction, so wrapping the whole
-- file in BEGIN/COMMIT means it either creates every table or none of them.

BEGIN;

CREATE TABLE problems (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    title           TEXT NOT NULL,
    time_limit_ms   INT  NOT NULL DEFAULT 2000,
    memory_limit_mb INT  NOT NULL DEFAULT 256
);

CREATE TABLE test_cases (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    problem_id      UUID NOT NULL REFERENCES problems(id) ON DELETE CASCADE,
    -- Position of the test within its problem: 1, 2, 3, ...
    -- Without it, "the first failing test case" (Step 6) would depend on
    -- whatever order Postgres happens to return rows in.
    ordinal         INT  NOT NULL,
    input           TEXT NOT NULL,
    expected_output TEXT NOT NULL,
    UNIQUE (problem_id, ordinal)
);

CREATE TABLE submissions (
    id               UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    problem_id       UUID NOT NULL REFERENCES problems(id),
    language         TEXT NOT NULL CHECK (language IN ('cpp', 'python', 'java')),
    source_code      TEXT NOT NULL,
    status           TEXT NOT NULL DEFAULT 'pending'
                     CHECK (status IN ('pending', 'running', 'done')),
    verdict          TEXT CHECK (verdict IN ('AC', 'WA', 'TLE', 'MLE', 'RE', 'CE')),
    runtime_ms       INT,
    worker_id        TEXT,
    -- lease_expires_at and retry_count stay unused until Step 5.
    lease_expires_at TIMESTAMPTZ,
    retry_count      INT NOT NULL DEFAULT 0,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- No index for the worker's "find the next job" query yet. We'll look at
-- what that query needs in Step 4.

COMMIT;
