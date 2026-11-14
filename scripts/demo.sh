#!/usr/bin/env bash
# One-command demo: starts the API and a worker, submits programs from
# testdata/sum, waits for the verdicts, prints them, then stops everything.
#
# Run it in Ubuntu (not Windows PowerShell), from the project folder:
#   bash scripts/demo.sh                    # every program in testdata/sum
#   bash scripts/demo.sh ac.cpp mle.java    # just these files
#   bash scripts/demo.sh fork net           # no extension means .py
#   WORKERS=3 bash scripts/demo.sh          # judge with 3 workers at once
#
# The language comes from the extension: .py, .cpp or .java.
# Worker logs (including compiler errors and stderr of crashing programs)
# end up in /tmp/judge-worker-1.log, -2.log, ...
set -euo pipefail
cd "$(dirname "$0")/.."

if [ -z "${DATABASE_URL:-}" ]; then
  set -a; source .env.example; set +a
fi
WORKERS=${WORKERS:-1}
PORT=${PORT:-8080}
API=http://localhost:$PORT

if curl -s -o /dev/null "$API/submissions/x"; then
  echo "Something is already running on port $PORT. Stop it (Ctrl+C in its terminal) and try again." >&2
  exit 1
fi

FILES=()
if [ $# -gt 0 ]; then
  for f in "$@"; do
    [[ $f == *.* ]] || f="$f.py"
    FILES+=("$f")
  done
else
  for f in testdata/sum/*.py testdata/sum/*.cpp testdata/sum/*.java; do
    FILES+=("$(basename "$f")")
  done
fi

echo "Building and starting the API and $WORKERS worker(s)..."
go build -o /tmp/judge-api ./cmd/api
go build -o /tmp/judge-worker ./cmd/worker

PIDS=()
stop_all() {
  # Stop everything the same way Ctrl+C would, then wait for it to finish.
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
for _ in $(seq 1 50); do curl -s -o /dev/null "$API/submissions/x" && break; sleep 0.1; done

PROBLEM=$(curl -sS -X POST "$API/problems" -H 'Content-Type: application/json' \
  --data-binary @testdata/sum/problem.json |
  python3 -c 'import json,sys; print(json.load(sys.stdin)["problem_id"])')

IDS=() # IDS[i] is the submission id for FILES[i]
for file in "${FILES[@]}"; do
  case "${file##*.}" in
    py) language=python ;;
    cpp) language=cpp ;;
    java) language=java ;;
    *) echo "Don't know the language of $file (use .py, .cpp or .java)" >&2; exit 1 ;;
  esac
  IDS+=("$(JUDGE_URL=$API bash scripts/submit.sh "$PROBLEM" "$language" "testdata/sum/$file" |
    python3 -c 'import json,sys; print(json.load(sys.stdin)["submission_id"])')")
done
echo "Submitted ${#FILES[@]} programs. Waiting for verdicts..."

printf '\n%-16s %-8s %s\n' PROGRAM VERDICT RUNTIME
for i in "${!FILES[@]}"; do
  file=${FILES[$i]}
  for _ in $(seq 1 120); do # give up after 60 s
    resp=$(curl -s "$API/submissions/${IDS[$i]}")
    status=$(python3 -c 'import json,sys; print(json.loads(sys.argv[1])["status"])' "$resp")
    [ "$status" = done ] && break
    sleep 0.5
  done
  if [ "$status" = done ]; then
    python3 -c '
import json, sys
r = json.loads(sys.argv[2])
tests = r["tests"]
if r["verdict"] == "AC":
    where = "all %d tests passed" % len(tests)
elif tests:
    where = "on test %d" % tests[-1]["test"]  # judging stops at the first failure
else:
    where = "no test ran"
print("%-16s %-8s %5s ms   %s" % (sys.argv[1], r["verdict"], r["runtime_ms"], where))
' "$file" "$resp"
  else
    printf '%-16s still %s after 60 s\n' "$file" "$status"
  fi
done
echo
echo "Worker logs: /tmp/judge-worker-*.log"
