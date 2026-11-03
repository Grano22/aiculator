#!/bin/sh
# Creates a release: moves the Unreleased changelog notes under a new version, commits,
# and makes a v-prefixed semantic version tag. Pushing the tag starts the release and
# docs workflows. Nothing leaves this machine without --push.
set -eu
usage() {
  cat <<'USAGE'
Usage: scripts/release.sh (--bump LEVEL | --version X.Y.Z) [options]

  --bump major|minor|patch  Next version, from the latest v* tag.
  --version X.Y.Z           An exact version. A leading v is accepted.
  --pre LABEL               Prerelease suffix, such as rc.1, making vX.Y.Z-rc.1.
  --allow-branch            Allow releasing from a branch other than master.
  --skip-tests              Skip go test. CI still runs them.
  --push                    Push the commit and tag, which publishes the release.
  --dry-run                 Show what would happen and change nothing.
USAGE
}
BUMP="" VERSION="" PRE="" PUSH=0 DRY=0 ALLOW=0 SKIP=0
while [ $# -gt 0 ]; do
  case "$1" in
    --bump) BUMP=${2:?}; shift 2 ;;
    --version) VERSION=${2:?}; shift 2 ;;
    --pre) PRE=${2:?}; shift 2 ;;
    --allow-branch) ALLOW=1; shift ;;
    --skip-tests) SKIP=1; shift ;;
    --push) PUSH=1; shift ;;
    --dry-run) DRY=1; shift ;;
    -h|--help) usage; exit 0 ;;
    *) echo "unknown flag: $1" >&2; usage >&2; exit 2 ;;
  esac
done
die() { echo "release: $*" >&2; exit 1; }
cd "$(git rev-parse --show-toplevel)"

[ -n "$BUMP" ] && [ -n "$VERSION" ] && die "use --bump or --version, not both"
[ -z "$BUMP" ] && [ -z "$VERSION" ] && { usage >&2; exit 2; }

LATEST=$(git tag --list 'v[0-9]*' --sort=-v:refname | grep -vE -- '-' | head -1 || true)
if [ -n "$BUMP" ]; then
  BASE=${LATEST#v}; [ -n "$BASE" ] || BASE=0.0.0
  MAJOR=${BASE%%.*}; REST=${BASE#*.}; MINOR=${REST%%.*}; PATCH=${REST#*.}
  case "$BUMP" in
    major) VERSION="$((MAJOR+1)).0.0" ;;
    minor) VERSION="$MAJOR.$((MINOR+1)).0" ;;
    patch) VERSION="$MAJOR.$MINOR.$((PATCH+1))" ;;
    *) die "--bump must be major, minor or patch" ;;
  esac
fi
VERSION=${VERSION#v}
[ -n "$PRE" ] && VERSION="$VERSION-$PRE"
echo "$VERSION" | grep -Eq '^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z.-]+)?$' \
  || die "$VERSION is not a semantic version"
TAG="v$VERSION"

git rev-parse -q --verify "refs/tags/$TAG" >/dev/null && die "$TAG already exists"
BRANCH=$(git rev-parse --abbrev-ref HEAD)
[ "$BRANCH" = master ] || [ "$ALLOW" = 1 ] || die "on $BRANCH, not master; pass --allow-branch to override"
# Untracked files stay out: only CHANGELOG.md is committed, and CI builds from the tag.
[ -z "$(git status --porcelain --untracked-files=no)" ] || die "the working tree has uncommitted changes"
grep -q '^## \[Unreleased\]' CHANGELOG.md || die "CHANGELOG.md has no [Unreleased] section"
NOTES=$(awk '/^## \[/ { if (u) exit; if (index($0, "## [Unreleased]") == 1) { u = 1; next } } u { print }' CHANGELOG.md | sed -e '/./,$!d')
[ -n "$NOTES" ] || die "the [Unreleased] section is empty, so there is nothing to release"

echo "version: $TAG (previous: ${LATEST:-none})"
echo "notes:"; printf '%s\n' "$NOTES" | sed 's/^/  /'
if [ "$DRY" = 1 ]; then echo "dry run: changed nothing"; exit 0; fi

[ "$SKIP" = 1 ] || go test -count=1 -short ./...
TODAY=$(date -u +%Y-%m-%d)
TMP=$(mktemp)
awk -v v="$VERSION" -v d="$TODAY" '
  /^## \[Unreleased\]/ && !done { print; print ""; print "## [" v "] - " d; done = 1; next }
  { print }
' CHANGELOG.md > "$TMP"
mv "$TMP" CHANGELOG.md
git add CHANGELOG.md
git commit -q -m "chore(release): $TAG"
git tag -a "$TAG" -m "Release $TAG"
echo "created $TAG"
if [ "$PUSH" = 1 ]; then
  git push origin "$BRANCH" "$TAG"
  echo "pushed: the release and docs workflows will now run"
else
  echo "not pushed. To publish: git push origin $BRANCH $TAG"
  echo "To undo: git tag -d $TAG && git reset --hard HEAD~1"
fi
