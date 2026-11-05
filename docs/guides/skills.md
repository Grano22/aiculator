# Skills

Skills live in one place, `.agents/skills/`, the cross-client convention from [agentskills.io](https://agentskills.io). Harnesses that scan it need nothing, and the rest get a symlink, so an edit reaches all of them.

!!! note "`.agents`, plural"
    Harnesses scan `.agents/skills`, so that is the main location. Skills in `.agent/skills` are picked up too, and linked across.

## Install

```sh
aiculator skill --install '*'                   # All skills, to every installed harness
aiculator skill --install 'swarm-*' '*-tool'    # By pattern
aiculator skill --install '*' --global          # ~/.agents/skills and home directories
aiculator skill --install '*' --harness all     # Every supported harness
aiculator skill --install x --harness claude,pi
aiculator skill --install x --path ./vendor/skills --harness none
aiculator skill --install x --source ~/my-skills
aiculator skill --install x --copy              # Copy, for tools that do not follow links
```

Patterns use `*` for anything, `?` for one character and `[abc]` for a set.

!!! warning "Quote your patterns"
    A bare `*` works: the shell expands it into the directory listing, and aiculator recognizes that exact list as "all". But zsh stops with an error on an unmatched `*-sth` before aiculator runs, so quote anything else.

## Manage

```sh
aiculator skill --list                # Versions, lint state and who can see what
aiculator skill --lint                # Check against the Agent Skills spec
aiculator skill --new my-skill --description "..."
aiculator skill --uninstall 'swarm-*'           # Remove links; keep the source
aiculator skill --uninstall 'swarm-*' --purge   # Also delete the source
```

## Safety

- Your own directories are never replaced, even with `--force`.
- A skill that fails the linter is not linked anywhere.
- Reinstalling keeps your edits to a bundled skill. `--force` restores the bundled version.

## Bundled skills

| Skill | Use |
|---|---|
| `swarm-join` | Work the queue. It only starts when you ask for it. |
| `swarm-plan` | Turn a spec into tasks. |
| `swarm-parallel` | Split one task among subagents in one worktree, without conflicts. |
| `swarm-operate` | Start the server, add tasks, launch workers. |

## Metadata

Use the spec's fields first. These are understood everywhere:

```yaml
---
name: my-skill              # Must match the directory
description: What it does and when to use it, in words people actually use.
license: MIT
compatibility: Requires git and internet access.
allowed-tools: Read Bash(git:*)
metadata:                   # String values only, so quote numbers
  version: "1.0.0"
---
```

Some fields work only in certain harnesses. Others ignore them, so they are safe in project skills, but claude.ai uploads and the Skills API reject them.

| Field or file | Honored by |
|---|---|
| `disable-model-invocation` | Claude Code, Cursor, Pi |
| `paths` | Claude Code, Cursor |
| `when_to_use`, `argument-hint`, `arguments`, `user-invocable`, `disallowed-tools`, `model`, `effort`, `context`, `agent`, `hooks` | Claude Code |
| `icon`, `color` | Cursor |
| `agents/openai.yaml` | Codex |

`aiculator skill --lint` explains each of these when it finds one.
