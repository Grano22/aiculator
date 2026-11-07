---
name: swarm-operate
description: Operates the agent swarm from an open coding session, starting the server, adding tasks, launching harness workers, checking status and handling parked tasks. Use when the user asks to start the swarm, add a task, launch or scale workers, see swarm status, or unpark a task.
license: MIT
compatibility: Requires git and the aiculator binary on PATH. Launching a harness needs that harness installed and logged in.
allowed-tools: Bash(aiculator:*)
argument-hint: "[what to do]"
when_to_use: The user asks about swarm status, adding tasks, launching workers, or parked tasks.
metadata:
  version: "1.2.0"
  aiculator.kind: operator
  aiculator.channels: shell
  aiculator.min-version: "0.8.0"
---

# Operate the swarm

| Goal | Command |
|---|---|
| Start or stop the server | `aiculator up`, `aiculator down` |
| Stop every project's server | `aiculator down --all`, or `aiculator realm stop NAME...` |
| See what is going on | `aiculator situation`, `aiculator task ls`, `aiculator task show ID` |
| See who is connected, from where | `aiculator sessions`: kind, harness, model, host, process, folder, state and any usage limit |
| Clear disconnected sessions | `aiculator sessions forget` |
| Stop agents | `aiculator agents stop --all` tells them to exit and hands back what they hold |
| Kill agent processes | `aiculator agents kill --all` also ends the drivers aiculator started; add `--realms` for every project |
| Watch live | `aiculator top`, then `r` for live mode; `aiculator web --control` adds stop and kill buttons |
| Size a task | `aiculator task estimate ID`, or `--title "..." --tier N` for one not added yet |
| See what agents share | `aiculator memory status`, `aiculator memory ls`, `aiculator memory recall "question"` |
| Ask the code graph | `aiculator code query "where is X"`, `aiculator code symbol NAME` |
| Follow changes from a script | `aiculator events --since SEQ --follow` resumes after the last event it printed |
| Add one task | `aiculator task add --title "..." --spec "..." --tier 1 --check "go test ./..."` |
| See harnesses on this machine | `aiculator harness` |
| Connect a harness | `aiculator join --harness ID --tier N` |
| Stop or return a task | `aiculator task cancel ID`, `aiculator task unpark ID --add-budget N` |

## Launching sandboxed workers

Unlike a session joined by hand, these run in the swarm's sandbox and start only when there is work, so idle time costs no model turns:

```sh
SWARM_TOKEN=$(aiculator token --agent worker1 --tier 1) aiculator drive --harness claude --agent worker1
```

Run it in the background for several workers, with a distinct agent name each. Do not start harnesses the user has not asked for, because they spend the user's money.

A driver can start before the server: it waits for it, and follows it across a restart. If the harness runs out of usage or credit, the driver hands its task back with no penalty, waits for the reset the harness reported, and carries on (`--on-limit exit` stops instead). `aiculator sessions` shows such an agent as limited, with when.

## Shared memory

The server builds a graph of the code from the integration branch, and rebuilds it after each merge. Agents ask it instead of reading files, and leave each other short notes. With Graphify installed (`uv tool install graphifyy`), the graph is Graphify's, built with tree-sitter, and a `graphify-out/graph.json` you built yourself is used as its start. Without it, a built-in indexer stands in. `aiculator serve --code-graph off` turns the graph off.

## Reading results

Finished work lands on the `swarm/integration` branch and never on the user's own branches. Show the diff with `git log swarm/integration` and leave merging to the user unless they ask.

Before adding a large task, `aiculator task estimate --title "..."` says what similar finished tasks took. An estimate far above the others is a reason to split it.

A parked task needs a human: `needs_resplit` failed at every tier, and `budget_exhausted` ran out of budget. Tell the user which one and suggest what to do.
