# Tasks and tiers

## Lifecycle

```mermaid
stateDiagram-v2
    [*] --> draft
    draft --> ready: published
    ready --> claimed: granted
    claimed --> running: first heartbeat or progress
    claimed --> ready: timeout or release
    claimed --> review: submit
    running --> review: submit
    running --> failed: fail or timeout
    running --> ready: release
    review --> merging: verified
    review --> failed: checks or review failed
    merging --> done: merged
    merging --> failed: merge conflict
    failed --> ready: retry or escalate
    failed --> parked: out of tiers or budget
    parked --> ready: unpark
    done --> [*]
    canceled --> [*]
```

Any state except `done` can also move to `canceled`.

## Tiers

A tier is a number, and which model counts as which tier is your choice, set in the agent's token.

| Tier | Typical work | Takes lower-tier tasks |
|---|---|---|
| **1 Standard** | Mechanical edits, tests, docs | None lower |
| **2 Advanced** | Multi-file features, debugging, reviews | Tier 1 tasks, after 2 minutes |
| **3 Ultra** | Architecture and the hardest problems | Tier 2 tasks, after 5 minutes |

Eligibility also needs the task's tags, and no overlap between its `--touches` paths and those of work in progress.

## What happens on failure

1. Two failures raise the task's tier.
2. The next attempt inherits the saved work and the earlier notes, marked as untrusted.
3. After the top tier fails twice, the task is parked with the reason `needs_resplit`, for a human or a planner.

A task that goes over its budget is parked with `budget_exhausted`. `aiculator task unpark ID --add-budget N` returns it to the queue.

A claim that times out before work starts costs nothing, and neither does a `release`.

## Zombies

A heartbeat shows only that an agent is alive. Progress is measured separately: the swarm hashes the worktree itself, and only a changed hash counts. An agent with a live heartbeat and unchanged files loses its lease when the progress timeout runs out, which is 10 minutes by default. Its late submit is refused, and its saved work goes to the next attempt.

Most heartbeats only push a lease deadline forward, so they aren't all written to disk. One is kept in memory while the stored deadline is still more than half a lease away, and written once it isn't. Any heartbeat that changes something else, such as the first one moving a task from claimed to running, is always written. Fencing uses the lease number, which a heartbeat never changes, so a stored deadline that is behind can't let a second agent in. After a crash or a takeover, the stored deadline still leaves at least half a lease, and no lease expires within the restart grace anyway. `engine.Config.DurableHeartbeats` writes every heartbeat instead.
