#!/usr/bin/env bash
set -euo pipefail

command=$(jq -r '.tool_input.command // ""')
scrubbed=${command//.env.example/}

deny() {
  jq -n --arg reason "$1" '{hookSpecificOutput: {hookEventName: "PreToolUse", permissionDecision: "deny", permissionDecisionReason: $reason}}'
  exit 0
}

if grep -Eq '(^|[^A-Za-z0-9_-])\.env([^A-Za-z0-9_-]|$)|\.env\.[A-Za-z]+' <<<"$scrubbed"; then
  deny "Blocked: this command touches a .env file. Secret files are off limits for Claude in this project; ask the user to run it with ! if it is really needed."
fi
if grep -Eq 'public/firebase-config\.json|serviceAccount[^ ]*\.json|firebase-adminsdk[^ ]*\.json|\.firebaserc|\.pem([^A-Za-z]|$)|id_rsa' <<<"$scrubbed"; then
  deny "Blocked: this command touches a generated Firebase settings file or a key file. Claude must not read, write or copy it."
fi
if grep -Eq 'firebase-config\.mjs|npm (run )?(start|build|build:pages|config:firebase|start:emulators)([^A-Za-z:-]|$)|npm run (start|build|build:pages|config:firebase|start:emulators)([^A-Za-z:-]|$)' <<<"$scrubbed"; then
  deny "Blocked: this script reads .env.local or rewrites public/firebase-config.json. Use 'npx ng build' or 'npx ng serve' (with --configuration emulators for local tests) instead."
fi
exit 0
