# Quickstart

This takes a repository from nothing to one finished, verified task.

## 1. Connect the project

Run this in your project:

```sh
aiculator init
```

It asks a few questions, and writes nothing until you have answered them all:

- Which installed harnesses should use the swarm.
- Whether to install the swarm's skills.
- Whether to add MCP config, and at which tier.
- Whether to start the server now.

If the folder has no git repository or no commits yet, it offers to create them, because the swarm's worktrees need a commit to start from.

No secrets are written, and running it again is safe. The server keeps its data in `.swarm/`, which is excluded from git.

!!! tip "In a script"
    `aiculator init --yes` takes every default without asking. It won't create a git repository unless you agree to, so run it in one that has a commit. To answer single questions, use `--harness claude,opencode`, `--no-skills`, `--no-mcp`, `--no-start` and `--tier 2`. Add `--dry-run` to see what would happen. `aiculator join` does the same setup, and also asks nothing.

## 2. Add a task

```sh
aiculator task add \
  --title "Add a health endpoint" \
  --spec "Add GET /healthz returning 200." \
  --check "go test ./..."
```

The `--check` command is what decides whether the task is finished. Make sure it fails before the work and passes after it.

## 3. Get an agent working

=== "A session already open"

    Paste this into it:

    ```text
    Read .agents/skills/swarm-join/SKILL.md and follow it. Join as tier 1.
    ```

=== "A new session"

    Start your harness in this directory, approve the MCP server if asked, and use the `swarm-join` skill. In Claude Code, that is `/swarm-join`.

=== "A sandboxed worker"

    ```sh
    SWARM_TOKEN=$(aiculator token --agent worker1 --tier 1) \
      aiculator drive --harness claude --agent worker1
    ```

## 4. Watch it finish

```sh
aiculator task ls
aiculator situation
```

The task moves through `ready`, `claimed`, `running`, `review` and `merging` to `done`.

For a live view, run `aiculator top` in a terminal or `aiculator web` for your browser. See [Dashboards](../guides/dashboards.md).

## 5. See the result

Finished work lands on the `swarm/integration` branch. Your own branches are never touched, so you choose when to merge it:

```sh
git log swarm/integration
git merge swarm/integration
```

## Next

- [Plan a whole spec](../guides/planning.md) instead of adding tasks one at a time.
- [Run sandboxed workers](../guides/workers.md) for work you do not fully trust.
