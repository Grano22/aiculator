#!/bin/bash
# Installs the package from each signed repository in SITE, in a clean container, the way
# a user would, with signature checks on. Then runs it.
# usage: test_linux_repo.sh SITE VERSION
set -euo pipefail
SITE=$(cd "$1" && pwd)
VERSION=$2
PORT=8731
URL="http://127.0.0.1:$PORT"

python3 -m http.server "$PORT" --bind 127.0.0.1 --directory "$SITE" > /dev/null 2>&1 &
SERVER=$!
trap 'kill "$SERVER" 2>/dev/null || true' EXIT
for _ in $(seq 40); do
  curl -fsS -o /dev/null "$URL/aiculator.asc" && break
  sleep 0.25
done

check() { # name image script
  echo "--- $1 ($2)"
  docker run --rm --network host -e URL="$URL" -e VERSION="$VERSION" "$2" sh -euc "$3"
}

# shellcheck disable=SC2016 # These run inside the containers.
check apt debian:stable-slim '
  apt-get update -qq
  apt-get install -qq -y --no-install-recommends curl ca-certificates > /dev/null
  mkdir -p /etc/apt/keyrings
  curl -fsS "$URL/aiculator.gpg" -o /etc/apt/keyrings/aiculator.gpg
  echo "deb [signed-by=/etc/apt/keyrings/aiculator.gpg] $URL/apt stable main" > /etc/apt/sources.list.d/aiculator.list
  apt-get update -qq
  apt-get install -qq -y aiculator > /dev/null
  aiculator --version | grep -F "$VERSION"
  test -f /usr/share/doc/aiculator/THIRD_PARTY_NOTICES.md
'
# shellcheck disable=SC2016
check dnf fedora:latest '
  printf "[aiculator]\nname=Aiculator\nbaseurl=%s/rpm\nenabled=1\ngpgcheck=1\nrepo_gpgcheck=1\ngpgkey=%s/aiculator.asc\n" "$URL" "$URL" > /etc/yum.repos.d/aiculator.repo
  dnf install -q -y aiculator
  aiculator --version | grep -F "$VERSION"
  test -f /usr/share/doc/aiculator/THIRD_PARTY_NOTICES.md
'
# shellcheck disable=SC2016
check apk alpine:latest '
  wget -q "$URL/aiculator.rsa.pub" -O /etc/apk/keys/aiculator.rsa.pub
  echo "$URL/apk" >> /etc/apk/repositories
  apk update --quiet
  apk add --quiet aiculator
  aiculator --version | grep -F "$VERSION"
  test -f /usr/share/doc/aiculator/THIRD_PARTY_NOTICES.md
'
# A repository signed by a key the client does not trust must be refused, or the checks above prove nothing.
# shellcheck disable=SC2016
check apt-untrusted debian:stable-slim '
  apt-get update -qq
  apt-get install -qq -y --no-install-recommends curl ca-certificates > /dev/null
  mkdir -p /etc/apt/keyrings
  : > /etc/apt/keyrings/empty.gpg
  echo "deb [signed-by=/etc/apt/keyrings/empty.gpg] $URL/apt stable main" > /etc/apt/sources.list.d/aiculator.list
  if apt-get update -qq -o APT::Update::Error-Mode=any 2>/dev/null; then
    echo "apt accepted a repository signed by an untrusted key" >&2
    exit 1
  fi
  echo "refused, as it should be"
'
echo "all repositories work"
