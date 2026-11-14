#!/usr/bin/env bash
# Starts everything the web page needs: the API, 2 workers, and the sample
# problems (testdata/*/problem.json) if they aren't there yet. Then open
# http://localhost:8080 in your browser. That works from Windows too,
# because WSL forwards localhost. Press Ctrl+C here to stop everything.
#
# Run it in Ubuntu, from the project folder:
#   bash scripts/web.sh
#   WORKERS=3 bash scripts/web.sh
set -euo pipefail
cd "$(dirname "$0")/.."

if [ -z "${DATABASE_URL:-}" ]; then
  set -a; source .env.example; set +a
fi
WORKERS=${WORKERS:-2}
PORT=${PORT:-8080}
API=http://localhost:$PORT

if curl -s -o /dev/null "$API/problems"; then
  echo "Something is already running on port $PORT. Stop it (Ctrl+C in its terminal) and try again." >&2
  exit 1
fi

echo "Building..."
go build -o /tmp/judge-api ./cmd/api
go build -o /tmp/judge-worker ./cmd/worker

PIDS=()
stop_all() {
  echo; echo "Stopping..."
  for pid in "${PIDS[@]}"; do kill -INT "$pid" 2>/dev/null || true; done
  for pid in "${PIDS[@]}"; do wait "$pid" 2>/dev/null || true; done
}
trap stop_all EXIT

ADDR=":$PORT" /tmp/judge-api > /tmp/judge-api.log 2>&1 &
PIDS+=($!)
for i in $(seq 1 "$WORKERS"); do
  /tmp/judge-worker > "/tmp/judge-worker-$i.log" 2>&1 &
  PIDS+=($!)
done
for _ in $(seq 1 50); do curl -s -o /dev/null "$API/problems" && break; sleep 0.1; done

# Add each sample problem unless the newest copy in the database already
# matches the file (same statement and number of tests). The page only lists
# the newest copy of each title, so an outdated copy is simply hidden.
curl -s "$API/problems" > /tmp/judge-problems.json
for f in testdata/*/problem.json; do
  if python3 - "$f" /tmp/judge-problems.json <<'EOF'
import json, sys
want = json.load(open(sys.argv[1]))
have = {p["title"]: p for p in json.load(open(sys.argv[2]))}
p = have.get(want["title"])
up_to_date = p and p["statement"] == want.get("statement", "") and p["test_count"] == len(want["test_cases"])
sys.exit(0 if up_to_date else 1)
EOF
  then continue; fi
  curl -sS -X POST "$API/problems" -H 'Content-Type: application/json' --data-binary @"$f" > /dev/null
  echo "Added sample problem: $(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["title"])' "$f")"
done

echo
echo "Ready: open http://localhost:$PORT in your browser."
echo "API log: /tmp/judge-api.log   Worker logs: /tmp/judge-worker-*.log"
echo "Press Ctrl+C here to stop."
wait "${PIDS[0]}"
