#!/usr/bin/env bash
# Step 4 experiment: start the API and several workers, submit a batch of
# jobs at once, wait for them all, then check the claims table for any job
# that was claimed more than once.
#
# Run it in Ubuntu, from the project folder:
#   bash scripts/race.sh                       # 3 workers, 24 jobs
#   CLAIM_DELAY_MS=200 bash scripts/race.sh    # widen the race window
#   WORKERS=5 JOBS=40 bash scripts/race.sh
#
# Worker logs end up in /tmp/judge-worker-1.log, -2.log, ...
set -euo pipefail
cd "$(dirname "$0")/.."

if [ -z "${DATABASE_URL:-}" ]; then
  set -a; source .env.example; set +a
fi
WORKERS=${WORKERS:-3}
JOBS=${JOBS:-24}
PORT=${PORT:-8080}
export CLAIM_DELAY_MS=${CLAIM_DELAY_MS:-0}
API=http://localhost:$PORT
sql() { psql "$DATABASE_URL" -qAtc "$1"; }

if curl -s -o /dev/null "$API/submissions/x"; then
  echo "Something is already running on port $PORT. Stop it (Ctrl+C in its terminal) and try again." >&2
  exit 1
fi

echo "Building..."
go build -o /tmp/judge-api ./cmd/api
go build -o /tmp/judge-worker ./cmd/worker

PIDS=()
stop_all() {
  for pid in "${PIDS[@]}"; do kill -INT "$pid" 2>/dev/null || true; done
  for pid in "${PIDS[@]}"; do wait "$pid" 2>/dev/null || true; done
}
trap stop_all EXIT

ADDR=":$PORT" /tmp/judge-api > /tmp/judge-api.log 2>&1 &
PIDS+=($!)
for _ in $(seq 1 50); do curl -s -o /dev/null "$API/submissions/x" && break; sleep 0.1; done

PROBLEM=$(curl -sS -X POST "$API/problems" -H 'Content-Type: application/json' \
  --data-binary @testdata/sum/problem.json |
  python3 -c 'import json,sys; print(json.load(sys.stdin)["problem_id"])')

# Submit everything before any worker starts, so all the workers wake up to
# a full queue at the same moment: the worst case for a race.
echo "Submitting $JOBS jobs..."
for _ in $(seq 1 "$JOBS"); do
  JUDGE_URL=$API bash scripts/submit.sh "$PROBLEM" python testdata/sum/ac.py > /dev/null
done

echo "Starting $WORKERS workers (CLAIM_DELAY_MS=$CLAIM_DELAY_MS)..."
for i in $(seq 1 "$WORKERS"); do
  /tmp/judge-worker > "/tmp/judge-worker-$i.log" 2>&1 &
  PIDS+=($!)
done

start=$(date +%s)
while [ "$(sql "SELECT count(*) FROM submissions WHERE problem_id = '$PROBLEM' AND status <> 'done'")" != 0 ]; do
  if [ $(( $(date +%s) - start )) -gt 180 ]; then echo "Gave up waiting after 180 s" >&2; break; fi
  sleep 1
done
echo "All judged in $(( $(date +%s) - start )) s."

echo
echo "Claims per worker:"
psql "$DATABASE_URL" -c "
  SELECT c.worker_id, count(*) AS claims
  FROM claims c JOIN submissions s ON s.id = c.submission_id
  WHERE s.problem_id = '$PROBLEM'
  GROUP BY c.worker_id ORDER BY c.worker_id"

echo "Submissions claimed more than once (should be none):"
psql "$DATABASE_URL" -c "
  SELECT left(c.submission_id::text, 8) AS submission, count(*) AS times_claimed,
         string_agg(c.worker_id, ', ' ORDER BY c.claimed_at) AS by_workers
  FROM claims c JOIN submissions s ON s.id = c.submission_id
  WHERE s.problem_id = '$PROBLEM'
  GROUP BY c.submission_id HAVING count(*) > 1"

total=$(sql "SELECT count(*) FROM claims c JOIN submissions s ON s.id = c.submission_id WHERE s.problem_id = '$PROBLEM'")
dupes=$(sql "SELECT count(*) FROM (SELECT 1 FROM claims c JOIN submissions s ON s.id = c.submission_id WHERE s.problem_id = '$PROBLEM' GROUP BY c.submission_id HAVING count(*) > 1) d")
echo "SUMMARY: $JOBS jobs, $total claims, $dupes jobs claimed more than once."
