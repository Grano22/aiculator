---
name: swarm-operate
description: Operates the agent swarm from an open coding session, starting the server, adding tasks, launching harness workers, checking status and handling parked tasks. Use when the user asks to start the swarm, add a task, launch or scale workers, see swarm status, or unpark a task.
license: MIT
compatibility: Requires git and the aiculator binary on PATH. Launching a harness needs that harness installed and logged in.
allowed-tools: Bash(aiculator:*)
argument-hint: "[what to do]"
when_to_use: The user asks about swarm status, adding tasks, launching workers, or parked tasks.
metadata:
  version: "1.0.0"
  aiculator.kind: operator
  aiculator.channels: shell
  aiculator.min-version: "0.1.0"
---

# Operate the swarm

| Goal | Command |
|---|---|
| Start or stop the server | `aiculator up`, `aiculator down` |
| See what is going on | `aiculator situation`, `aiculator task ls`, `aiculator task show ID` |
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

## Reading results

Finished work lands on the `swarm/integration` branch and never on the user's own branches. Show the diff with `git log swarm/integration` and leave merging to the user unless they ask.

A parked task needs a human: `needs_resplit` failed at every tier, and `budget_exhausted` ran out of budget. Tell the user which one and suggest what to do.
