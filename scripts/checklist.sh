#!/usr/bin/env bash
# Runs the spec's "done" checklist end to end and prints PASS or FAIL for
# each item. Takes about 2 minutes.
#
# Run it in Ubuntu, from the project folder:
#   bash scripts/checklist.sh
set -uo pipefail
cd "$(dirname "$0")/.."

results=()
check() { # check "<description>" <yes|anything else>
  if [ "$2" = yes ]; then results+=("PASS  $1"); else results+=("FAIL  $1"); fi
}

echo "=== 1 of 3: one program per verdict (scripts/demo.sh) ==="
demo=$(bash scripts/demo.sh ac.py ac.java wa.py tle.py mle.py ce.cpp ce.py wa_overflow.cpp 2>&1)
echo "$demo"
line_of() { echo "$demo" | awk -v f="$1" '$1 == f'; }
verdict_of() { line_of "$1" | awk '{print $2}'; }
is() { [ "$(verdict_of "$1")" = "$2" ] && echo yes; }
check "Valid code -> AC (Python and Java)" "$([ "$(is ac.py AC)$(is ac.java AC)" = yesyes ] && echo yes)"
check "Wrong output -> WA" "$(is wa.py WA)"
check "Infinite loop -> TLE" "$(is tle.py TLE)"
check "Memory bomb -> MLE" "$(is mle.py MLE)"
check "Syntax error -> CE (C++ and Python)" "$([ "$(is ce.cpp CE)$(is ce.py CE)" = yesyes ] && echo yes)"
check "All test cases judged; first failure decides (WA on test 3)" \
  "$(line_of wa_overflow.cpp | grep -q 'WA .*on test 3' && echo yes)"

echo
echo "=== 2 of 3: 24 jobs, 3 workers (scripts/race.sh) ==="
race=$(bash scripts/race.sh 2>&1)
echo "$race" | grep -E 'Starting|All judged|SUMMARY'
check "20+ jobs across 3+ workers, each processed exactly once" \
  "$(echo "$race" | grep -q 'SUMMARY: 24 jobs, 24 claims, 0 jobs claimed more than once' && echo yes)"

echo
echo "=== 3 of 3: kill a worker mid-job (scripts/crash.sh) ==="
crash=$(bash scripts/crash.sh 2>&1)
echo "$crash" | grep -E 'kill -9|status=done'
check "Killed worker's job picked up and finished by another worker" \
  "$(echo "$crash" | grep -q 'status=done .*attempts=2 .*verdict=AC' && echo yes)"

echo
echo "=== Checklist ==="
printf '%s\n' "${results[@]}"
! printf '%s\n' "${results[@]}" | grep -q '^FAIL'
