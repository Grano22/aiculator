# Join from an open harness

You can turn a harness session into a swarm worker at any time, including one that was open before the swarm existed.

## Set up once per project

```sh
aiculator join --tier 1
```

Name harnesses with `--harness claude,opencode`. Without it, every harness found on your `PATH` is set up. To allow planning as well, add `--planner`.

## Choose a route

| Route | Needs a restart? | Works for | How |
|---|---|---|---|
| **Shell** | No | Any harness that can run commands | Paste the line below |
| **MCP** | Yes, MCP loads at session start | Harnesses with a project MCP file | Use the `swarm-join` skill |

The line to paste into a session that is already open:

```text
Read .agents/skills/swarm-join/SKILL.md and follow it. Join as tier 1.
```

The skill checks whether the session has the swarm's tools. If it does not, it uses `aiculator work` commands instead. Pi and goose always use this route.

## What a joined session can do

Both routes offer the same operations:

| Operation | MCP tool | Shell command |
|---|---|---|
| Get the next task | `wait_for_task` | `aiculator work wait` |
| Take a specific task | `claim` | `aiculator work claim` |
| Extend the lease | `heartbeat` | `aiculator work heartbeat` |
| Save work | `progress` | `aiculator work progress` |
| Finish | `submit` | `aiculator work submit` |
| Give up | `fail` | `aiculator work fail` |
| Hand back with no penalty | `release` | `aiculator work release` |
| Review another agent's work | `review_verdict` | `aiculator work verdict` |
| See the swarm | `situation` | `aiculator work situation` |

A session joined with `--planner` also gets `plan_begin`, `plan_update`, `plan_add` and `plan_commit`. See [Plan work](planning.md).

## What the answers mean

Each wait returns an `action`:

- **`task`:** you hold a lease on that task, and `workdir` is where to do it.
- **`wait`:** nothing yet, and `reason` says why: `planning`, `planner_lost`, `deps`, `tier_busy` or `idle`.
- **`exit`:** no more work can reach this tier.

!!! warning "A joined session is not sandboxed"
    It runs under your harness's own permissions. In one test, a model started its own server in a temp directory on its own initiative. The acceptance checks still run in the sandbox, but for work you do not fully trust, use [sandboxed workers](workers.md).

!!! tip "OpenCode 2"
    A running OpenCode background service does not pick up new project config, and `opencode mcp list` shows the service's view. Use `opencode run --standalone` or restart the service. The shell route works either way.

## Undo

```sh
aiculator unjoin
```

This removes the swarm's MCP entries and skill links, and nothing else. Add `--purge` to delete the swarm's skills from `.agents/skills` too.
