#!/bin/bash
# Measures coverage the way the code really runs: unit tests, plus the binary the end-to-end
# tests start, built to record what it reaches, including the server it launches. Then each
# package is held to its minimum in scripts/coverage-min.txt.
#
# usage: scripts/coverage.sh [OUT_DIR]     writes coverage.out, coverage.html and summary.txt
set -euo pipefail
ROOT=$(cd "$(dirname "$0")/.." && pwd)
OUT=${1:-$ROOT/coverage}
mkdir -p "$OUT"
OUT=$(cd "$OUT" && pwd)
RAW=$(mktemp -d)
trap 'rm -rf "$RAW"' EXIT
mkdir -p "$RAW/unit" "$RAW/bin"
cd "$ROOT"
# -cover with a coverage directory records unit tests in the same raw form as the binary.
AICULATOR_COVERDIR="$RAW/bin" go test -count=1 -timeout 20m -cover -coverpkg=./... ./... \
  -args -test.gocoverdir="$RAW/unit" > "$OUT/test-output.txt" 2>&1 || { tail -40 "$OUT/test-output.txt"; exit 1; }
mkdir -p "$RAW/merged"
go tool covdata merge -i="$RAW/unit,$RAW/bin" -o "$RAW/merged"
echo "raw coverage files: $(ls "$RAW/unit" | wc -l | tr -d ' ') from tests, $(ls "$RAW/bin" | wc -l | tr -d ' ') from the binaries they ran"
go tool covdata textfmt -i="$RAW/merged" -o "$OUT/coverage.out"
go tool cover -html="$OUT/coverage.out" -o "$OUT/coverage.html"
go tool covdata percent -i="$RAW/merged" | sed 's#github.com/Grano22/aiculator/##' | awk '{printf "%-28s %s\n", $1, $3}' | sort > "$OUT/summary.txt"
fail=0
while read -r pkg want; do
  case "$pkg" in ''|'#'*) continue ;; esac
  got=$(awk -v p="$pkg" '$1==p{gsub("%","",$2); print $2}' "$OUT/summary.txt")
  if [ -z "$got" ]; then echo "MISSING  $pkg has no coverage data"; fail=1; continue; fi
  if awk -v g="$got" -v w="$want" 'BEGIN{exit !(g+0 < w+0)}'; then echo "BELOW    $pkg $got% < $want%"; fail=1; fi
done < "$ROOT/scripts/coverage-min.txt"
total=$(go tool cover -func="$OUT/coverage.out" | awk '/^total:/{print $NF}')
echo "total $total, report in $OUT/coverage.html"
exit $fail
