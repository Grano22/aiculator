#!/bin/sh
# Builds one release archive. The release workflow calls this once per platform, so
# what CI does can be run and tested on any machine.
# Usage: scripts/build_release.sh VERSION GOOS GOARCH [OUTDIR]
set -eu
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
  (cd "$STAGE" && zip -qr "$OUT/$NAME.zip" "$NAME")
  echo "$OUT/$NAME.zip"
else
  tar -C "$STAGE" -czf "$OUT/$NAME.tar.gz" "$NAME"
  echo "$OUT/$NAME.tar.gz"
fi
