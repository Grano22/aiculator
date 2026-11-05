#!/bin/sh
# Runs every formal check. Models named *Bug* must FAIL, which proves the checker
# can catch that defect; everything else must pass. Any other result fails this script.
set -eu
ROOT=$(cd "$(dirname "$0")/.." && pwd)
"$ROOT/scripts/formal_tools.sh"
OUT=$(mktemp -d)
trap 'rm -rf "$OUT"' EXIT
TLA="$ROOT/.tools/tla/tla2tools.jar"
fail=0

tlc() { # module config expect
  log="$OUT/$2.log"
  ( cd "$ROOT/spec/tla" && java -XX:+UseParallelGC -cp "$TLA" tlc2.TLC -workers auto \
      -metadir "$OUT/meta-$2" -config "$2.cfg" "$1.tla" > "$log" 2>&1 ) || true
  rm -f "$ROOT"/spec/tla/*_TTrace_*
  if grep -q 'Model checking completed. No error has been found' "$log"; then got=pass
  elif grep -qE 'Error: (Invariant|Action property|Temporal properties).* violated|Error: Temporal properties were violated' "$log"; then got=violation
  else got=broken; fi
  states=$(grep -E 'distinct states found' "$log" | tail -1 | sed -E 's/.* ([0-9]+) distinct states found.*/\1/')
  why=$(grep -E 'violated' "$log" | head -1 | sed 's/^Error: //')
  printf '%-24s %-10s %12s  %s\n' "$2" "$got" "${states:-?}" "$why"
  if [ "$got" != "$3" ]; then echo "  UNEXPECTED: wanted $3; see output below"; tail -20 "$log"; fail=1; fi
}

echo "--- TLA+ (config, result, distinct states)"
tlc Swarm Swarm pass
tlc Swarm SwarmOneTaskDeep pass
tlc SwarmLiveness SwarmLiveness pass
tlc Planner Planner pass
tlc Sync Sync pass
tlc Election Election pass
tlc Swarm SwarmBug_nofence violation
tlc Swarm SwarmBug_selfverify violation
tlc Planner PlannerBug_nohints violation
tlc Sync SyncBug_noecho violation
tlc Election ElectionBug_health violation

echo "--- Alloy"
java --enable-native-access=ALL-UNNAMED -jar "$ROOT/.tools/alloy/alloy.jar" exec -f -c '*' -o "$OUT/alloy" "$ROOT/spec/alloy/swarm.als" > "$OUT/alloy.log" 2>&1 || true
alloy=$(grep -E '^[0-9]+\. (check|run) ' "$OUT/alloy.log" || true)
[ -n "$alloy" ] || { echo "alloy produced no results"; cat "$OUT/alloy.log"; fail=1; }
printf '%s\n' "$alloy"
bad=$(printf '%s\n' "$alloy" | awk '
  $2=="check" && $3 ~ /^Bug/ { if ($NF!="SAT") print }
  $2=="check" && $3 !~ /^Bug/ { if ($NF!="UNSAT") print }
  $2=="run" { if ($NF!="SAT") print }')
if [ -n "$bad" ]; then echo "UNEXPECTED alloy results:"; printf '%s\n' "$bad"; fail=1; fi
printf 'alloy: %s commands\n' "$(printf '%s\n' "$alloy" | wc -l | tr -d ' ')"

echo "--- Spec matches code"
( cd "$ROOT" && go test -count=1 ./internal/core -run TestSpecMatchesCode -v 2>&1 | grep -E 'transitions agree|^(ok|FAIL|---)' ) || fail=1

[ "$fail" = 0 ] && echo "FORMAL CHECKS PASSED" || { echo "FORMAL CHECKS FAILED"; exit 1; }
