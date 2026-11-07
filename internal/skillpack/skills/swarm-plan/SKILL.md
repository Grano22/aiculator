---
name: swarm-plan
description: Turns a spec into a task graph for the agent swarm from an open coding session, keeping waiting workers informed while planning. Use when the user asks to plan work for the swarm, split a spec into swarm tasks, or feed the queue.
license: MIT
compatibility: Requires the aiculator binary on PATH, with a server running for this project (aiculator up).
allowed-tools: mcp__swarm__plan_begin mcp__swarm__plan_update mcp__swarm__plan_add mcp__swarm__plan_commit mcp__swarm__plan_estimate mcp__swarm__code_query mcp__swarm__code_symbol mcp__swarm__memory_recall mcp__swarm__situation Bash(aiculator plan:*) Bash(aiculator situation) Bash(aiculator task estimate:*) Bash(aiculator code:*) Bash(aiculator memory recall:*) Read
argument-hint: "[spec or path to a spec]"
when_to_use: The user wants a spec turned into swarm tasks, or asks to plan work for the swarm.
metadata:
  version: "1.2.0"
  aiculator.kind: planner
  aiculator.channels: mcp shell
  aiculator.min-version: "0.1.0"
---

# Plan for the swarm

Workers wait while a plan is in progress, and they see your phase and hints live. Use the `plan_*` MCP tools if you have them, which needs a session joined with `aiculator join --planner`. Otherwise use `aiculator plan`.

## Steps

1. **Take the lease first, before reading the spec:** `aiculator plan begin --ttl 5m`. Keep the returned `epoch`. Workers now wait instead of exiting.
2. **Read the spec and the code,** and renew as you go. Ask the shared code graph before opening files (`code_query`, `code_symbol`, or `aiculator code query "..."`), and `memory_recall` for what agents learned: `aiculator plan update --epoch E --ttl 5m --phase "reading code"`.
3. **Publish hints once you know the shape:** `--expected 1=12 --expected 2=3`. A hint is a promise. Tier T agents may exit once nothing is expected at or below T, and after that the swarm refuses tasks that would have reached them. Only hint when sure.
4. **Add tasks:** `aiculator plan add --epoch E --id ID --title "..." --spec "..." --tier N --check "test command" --dep OTHER --touches path`. They stay invisible until step 5.
5. **Commit:** `aiculator plan commit --epoch E`. The whole plan appears at once. Cycles and unknown dependencies are rejected.

If you stop renewing, the lease expires, the drafts are canceled and workers see `planner_lost`. Your old epoch is then refused, so begin again.

## Writing good tasks

- **Tier by difficulty:** 1 for mechanical edits, tests and docs; 2 for multi-file features and debugging; 3 for architecture and the hardest problems. Failures escalate automatically, so start low.
- **Always give a check.** A task with `--check` is verified by the swarm running that command. Without one, it waits for another agent's review. A check must fail before the work and pass after it.
- **Point workers at the code:** name the symbols and files a task is about in its spec, as `code_query` found them, so the worker starts there instead of searching.
- **Declare `--touches`:** overlapping tasks then do not run at once, which avoids merge conflicts.
- **Keep tasks small** and self-contained, since a worker sees only its own spec and the code.
- **Size them from history:** `plan_estimate`, or `aiculator task estimate --title "..." --spec "..." --tier N`, predicts the tokens a task would take from finished tasks like it. One estimated far above the rest is a reason to split it, and an estimate is a fair starting `--budget`.
