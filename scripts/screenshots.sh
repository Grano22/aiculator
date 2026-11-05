#!/bin/bash
# Regenerates docs/assets/screens, and the README's demo.gif, from a real server doing real
# work for about 90 seconds, with agents driven through the CLI, so every image shows actual
# output. Only the token and cost figures are demo values, because no model runs here.
# Needs Chrome and Node.
set -euo pipefail
ROOT=$(cd "$(dirname "$0")/.." && pwd)
OUT="$ROOT/docs/assets/screens"
go build -C "$ROOT" -o bin/aiculator ./cmd/aiculator
export PATH="$ROOT/bin:$PATH"
D=$(mktemp -d); D=$(cd "$D" && pwd -P)
cd "$D"
git init -q -b main .
git -c user.name=demo -c user.email=demo@example.com commit -q --allow-empty -m "Initial commit"
mkdir -p .swarm
excl=$(git rev-parse --git-path info/exclude); echo ".swarm/" >> "$excl"
# Shorter timeouts than the defaults, so a zombie can be caught during the demo.
aiculator serve --data .swarm --addr 127.0.0.1:0 --repo . --lease-ttl 5m --progress-ttl 70s > server.log 2>&1 &
SERVER=$!
trap 'kill $SERVER 2>/dev/null || true; cd /; rm -rf "$D"' EXIT
for _ in $(seq 100); do [ -f .swarm/addr ] && break; sleep 0.1; done

