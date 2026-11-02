#!/bin/sh
# Phase 2 gate: a real Claude Code session takes and finishes 10 tasks over MCP.
# It uses your Claude account. MODEL defaults to the cheapest tier.
set -eu
ROOT=$(cd "$(dirname "$0")/.." && pwd)
MODEL=${MODEL:-haiku}
D=$(mktemp -d)
D=$(cd "$D" && pwd -P)
SW="$ROOT/bin/aiculator --data $D/data"
git init -q -b main "$D/repo"
git -C "$D/repo" -c user.name=t -c user.email=t@t commit -q --allow-empty -m root

$SW serve --addr 127.0.0.1:0 --repo "$D/repo" --exit-grace 10s > "$D/server.log" 2>&1 &
SERVER=$!
trap 'kill $SERVER 2>/dev/null || true' EXIT
for _ in $(seq 100); do [ -f "$D/data/addr" ] && break; sleep 0.1; done
URL=$(cat "$D/data/addr")

for i in 1 2 3 4 5 6 7 8 9 10; do
  $SW task add --id "gate$i" --title "Create note$i.txt" \
    --spec "Create the file note$i.txt containing exactly the word swarm$i followed by a newline." \
    --check "grep -qx swarm$i note$i.txt" >/dev/null
done
TOKEN=$($SW token --agent claude-gate --tier 1)
cat > "$D/mcp.json" <<JSON
{"mcpServers":{"swarm":{"type":"http","url":"$URL/mcp","headers":{"Authorization":"Bearer $TOKEN"}}}}
JSON

echo "worktrees: $D/data/worktrees"
start=$(date +%s)
claude -p "You are a swarm worker. Call the swarm wait_for_task tool, follow the swarm server instructions exactly, and keep looping until wait_for_task answers with action exit. Each task gives a workdir: write files only there, using absolute paths under it. Finish each task by calling the swarm submit tool. Be brief." \
  --model "$MODEL" --strict-mcp-config --mcp-config "$D/mcp.json" \
  --add-dir "$D/data/worktrees" --permission-mode acceptEdits \
  --allowedTools "mcp__swarm__*,Write,Edit,Read" --max-turns 120 > "$D/claude.out" 2>&1 || echo "claude exited with $?"
took=$(( $(date +%s) - start ))
sleep 3
$SW task ls
done_count=$($SW task ls | awk '$2=="done"' | wc -l | tr -d ' ')
files=$(git -C "$D/repo" ls-tree --name-only swarm/integration | grep -c '^note' || true)
max_ms=$(grep 'mcp call' "$D/server.log" | sed -E 's/.* ms=([0-9]+).*/\1/' | sort -n | tail -1)
calls=$(grep -c 'mcp call' "$D/server.log" || true)
waits=$(grep 'mcp call' "$D/server.log" | grep -c 'tool=wait_for_task' || true)
echo "---"
echo "tasks done: $done_count/10, files on integration: $files/10"
echo "mcp calls: $calls (wait_for_task: $waits), slowest call: ${max_ms:-0} ms, session: ${took}s"
tail -5 "$D/claude.out"
echo "logs in $D"
[ "$done_count" = 10 ] && [ "$files" = 10 ] && [ "${max_ms:-0}" -lt 45000 ]
