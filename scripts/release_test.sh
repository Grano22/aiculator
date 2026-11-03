#!/bin/sh
# Tests release.sh, changelog_notes.sh and docs_deploy.sh in a throwaway repository.
set -eu
ROOT=$(cd "$(dirname "$0")/.." && pwd)
D=$(mktemp -d); trap 'rm -rf "$D"' EXIT
cd "$D"
git init -q -b master .
git config user.name t; git config user.email t@t
mkdir -p scripts
cp "$ROOT/scripts/release.sh" scripts/
printf '# Changelog\n\n## [Unreleased]\n\n### Added\n\n- First thing.\n' > CHANGELOG.md
git add -A; git commit -qm init
R="scripts/release.sh --skip-tests"
fail() { echo "FAIL: $*" >&2; exit 1; }
rejects() { if "$@" >/dev/null 2>&1; then fail "should have refused: $*"; fi; }
tags() { git tag --list | tr '\n' ' '; }

# Dry run changes nothing.
$R --bump minor --dry-run | grep -q 'version: v0.1.0' || fail "dry run version"
[ -z "$(tags)" ] || fail "dry run made a tag"
[ -z "$(git status --porcelain)" ] || fail "dry run changed files"

# Bad input is refused.
rejects $R --version banana
rejects $R --version 1.2
rejects $R --version 01.2.3
rejects $R --bump huge
rejects $R --bump minor --version 1.0.0
rejects $R --nonsense

# A real bump moves the notes and tags.
$R --bump minor >/dev/null
git rev-parse -q --verify refs/tags/v0.1.0 >/dev/null || fail "no tag"
grep -q '^## \[0.1.0\] - ' CHANGELOG.md || fail "no section"
"$ROOT/scripts/changelog_notes.sh" v0.1.0 CHANGELOG.md | grep -q 'First thing' || fail "notes"
if "$ROOT/scripts/changelog_notes.sh" v9.9.9 CHANGELOG.md >/dev/null 2>&1; then fail "notes for a missing version"; fi

# An empty Unreleased section stops the next release.
rejects $R --bump patch
# So do uncommitted changes, even when there are notes.
printf '\n### Fixed\n\n- A fix.\n' > extra
awk '/^## \[Unreleased\]/{print;print "";print "- A fix.";next}{print}' CHANGELOG.md > c && mv c CHANGELOG.md
rm extra
rejects $R --bump patch
git add -A; git commit -qm change

# The same version cannot be released twice.
rejects $R --version 0.1.0

# Off master needs an explicit flag.
git switch -qc other
rejects $R --bump patch --dry-run
$R --bump patch --dry-run --allow-branch >/dev/null
git switch -q master

# Prerelease, then the next bump ignores it and counts from the last stable version.
$R --bump patch --pre rc.1 >/dev/null
git rev-parse -q --verify refs/tags/v0.1.1-rc.1 >/dev/null || fail "no prerelease tag"
awk '/^## \[Unreleased\]/{print;print "";print "- More.";next}{print}' CHANGELOG.md > c && mv c CHANGELOG.md
git add -A; git commit -qm more
$R --bump major --dry-run | grep -q 'version: v1.0.0' || fail "major from stable"
$R --bump minor --dry-run | grep -q 'version: v0.2.0' || fail "minor from stable"
$R --version v3.4.5 --dry-run | grep -q 'version: v3.4.5' || fail "leading v"

# Docs versions.
dd() { env "$@" "$ROOT/scripts/docs_deploy.sh" --dry-run; }
dd EVENT=push REF_TYPE=tag REF_NAME=v1.2.3 | grep -q 'mike deploy --push --update-aliases 1.2 latest' || fail "stable docs"
dd EVENT=push REF_TYPE=tag REF_NAME=v1.2.3-rc.1 | grep -q 'mike deploy --push 1.2.3-rc.1$' || fail "prerelease docs"
if dd EVENT=push REF_TYPE=tag REF_NAME=v1.2.3-rc.1 | grep -q latest; then fail "a prerelease moved latest"; fi
dd EVENT=push REF_TYPE=branch REF_NAME=master | grep -q 'mike deploy --push dev$' || fail "master docs"
dd EVENT=workflow_dispatch INPUT_VERSION=2.0 INPUT_ALIAS=stable | grep -q '2.0 stable' || fail "manual docs"
echo "release tests passed"
