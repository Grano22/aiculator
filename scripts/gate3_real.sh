#!/bin/sh
# Phase 3 gate, real harness: one ACP harness finishes N tasks inside the sandbox.
# Usage: scripts/gate3_real.sh claude|gemini|codex [tasks]. Uses that harness's account.
set -eu
ROOT=$(cd "$(dirname "$0")/.." && pwd)
HARNESS=${1:?harness}
N=${2:-3}
D=$(mktemp -d)
D=$(cd "$D" && pwd -P)
SW="$ROOT/bin/aiculator --data $D/data"
git init -q -b main "$D/repo"
git -C "$D/repo" -c user.name=t -c user.email=t@t commit -q --allow-empty -m root
$SW serve --addr 127.0.0.1:0 --repo "$D/repo" --exit-grace 5s > "$D/server.log" 2>&1 &
SERVER=$!
trap 'kill $SERVER 2>/dev/null || true' EXIT
for _ in $(seq 100); do [ -f "$D/data/addr" ] && break; sleep 0.1; done
URL=$(cat "$D/data/addr")
i=1
while [ "$i" -le "$N" ]; do
  $SW task add --id "acp$i" --title "Create note$i.txt" \
    --spec "Create the file note$i.txt in the current directory containing exactly the word swarm$i followed by a newline. Then run this exact command with your Bash tool and report its output: grep -c swarm$i note$i.txt" \
    --check "grep -qx swarm$i note$i.txt" >/dev/null
  i=$((i+1))
done
export SWARM_TOKEN
SWARM_TOKEN=$($SW token --agent "$HARNESS-gate" --tier 1)
start=$(date +%s)
timeout_s=${TIMEOUT:-300}
perl -e 'alarm shift; exec @ARGV' "$timeout_s" "$ROOT/bin/aiculator" drive --harness "$HARNESS" --url "$URL" --agent "$HARNESS-gate" --heartbeat 5s --hold 5s > "$D/driver.log" 2>&1 || echo "driver exited with $?"
took=$(( $(date +%s) - start ))
sleep 2
$SW task ls
done_count=$($SW task ls | awk '$2=="done"' | wc -l | tr -d ' ')
echo "---"
echo "$HARNESS: $done_count/$N done in ${took}s"
grep -E 'driver finished|lease lost|reporting result' "$D/driver.log" | tail -5 || true
$SW task show acp1 | grep -E '"reason"|"text"' | tail -4 || true
echo "logs in $D"
[ "$done_count" = "$N" ]
