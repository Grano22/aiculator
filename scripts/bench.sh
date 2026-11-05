#!/bin/bash
# Runs the benchmarks of HEAD and of a base commit on this machine, interleaved, so that
# the two see the same machine: CI runners differ in speed from one run to the next, and
# even within one, so numbers are only comparable when taken side by side.
#
# usage: scripts/bench.sh [BASE_REF] [OUT_DIR] [COUNT]
#   writes OUT_DIR/head.txt and, when BASE_REF is given and builds, OUT_DIR/base.txt
#
# Test binaries are built once per side and then run in turns, base then head, COUNT times.
# Benchmarks that only one side has are kept; the report marks them new or missing.
set -euo pipefail
ROOT=$(cd "$(dirname "$0")/.." && pwd)
BASE=${1:-}
OUT=${2:-$ROOT/bench}
COUNT=${3:-6}
PKGS=${BENCH_PKGS:-"internal/engine internal/api internal/auth internal/store internal/vlog"}
BENCHTIME=${BENCHTIME:-500ms}
mkdir -p "$OUT"
OUT=$(cd "$OUT" && pwd)
WORK=$(mktemp -d)
cleanup() {
  if [ -d "$WORK/base" ]; then git -C "$ROOT" worktree remove --force "$WORK/base" >/dev/null 2>&1 || true; fi
  rm -rf "$WORK"
}
trap cleanup EXIT

build() { # tree side
  mkdir -p "$WORK/bin/$2"
  for p in $PKGS; do
    if [ -d "$1/$p" ] && (cd "$1" && go test -c -o "$WORK/bin/$2/$(echo "$p" | tr / _).test" "./$p" >/dev/null 2>"$WORK/build-$2.log"); then
      :
    else
      echo "$2: $p has no benchmarks to build here; skipped"
    fi
  done
}

build "$ROOT" head
sides="head"
if [ -n "$BASE" ] && [ "$(git -C "$ROOT" rev-parse "$BASE" 2>/dev/null)" != "$(git -C "$ROOT" rev-parse HEAD)" ]; then
  if git -C "$ROOT" worktree add -q --detach "$WORK/base" "$BASE" 2>/dev/null; then
    build "$WORK/base" base
    sides="base head"
  else
    echo "base $BASE is not available; reporting head alone"
  fi
fi

: > "$OUT/head.txt"
[ "$sides" = "head" ] || : > "$OUT/base.txt"
for i in $(seq "$COUNT"); do
  for side in $sides; do
    tree=$ROOT
    [ "$side" = base ] && tree=$WORK/base
    for p in $PKGS; do
      bin="$WORK/bin/$side/$(echo "$p" | tr / _).test"
      [ -x "$bin" ] || continue
      # From the package directory, as go test would run it, so testdata paths resolve.
      (cd "$tree/$p" && "$bin" -test.run '^$' -test.bench . -test.benchmem -test.benchtime "$BENCHTIME" -test.count 1) >> "$OUT/$side.txt"
    done
  done
  echo "round $i of $COUNT done"
done
echo "results in $OUT"
