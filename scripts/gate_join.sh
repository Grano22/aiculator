#!/bin/sh
# Gate for joining: real Claude Code sessions join using only what aiculator join wrote.
# ROUTE=mcp is a new session, using .mcp.json and the linked skill.
# ROUTE=shell is a session already open: no swarm MCP, only the pasted one-liner.
# Uses your Claude account. MODEL defaults to the cheapest tier.
set -eu
ROOT=$(cd "$(dirname "$0")/.." && pwd)
ROUTE=${ROUTE:-mcp}
MODEL=${MODEL:-haiku}
N=${N:-3}
D=$(mktemp -d); D=$(cd "$D" && pwd -P)
export PATH="$ROOT/bin:$PATH"
cd "$D"
git init -q -b main .
git -c user.name=t -c user.email=t@t commit -q --allow-empty -m root
trap 'aiculator down >/dev/null 2>&1 || true' EXIT
aiculator join --harness claude --tier 1 | tail -12
i=1
while [ "$i" -le "$N" ]; do
  aiculator task add --id "j$i" --title "Create note$i.txt" \
    --spec "Create the file note$i.txt containing exactly the word swarm$i followed by a newline." \
    --check "grep -qx swarm$i note$i.txt" >/dev/null
  i=$((i+1))
done
if [ "$ROUTE" = mcp ]; then
  claude -p "/swarm-join 1" --model "$MODEL" --permission-mode acceptEdits \
    --allowedTools "mcp__swarm__*,Write,Edit,Read" --max-turns 80 < /dev/null > "$D/claude.out" 2>&1 || echo "claude exited with $?"
else
  claude -p "Read .agents/skills/swarm-join/SKILL.md and follow it. Join as tier 1." --model "$MODEL" \
    --strict-mcp-config --permission-mode acceptEdits \
    --allowedTools "Bash(aiculator work:*),Write,Edit,Read" --max-turns 80 < /dev/null > "$D/claude.out" 2>&1 || echo "claude exited with $?"
fi
sleep 3
aiculator task ls
done_count=$(aiculator task ls | awk '$2=="done"' | wc -l | tr -d ' ')
owners=$(grep -c 'mcp call' .swarm/server.log || true)
echo "--- route=$ROUTE done=$done_count/$N mcp_calls=$owners"
tail -4 "$D/claude.out" | cut -c1-400
echo "logs in $D"
[ "$done_count" = "$N" ]
