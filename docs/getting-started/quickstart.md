# Quickstart

This takes a repository from nothing to one finished, verified task.

## 1. Connect the project

Run this in a git repository with at least one commit:

```sh
aiculator join --tier 1
```

It does three things, and writes no secrets:

- Starts a local server, keeping its data in `.swarm/`, which is excluded from git.
- Installs the bundled skills into `.agents/skills`.
- Adds the swarm's MCP server to the config of each harness it finds.

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

## 5. See the result

Finished work lands on the `swarm/integration` branch. Your own branches are never touched, so you choose when to merge it:

```sh
git log swarm/integration
git merge swarm/integration
```

## Next

- [Plan a whole spec](../guides/planning.md) instead of adding tasks one at a time.
- [Run sandboxed workers](../guides/workers.md) for work you do not fully trust.
