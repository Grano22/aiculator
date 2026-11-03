#!/bin/bash
# Gate for the UI templates: does a real Claude Code session, given only the rendered skill,
# reach for the right question tool in the right mode? Uses your Claude account.
set -uo pipefail
ROOT=$(cd "$(dirname "$0")/.." && pwd)
MODEL=${MODEL:-haiku}
export PATH="$ROOT/bin:$PATH"
D=$(mktemp -d); D=$(cd "$D" && pwd -P)
trap 'cd /; rm -rf "$D"' EXIT
cd "$D"
git init -q -b main .
git -c user.name=t -c user.email=t@t commit -q --allow-empty -m r
aiculator template --install 'ui-*' subagent-flow living-section --per-harness --harness claude > /dev/null
# Print mode has nobody to answer, so Claude Code does not offer AskUserQuestion there. A small
# server offers a tool with the same name and schema, which records the arguments the model
# chooses. That tests its choice of mode, though not the interactive UI itself.
cat > "$D/ask.py" <<'PY'
import json, sys
SCHEMA = {"type": "object", "required": ["questions"], "properties": {"questions": {"type": "array", "minItems": 1, "maxItems": 4, "items": {
    "type": "object", "required": ["question", "header", "options", "multiSelect"], "properties": {
        "question": {"type": "string"}, "header": {"type": "string"}, "multiSelect": {"type": "boolean"},
        "options": {"type": "array", "minItems": 2, "maxItems": 4, "items": {"type": "object", "required": ["label", "description"],
            "properties": {"label": {"type": "string"}, "description": {"type": "string"}}}}}}}}}
for line in sys.stdin:
    m = json.loads(line)
    if "id" not in m: continue
    r = {}
    if m["method"] == "initialize":
        r = {"protocolVersion": m["params"]["protocolVersion"], "capabilities": {"tools": {}}, "serverInfo": {"name": "ask", "version": "1"}}
    elif m["method"] == "tools/list":
        r = {"tools": [{"name": "AskUserQuestion", "description": "Ask the user questions with selectable options.", "inputSchema": SCHEMA}]}
    elif m["method"] == "tools/call":
        r = {"content": [{"type": "text", "text": "The user answered with the first option of each question."}]}
    print(json.dumps({"jsonrpc": "2.0", "id": m["id"], "result": r}), flush=True)
PY
printf '{"mcpServers":{"ask":{"command":"python3","args":["%s/ask.py"]}}}' "$D" > "$D/mcp.json"
pass=0; fail=0
probe() { # name mcp|none prompt check-python
  local name=$1 mode=$2 prompt=$3 check=$4 extra=(--strict-mcp-config)
  if [ -n "${ONLY:-}" ] && [[ ! " $ONLY " == *" $name "* ]]; then return; fi
  [ "$mode" = mcp ] && extra+=(--mcp-config "$D/mcp.json")
  claude -p "$prompt" --model "$MODEL" --output-format stream-json --verbose --max-turns "${TURNS:-8}" "${extra[@]}" \
    --allowedTools "Skill,ToolSearch,Read,Write,Edit,Glob,Grep,Agent,Task,mcp__ask__AskUserQuestion" < /dev/null > "$name.jsonl" 2>/dev/null
  if python3 - "$name.jsonl" "$check" <<'PY'
import json, sys
calls, text = [], ""
for line in open(sys.argv[1]):
    try: m = json.loads(line)
    except ValueError: continue
    if m.get("type") != "assistant": continue
    for c in m["message"].get("content", []):
        if c.get("type") == "tool_use": calls.append((c["name"], c.get("input", {})))
        if c.get("type") == "text": text += c["text"]
asks = [i for n, i in calls if n.endswith("AskUserQuestion")]
first = asks[0]["questions"] if asks else []
ok = eval(sys.argv[2], {"asks": asks, "q": first, "calls": calls, "text": text, "any": any, "all": all, "len": len, "sum": sum, "open": open})
print("   tools:", [n.replace("mcp__ask__", "") for n, _ in calls])
if asks: print("   asked:", [(x["header"], "many" if x.get("multiSelect") else "one", [o["label"] for o in x["options"]]) for x in first])
sys.exit(0 if ok else 1)
PY
  then echo "PASS $name"; pass=$((pass+1)); else echo "FAIL $name"; fail=$((fail+1)); fi
}
probe select mcp "Use the ui-select skill. I want to add a database to my app, and I have not decided which. Ask me." \
  "len(asks) == 1 and len(q) == 1 and q[0]['multiSelect'] is False and 'Recommended' in q[0]['options'][0]['label']"
probe checkbox mcp "Use the ui-checkbox skill. Before the release I can run unit tests, lint, a security scan and a load test. Ask me which to run." \
  "len(asks) == 1 and q[0]['multiSelect'] is True"
probe tabs mcp "Use the ui-tabs skill. I am starting a new web service. You need to know its language, its database and its hosting. Ask me." \
  "len(asks) == 1 and len(q) >= 2"
probe fallback none "Use the ui-select skill. I want to add a database to my app, and I have not decided which. Ask me." \
  "not asks and '1.' in text and '2.' in text"
probe subagent none "You are a subagent started by another agent, and you cannot reach the user. Use the ui-select skill. Decide which database my app should use, then state your choice." \
  "not asks and any(n == 'Skill' for n, _ in calls) and 'postgres' in text.lower()"
probe living none "Use the living-section skill. I just finished the login page, and password reset is next. Record that now." \
  "any(n in ('Write','Edit') and 'PROGRESS.md' in i.get('file_path','') and 'dynamic:status:start' in i.get('content', i.get('new_string','')) for n, i in calls)"
printf 'package main\n\nfunc add(a, b int) int { return a - b }\n' > calc.go
TURNS=24 probe flow none "Use the subagent-flow skill. The add function in calc.go looks wrong. Find out why, fix it, and have it reviewed." \
  "any(n == 'Skill' for n, _ in calls) and sum(1 for n, _ in calls if n in ('Agent', 'Task')) >= 2 and 'a + b' in open('calc.go').read()"
echo "--- $pass passed, $fail failed"
[ "$fail" = 0 ]
