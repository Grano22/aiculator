#!/bin/sh
# Prints one version's section from CHANGELOG.md, for the release notes. It fails if
# the section is missing or empty, so a release cannot ship without notes.
# Usage: scripts/changelog_notes.sh v1.2.3 [CHANGELOG]
set -eu
VERSION=${1:?version}
FILE=${2:-$(dirname "$0")/../CHANGELOG.md}
PLAIN=${VERSION#v}
NOTES=$(awk -v v="$PLAIN" '
  /^## \[/ { if (found) exit; if (index($0, "## [" v "]") == 1) { found = 1; next } }
  found { print }
' "$FILE" | sed -e '/./,$!d')
if [ -z "$NOTES" ]; then
  echo "CHANGELOG has no notes for $PLAIN. Add a ## [$PLAIN] section." >&2
  exit 1
fi
printf '%s\n' "$NOTES"
