#!/bin/sh
# Gate for joining: real Claude Code sessions join using only what aiculator join wrote.
# ROUTE=mcp is a new session, using .mcp.json and the linked skill.
# ROUTE=shell is a session already open: no swarm MCP, only the pasted one-liner.
# HARNESS is claude (default) or opencode. Uses that harness's account.
# MODEL defaults to the cheapest tier for claude and a free model for opencode.
set -eu
ROOT=$(cd "$(dirname "$0")/.." && pwd)
ROUTE=${ROUTE:-mcp}
HARNESS=${HARNESS:-claude}
if [ "$HARNESS" = opencode ]; then MODEL=${MODEL:-opencode/glm-5.3-flash-free}; else MODEL=${MODEL:-haiku}; fi
LIMIT=${LIMIT:-280}
N=${N:-3}
D=$(mktemp -d); D=$(cd "$D" && pwd -P)
export PATH="$ROOT/bin:$PATH"
cd "$D"
git init -q -b main .
git -c user.name=t -c user.email=t@t commit -q --allow-empty -m root
trap 'aiculator down >/dev/null 2>&1 || true' EXIT
aiculator join --harness "$HARNESS" --tier 1 | tail -12
i=1
while [ "$i" -le "$N" ]; do
  aiculator task add --id "j$i" --title "Create note$i.txt" \
    --spec "Create the file note$i.txt containing exactly the word swarm$i followed by a newline." \
    --check "grep -qx swarm$i note$i.txt" >/dev/null
  i=$((i+1))
done
JOIN="Read .agents/skills/swarm-join/SKILL.md and follow it. Join as tier 1."
if [ "$HARNESS" = opencode ]; then
  # --standalone: the shared background service does not pick up new project config.
  # An open session has no swarm MCP, so the shell route removes the config it reads.
  if [ "$ROUTE" = shell ]; then rm -f opencode.json; fi
  perl -e 'alarm shift; exec @ARGV' "$LIMIT" opencode run --standalone -m "$MODEL" "$JOIN" < /dev/null > "$D/claude.out" 2>&1 || echo "opencode exited with $?"
elif [ "$ROUTE" = mcp ]; then
  claude -p "/swarm-join 1" --model "$MODEL" --permission-mode acceptEdits \
    --allowedTools "mcp__swarm__*,Write,Edit,Read" --max-turns 80 < /dev/null > "$D/claude.out" 2>&1 || echo "claude exited with $?"
else
  claude -p "$JOIN" --model "$MODEL" --strict-mcp-config --permission-mode acceptEdits \
    --allowedTools "Bash(aiculator work:*),Write,Edit,Read" --max-turns 80 < /dev/null > "$D/claude.out" 2>&1 || echo "claude exited with $?"
fi
sleep 3
aiculator task ls
done_count=$(aiculator task ls | awk '$2=="done"' | wc -l | tr -d ' ')
owners=$(grep -c 'mcp call' .swarm/server.log || true)
echo "--- route=$ROUTE done=$done_count/$N mcp_calls=$owners"
tail -4 "$D/claude.out" | cut -c1-400
echo "logs in $D"
[ "$done_count" = "$N" ] || exit 1
# A pass through the wrong route proves nothing about the route being tested.
if [ "$ROUTE" = mcp ] && [ "$owners" = 0 ]; then echo "FAILED: finished without using MCP"; exit 1; fi
if [ "$ROUTE" = shell ] && [ "$owners" != 0 ]; then echo "FAILED: finished without using the shell route"; exit 1; fi
