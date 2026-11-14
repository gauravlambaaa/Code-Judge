-- 005_web.sql: what the web page needs.
-- Apply with:  psql "$DATABASE_URL" -f migrations/005_web.sql

BEGIN;

-- A problem statement to show above the editor.
ALTER TABLE problems ADD COLUMN statement TEXT NOT NULL DEFAULT '';

-- When the problem was added. The demo scripts add a fresh copy of the
-- sample problem every run; the page lists only the newest of each title.
ALTER TABLE problems ADD COLUMN created_at TIMESTAMPTZ NOT NULL DEFAULT now();

-- What to show next to a verdict: the compiler's errors for a CE, or the
-- failing test's stderr (a Python traceback, say). Cut to about 4 KB.
ALTER TABLE submissions ADD COLUMN message TEXT;

COMMIT;
