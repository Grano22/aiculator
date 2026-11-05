---
name: swarm-parallel
description: Splits one task among subagents working at the same time in one worktree, without conflicts, by planning which jobs cannot overlap and checking afterwards what each wave changed. Use when work splits into parts that touch different files, or the user asks to do several things at once in this checkout.
license: MIT
compatibility: Requires git and the aiculator binary on PATH. Needs no server. Falls back to doing the jobs in order in a harness without subagents.
allowed-tools: Bash(aiculator parallel:*)
when_to_use: Work splits into independent parts in one checkout, or the user asks to do several things at once or in parallel.
metadata:
  version: "1.0.0"
  aiculator.kind: flow
  aiculator.channels: shell
  aiculator.min-version: "0.6.0"
---

# Parallel subagents in one worktree

Subagents here share files, and no merge step will catch a collision. So the plan decides what may run together, not your judgment. If it says two jobs conflict, they do not share a wave, however small they look.

## 1. List the jobs

Write a file outside the project, such as in a temporary folder, with each job and everything it will change, as a file or a directory ending in `/`. Add `reads` for files it relies on staying the same. Include what is easy to forget: tests, and shared files such as lockfiles, generated code and indexes. A job that changes files you cannot predict gets `exclusive: true`, and `deps` say what must finish first.

```yaml
jobs:
  - id: api
    touches: [internal/api/users.go]
  - id: docs
    touches: [docs/users.md]
    reads: [internal/api/users.go]
  - id: format
    exclusive: true
    deps: [api, docs]
```

## 2. Get the plan

```sh
aiculator parallel plan --file jobs.yaml --max 4 --granularity auto -o plan.json
```

If it says `Sequential`, do the jobs yourself, one at a time. Do not run them together anyway. Do not reword the jobs to get a different answer. Two jobs in one file conflict, because two subagents editing one file can overwrite each other, and naming parts of the file does not change that.

## 3. Run each wave, in order

1. Record the state: `aiculator parallel snapshot`, and keep what it prints.
2. Start one subagent per job in that wave, all in the same turn.
3. Wait for every one of them.
4. Check them: `aiculator parallel verify --plan plan.json --wave N --since SNAPSHOT`.
5. If it reports files no job claimed, stop. Look at what changed, fix it yourself, and do not start the next wave until the tree is right.
6. If it lists a job that changed nothing, check whether that subagent really did its work.
7. Run the tests before the next wave.

## Tell every subagent, in its prompt

- Exactly which files it may change, and that it must change nothing else, even to fix something it notices. It reports that to you instead.
- To read a file again just before changing it, and to make small, targeted edits rather than rewriting the file.
- Not to run `git add`, `git commit`, `git stash`, `git checkout` or `git reset`, formatters over the whole project, or anything that installs dependencies. Others are working in the same tree.
- Not to call any swarm tools. A lease belongs to you, and only you report progress or submit.

## Exceptions

- **If your harness cannot run subagents,** do the jobs yourself, in the planned order.
- **If you are a subagent yourself,** do not start more. Do your own part, and report back.
- **For separate tasks that take a long time,** use the swarm's own worktrees instead, because those can also be reviewed and merged one by one.
