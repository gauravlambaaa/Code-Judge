#!/usr/bin/env bash
# Step 5 experiment: kill a worker in the middle of a job with kill -9 (no
# warning and no cleanup, like a crash or a power cut), then start another
# worker and watch it pick the job up once the dead worker's lease runs out.
#
# Run it in Ubuntu, from the project folder:
#   bash scripts/crash.sh                    # 10 s lease, so you wait less
#   LEASE_SECONDS=30 bash scripts/crash.sh   # the real setting
set -euo pipefail
cd "$(dirname "$0")/.."

if [ -z "${DATABASE_URL:-}" ]; then
  set -a; source .env.example; set +a
fi
export LEASE_SECONDS=${LEASE_SECONDS:-10}
PORT=${PORT:-8080}
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

# A problem with a 10 s time limit, so slow.py (which takes 5 s) is a correct answer.
PROBLEM=$(curl -sS -X POST "$API/problems" -H 'Content-Type: application/json' \
  -d '{"title": "Sum (slow is fine)", "time_limit_ms": 10000, "test_cases": [{"input": "2 3\n", "expected_output": "5\n"}]}' |
  python3 -c 'import json,sys; print(json.load(sys.stdin)["problem_id"])')
ID=$(JUDGE_URL=$API bash scripts/submit.sh "$PROBLEM" python testdata/sum/slow.py |
  python3 -c 'import json,sys; print(json.load(sys.stdin)["submission_id"])')
echo "Submitted slow.py (sleeps 5 s, then answers correctly): ${ID:0:8}"

status_line() {
  sql "SELECT to_char(now(), 'HH24:MI:SS') || '  status=' || status
        || '  worker=' || coalesce(worker_id, '-')
        || '  attempts=' || retry_count
        || '  lease_left=' || coalesce(greatest(0, ceil(extract(epoch FROM lease_expires_at - now())))::int || 's', '-')
        || '  verdict=' || coalesce(verdict, '-')
       FROM submissions WHERE id = '$ID'"
}

/tmp/judge-worker > /tmp/judge-worker-A.log 2>&1 &
A=$!
PIDS+=($A)
echo "Started worker A (pid $A), lease ${LEASE_SECONDS}s"
for _ in $(seq 1 30); do
  [ "$(sql "SELECT status FROM submissions WHERE id = '$ID'")" = running ] && break
  sleep 0.5
done
status_line
sleep 2 # let it get properly into the job

kill -9 "$A"
echo
echo ">>> $(date +%T)  kill -9 worker A, in the middle of the job <<<"
echo
status_line

/tmp/judge-worker > /tmp/judge-worker-B.log 2>&1 &
PIDS+=($!)
echo "Started worker B. It can't take the job until A's lease runs out..."
start=$(date +%s)
while [ "$(sql "SELECT status FROM submissions WHERE id = '$ID'")" != done ]; do
  status_line
  if [ $(( $(date +%s) - start )) -gt $(( LEASE_SECONDS + 60 )) ]; then echo "Gave up waiting" >&2; break; fi
  sleep 2
done
status_line

echo
echo "Who claimed it, and when:"
psql "$DATABASE_URL" -c "SELECT worker_id, to_char(claimed_at, 'HH24:MI:SS') AS claimed_at FROM claims WHERE submission_id = '$ID' ORDER BY claimed_at"
echo "Worker A's container was never removed (A died before it could). A worker"
echo "removes containers like that once they're 2 minutes old:"
docker ps -a --filter label=codejudge.sandbox --format '  {{.Names}}  {{.Status}}'
