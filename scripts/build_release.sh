#!/bin/sh
# Builds one release archive. The release workflow calls this once per platform, so
# what CI does can be run and tested on any machine.
# Usage: scripts/build_release.sh VERSION GOOS GOARCH [OUTDIR]
#
# It enforces the size budget, so neither CI nor a release can ship past it. To raise a
# limit, change it here on purpose and say why in the commit.
set -eu
MAX_BINARY_MIB=${MAX_BINARY_MIB:-30}
MAX_ARCHIVE_MIB=${MAX_ARCHIVE_MIB:-11}
VERSION=${1:?version, such as v1.2.3}
GOOS=${2:?goos}
GOARCH=${3:?goarch}
OUT=${4:-dist}
cd "$(dirname "$0")/.."
case "$OUT" in /*) ;; *) OUT="$PWD/$OUT" ;; esac
EXT=""
[ "$GOOS" = windows ] && EXT=".exe"
NAME="aiculator_${VERSION}_${GOOS}_${GOARCH}"
STAGE=$(mktemp -d)
trap 'rm -rf "$STAGE"' EXIT
mkdir -p "$STAGE/$NAME" "$OUT"
CGO_ENABLED=0 GOOS="$GOOS" GOARCH="$GOARCH" go build -trimpath \
  -ldflags "-s -w -X main.version=$VERSION" -o "$STAGE/$NAME/aiculator$EXT" ./cmd/aiculator
# The dependency licenses require their texts to ship with the binary.
for f in README.md CHANGELOG.md THIRD_PARTY_NOTICES.md LICENSE; do
  if [ -f "$f" ]; then cp "$f" "$STAGE/$NAME/"; fi
done
if [ "$GOOS" = windows ]; then
  ARCHIVE="$OUT/$NAME.zip"
  (cd "$STAGE" && zip -qr "$ARCHIVE" "$NAME")
else
  ARCHIVE="$OUT/$NAME.tar.gz"
  tar -C "$STAGE" -czf "$ARCHIVE" "$NAME"
fi
over() { # file limit-in-MiB
  [ "$(wc -c < "$1" | tr -d ' ')" -gt "$(($2 * 1048576))" ]
}
mib() { awk -v b="$(wc -c < "$1" | tr -d ' ')" 'BEGIN { printf "%.1f", b / 1048576 }'; }
BINARY="$STAGE/$NAME/aiculator$EXT"
if over "$BINARY" "$MAX_BINARY_MIB"; then
  rm -f "$ARCHIVE"
  echo "size budget exceeded: $GOOS/$GOARCH binary is $(mib "$BINARY") MiB, limit $MAX_BINARY_MIB MiB" >&2
  exit 1
fi
if over "$ARCHIVE" "$MAX_ARCHIVE_MIB"; then
  SIZE=$(mib "$ARCHIVE")
  rm -f "$ARCHIVE"
  echo "size budget exceeded: $GOOS/$GOARCH archive is $SIZE MiB, limit $MAX_ARCHIVE_MIB MiB" >&2
  exit 1
fi
echo "$ARCHIVE binary=$(mib "$BINARY")MiB archive=$(mib "$ARCHIVE")MiB"