work=(ship-api "Add the /healthz endpoint" ship-auth "Validate session tokens" ship-log "Structured request logging" ship-rate "Rate limit the public API" ship-cache "Cache the pricing lookup" ship-test "Cover the retry path with tests" ship-docs "Document the webhooks" ship-i18n "Extract the settings strings")
n=0
for round in $(seq 16); do
  for ((i = 0; i < ${#work[@]}; i += 2)); do
    n=$((n + 1))
    aiculator task add --id "${work[i]}-$round" --title "${work[i+1]}" --check "test -f out-$n" --budget 5000000 --priority $((RANDOM % 3)) > /dev/null
    echo "out-$n" > ".swarm/file-${work[i]}-$round"
    # Some fail their checks on the first try and pass on the second, so the retry funnel
    # and the failure flow have real retries to show.
    if [ $((n % 5)) = 0 ]; then touch ".swarm/flaky-${work[i]}-$round"; fi
  done
done
aiculator task add --id billing-v2 --title "Move billing to the new ledger" --tier 3 --budget 5000000 --check "true" > /dev/null
aiculator task add --id search-idx --title "Rebuild the search index" --tier 2 --check "true" > /dev/null
aiculator task add --id storage --title "Redesign the storage layer" --tier 3 --check "false" > /dev/null
aiculator task add --id spend-cap --title "Migrate the invoices table" --tier 2 --budget 1000000 --check "false" > /dev/null
aiculator task add --id silent --title "Update the changelog" --tier 2 --check "true" > /dev/null

field() { python3 -c "import json,sys;d=json.load(sys.stdin);t=d.get('task') or {};print(d['action'],t.get('id','-'),d.get('lease',0),d.get('workdir','-'))"; }
A=$(aiculator work join --agent claude-sonnet --tier 1)
B=$(aiculator work join --agent opencode-glm --tier 1)
C=$(aiculator work join --agent codex-worker --tier 1)
ADV=$(aiculator work join --agent claude-opus --tier 2)
ULTRA=$(aiculator work join --agent claude-fable --tier 3)
GHOST=$(aiculator work join --agent gemini-stuck --tier 2)

# A zombie: it heartbeats but changes no files, so the progress timeout catches it.
aiculator work claim --agent "$GHOST" --task silent > /dev/null
Z=$(aiculator task show silent | python3 -c "import json,sys;print(json.load(sys.stdin)['lease_epoch'])")
aiculator work heartbeat --agent "$GHOST" --task silent --lease "$Z" > /dev/null

# A budget that runs out, which parks the task for a human.
aiculator work claim --agent "$ADV" --task spend-cap > /dev/null
L=$(aiculator task show spend-cap | python3 -c "import json,sys;print(json.load(sys.stdin)['lease_epoch'])")
aiculator work fail --agent "$ADV" --task spend-cap --lease "$L" --cost 1200000 --tokens 48000 --note "Cannot reach the staging database" > /dev/null

# The top tier fails twice, which parks the task for a human too.
for _ in 1 2; do
  aiculator work claim --agent "$ULTRA" --task storage > /dev/null
  L=$(aiculator task show storage | python3 -c "import json,sys;print(json.load(sys.stdin)['lease_epoch'])")
  aiculator work fail --agent "$ULTRA" --task storage --lease "$L" --cost 900000 --tokens 31000 --note "Needs a smaller first step" > /dev/null
  sleep 0.2
done

worker() { # agent seconds-between-tasks
  local agent=$1 gap=$2 end=$(( $(date +%s) + 140 ))
  while [ "$(date +%s)" -lt "$end" ]; do
    read -r act id lease wd < <(aiculator work wait --agent "$agent" --hold 1s | field)
    if [ "$act" = task ] && [ -f ".swarm/file-$id" ]; then
      if [ -f ".swarm/flaky-$id" ] && [ ! -f ".swarm/tried-$id" ]; then
        touch ".swarm/tried-$id" "$wd/notes.txt" # Changes a file, but not the one the check wants.
      else
        touch "$wd/$(cat ".swarm/file-$id")"
      fi
      aiculator work progress --agent "$agent" --task "$id" --lease "$lease" --tokens $((3000 + RANDOM % 9000)) --cost $((40000 + RANDOM % 160000)) --note "Implemented" > /dev/null
      sleep "$gap"
      aiculator work submit --agent "$agent" --task "$id" --lease "$lease" > /dev/null
    fi
    sleep "$gap"
  done
}
worker "$A" 1.3 & P1=$!
worker "$B" 1.9 & P2=$!
worker "$C" 2.6 & P3=$!

sleep 12
# Plans change: two tasks are no longer needed.
aiculator task cancel ship-i18n-16 > /dev/null
aiculator task cancel ship-docs-16 > /dev/null
sleep 10
# Claimed late on purpose, so it is close to its timeout when the pictures are taken.
aiculator work claim --agent "$ADV" --task search-idx > /dev/null
L=$(aiculator task show search-idx | python3 -c "import json,sys;print(json.load(sys.stdin)['lease_epoch'])")
aiculator work heartbeat --agent "$ADV" --task search-idx --lease "$L" > /dev/null
sleep 10
aiculator work claim --agent "$ULTRA" --task billing-v2 > /dev/null
L=$(aiculator task show billing-v2 | python3 -c "import json,sys;print(json.load(sys.stdin)['lease_epoch'])")
touch .swarm/x
aiculator work progress --agent "$ULTRA" --task billing-v2 --lease "$L" --tokens 86000 --cost 4300000 --note "Halfway" > /dev/null
sleep 36
EPOCH=$(aiculator plan begin --ttl 5m | python3 -c "import json,sys;print(json.load(sys.stdin)['epoch'])")
aiculator plan update --epoch "$EPOCH" --ttl 5m --phase "splitting the mobile spec" --expected 1=8 --expected 2=2 > /dev/null
sleep 9

node "$ROOT/scripts/screenshots.mjs" "$(aiculator web --no-open)" "$OUT" "$D"
go run -C "$ROOT" ./scripts/gifmake -out "$OUT/demo.gif" -width 960 -delay 50 -hold 250 "$D"/frames/frame-*.png
kill "$P1" "$P2" "$P3" 2>/dev/null || true
pkill -f "aiculator work wait" 2>/dev/null || true
echo "--- what the images show"
aiculator top --once --width 100 | sed -n 1,9p
ls -la "$OUT" | awk 'NR>3 { printf "%d KB  %s\n", $5/1024, $9 }'
