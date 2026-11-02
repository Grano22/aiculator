#!/bin/sh
# Downloads the pinned formal tools into .tools (not committed; development only,
# never linked or shipped). Fails if a download does not match its recorded hash.
set -eu
cd "$(dirname "$0")/.."
TLA_URL=https://github.com/tlaplus/tlaplus/releases/download/v1.7.4/tla2tools.jar
ALLOY_URL=https://github.com/AlloyTools/org.alloytools.alloy/releases/download/v6.2.0/org.alloytools.alloy.dist.jar
TLA_SHA=936a262061c914694dfd669a543be24573c45d5aa0ff20a8b96b23d01e050e88
ALLOY_SHA=6b8c1cb5bc93bedfc7c61435c4e1ab6e688a242dc702a394628d9a9801edb78d
fetch() { # dir file url sha
  mkdir -p "$1"
  [ -f "$1/$2" ] || curl -fsSL -o "$1/$2" "$3"
  got=$(shasum -a 256 "$1/$2" | cut -d' ' -f1)
  if [ "$got" != "$4" ]; then echo "hash mismatch for $2: $got" >&2; rm -f "$1/$2"; exit 1; fi
}
fetch .tools/tla tla2tools.jar "$TLA_URL" "$TLA_SHA"
fetch .tools/alloy alloy.jar "$ALLOY_URL" "$ALLOY_SHA"
