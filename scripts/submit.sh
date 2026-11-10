#!/usr/bin/env bash
# Submit a source file to the judge and print the response.
#
# usage: bash scripts/submit.sh <problem_id> <language> <source_file>
#
# Source code has quotes, newlines and backslashes in it, which are painful to
# escape by hand inside a curl command, so Python's json module builds the
# request body.
set -euo pipefail

if [ $# -ne 3 ]; then
  echo "usage: $0 <problem_id> <language> <source_file>" >&2
  exit 1
fi

python3 -c '
import json, sys
problem_id, language, path = sys.argv[1:4]
with open(path) as f:
    print(json.dumps({"problem_id": problem_id, "language": language, "source_code": f.read()}))
' "$1" "$2" "$3" |
  curl -sS -X POST "${JUDGE_URL:-http://localhost:8080}/submissions" \
    -H 'Content-Type: application/json' --data-binary @-
echo
