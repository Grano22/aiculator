# Aiculator

Aiculator runs a queue of coding tasks across AI harnesses such as Claude Code and OpenCode. It gives each task to an agent of the right tier, isolates the work in a git worktree, runs the acceptance checks itself, and merges what passes.

**An agent saying "done" counts for nothing.** The swarm verifies every result on its own.

<div class="grid cards" markdown>

-   **New here?**

    Install it and finish your first task in a few minutes.

    [Quickstart](getting-started/quickstart.md)

-   **Have a harness open already?**

    Join the swarm from that session, with no restart.

    [Join from an open harness](guides/join.md)

-   **Want reusable instructions?**

    Write rules for subagents, retries and output formats once, then use them everywhere.

    [Templates](guides/templates.md)

-   **Curious how it stays correct?**

    See the crash tests and formal models behind it.

    [Verification](concepts/verification.md)

</div>

## What you get

| Capability | What it does |
|---|---|
| **Fast dispatch** | Agents receive a task that is already theirs, so they never race for it. |
| **Model tiers** | Standard, advanced and ultra. Failures move a task up a tier, and the next attempt inherits the earlier work and notes. |
| **Planner signal** | Waiting agents see what the planner is doing, so they know whether to wait or exit. |
| **Independent verification** | Checks run in a clean checkout, inside a sandbox. Optional review by a different agent. |
| **Zombie detection** | An agent with a live heartbeat but unchanged files loses its task. |
| **One place for skills** | Keep skills in `.agents/skills`, and every harness sees them. |

!!! warning "Early software"
    This is version 0.1. Only Claude Code and OpenCode have been run end to end, and several planned features are not built. See [Status](project/status.md) for exactly what works.
