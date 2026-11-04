# Quickstart

This takes a repository from nothing to one finished, verified task.

## 1. Set up your machine, once

```sh
aiculator init
```

It asks which installed harnesses should use the swarm and whether to install its skills, then puts them where each harness reads them for every project. It needs no git repository, writes nothing until you have answered everything, and running it again is safe.

## 2. Connect the project

Run this in a git repository with at least one commit:

```sh
aiculator join --tier 1
```

It starts a local server, keeping its data in `.swarm/`, which is excluded from git, and adds the swarm's MCP server to the project's config for each harness. No secrets are written.

!!! tip "You can skip step 1"
    `join` works on its own too. It installs whatever a harness can't already find, so step 1 only saves it the work, and keeps your projects free of copies.

### Which setup should I use?

| Command | Sets up | Good for |
|---|---|---|
| `aiculator init` | This machine, for every project | Your own machine |
| `aiculator init --scoped` | This project, asking step by step | Trying it out, or skills you want committed with a project |
| `aiculator join` | This project, without asking, reusing what exists | Scripts, and each project after `init` |

Harnesses look for a skill in the project first, so a scoped install takes priority over a global one.

For a script, `aiculator init --yes` takes every default. To answer single questions, use `--harness claude,opencode`, `--no-skills`, `--mcp`, `--no-mcp`, `--tier 2` and `--dry-run`.

!!! note "Why MCP is not global by default"
    An MCP server's tools load into every session, using up context even where you aren't using the swarm. `join` adds them per project, which is usually what you want. To have them everywhere, answer yes in `init`, or use `--mcp`. In a folder with no swarm, the tools then answer with how to start one, rather than showing up as a broken server.

## 3. Add a task

```sh
aiculator task add \
  --title "Add a health endpoint" \
  --spec "Add GET /healthz returning 200." \
  --check "go test ./..."
```

The `--check` command is what decides whether the task is finished. Make sure it fails before the work and passes after it.

## 4. Get an agent working

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
