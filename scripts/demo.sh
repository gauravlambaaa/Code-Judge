#!/usr/bin/env bash
# One-command demo: starts the API, submits programs from testdata/sum,
# waits for the verdicts, prints them, then stops the API.
#
# Run it in Ubuntu (not Windows PowerShell), from the project folder:
#   bash scripts/demo.sh              # every program in testdata/sum
#   bash scripts/demo.sh fork net     # just these (file names without .py)
#
# The worker's log (including stderr of crashing programs) ends up in
# /tmp/judge-api.log.
set -euo pipefail
cd "$(dirname "$0")/.."

if [ -z "${DATABASE_URL:-}" ]; then
  set -a; source .env.example; set +a
fi
PORT=${PORT:-8080}
API=http://localhost:$PORT

if curl -s -o /dev/null "$API/submissions/x"; then
  echo "Something is already running on port $PORT. Stop it (Ctrl+C in its terminal) and try again." >&2
  exit 1
fi

echo "Building and starting the API on port $PORT..."
go build -o /tmp/judge-api ./cmd/api
ADDR=":$PORT" /tmp/judge-api > /tmp/judge-api.log 2>&1 &
SERVER=$!
# When this script exits (normally, on error, or on Ctrl+C), stop the API
# the same way Ctrl+C would.
trap 'kill -INT "$SERVER" 2>/dev/null || true; wait "$SERVER" 2>/dev/null || true' EXIT
for _ in $(seq 1 50); do curl -s -o /dev/null "$API/submissions/x" && break; sleep 0.1; done

PROBLEM=$(curl -sS -X POST "$API/problems" -H 'Content-Type: application/json' \
  --data-binary @testdata/sum/problem.json |
  python3 -c 'import json,sys; print(json.load(sys.stdin)["problem_id"])')

if [ $# -gt 0 ]; then
  NAMES=("$@")
else
  NAMES=()
  for f in testdata/sum/*.py; do NAMES+=("$(basename "$f" .py)"); done
fi

IDS=() # IDS[i] is the submission id for NAMES[i]
for name in "${NAMES[@]}"; do
  IDS+=("$(JUDGE_URL=$API bash scripts/submit.sh "$PROBLEM" python "testdata/sum/$name.py" |
    python3 -c 'import json,sys; print(json.load(sys.stdin)["submission_id"])')")
done
echo "Submitted ${#NAMES[@]} programs. Waiting for verdicts (one worker, so one at a time)..."

printf '\n%-10s %-8s %s\n' PROGRAM VERDICT RUNTIME
for i in "${!NAMES[@]}"; do
  name=${NAMES[$i]}
  for _ in $(seq 1 120); do # give up after 60 s
    resp=$(curl -s "$API/submissions/${IDS[$i]}")
    status=$(python3 -c 'import json,sys; print(json.loads(sys.argv[1])["status"])' "$resp")
    [ "$status" = done ] && break
    sleep 0.5
  done
  if [ "$status" = done ]; then
    python3 -c 'import json,sys; r=json.loads(sys.argv[2]); print("%-10s %-8s %s ms" % (sys.argv[1], r["verdict"], r["runtime_ms"]))' "$name" "$resp"
  else
    printf '%-10s still %s after 60 s\n' "$name" "$status"
  fi
done
echo
echo "Worker log: /tmp/judge-api.log"
