#!/bin/sh
# Picks the docs version from how the workflow was started, then publishes it with
# mike. Pass --dry-run to print the commands instead of running them.
set -eu
RUN=""
[ "${1:-}" = "--dry-run" ] && RUN="echo"
if [ "${EVENT:-}" = workflow_dispatch ]; then
  VERSION=$INPUT_VERSION
  ALIAS=${INPUT_ALIAS:-}
elif [ "${REF_TYPE:-}" = tag ]; then
  PLAIN=${REF_NAME#v}
  case "$PLAIN" in
    *-*) VERSION=$PLAIN; ALIAS="" ;;   # A prerelease keeps its full name and is not "latest".
    *) VERSION=${PLAIN%.*}; ALIAS=latest ;;  # 1.2.3 updates the 1.2 docs.
  esac
else
  VERSION=dev
  ALIAS=""
fi
if [ -n "$ALIAS" ]; then
  $RUN mike deploy --push --update-aliases "$VERSION" "$ALIAS"
  $RUN mike set-default --push "$ALIAS"
else
  $RUN mike deploy --push "$VERSION"
fi
