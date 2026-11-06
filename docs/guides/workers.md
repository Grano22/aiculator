# Run sandboxed workers

`aiculator drive` launches a harness for you over the Agent Client Protocol (ACP), one session per task, inside the sandbox.

```sh
SWARM_TOKEN=$(aiculator token --agent worker1 --tier 1) \
  aiculator drive --harness claude --agent worker1
```

## Why use it

- **Idle time costs nothing.** No harness runs until there is a task, so waiting uses no model turns.
- **It is sandboxed.** The harness can write only to its worktree, a private temp directory and its own state directories. It cannot read the swarm's secrets or credential stores such as `~/.ssh`.
- **Permissions follow a policy.** Reads, searches and commands are allowed because the sandbox contains them. Edits must stay inside the worktree. Unknown tool kinds are refused.

## Useful flags

| Flag | Purpose |
|---|---|
| `--max-tasks N` | Stop after N tasks. |
| `--manifest file.yaml` | Use a harness that is not in the registry. |
| `--sandbox docker` | Choose the sandbox. `none` turns it off, and must be asked for by name. |
| `--heartbeat 15s` | How often to save work and renew the lease. |
| `--url` | The server. By default the project's own, found from `.swarm`: the driver waits for it if it is not running yet, and finds it again after a restart, a standby taking over, or a move to a new port. |
| `--on-limit wait` | What to do when the harness runs out of usage: `wait` for the reset it reported, or `exit`. |

## A custom harness

```yaml title="my-harness.yaml"
name: my-harness
command: [my-harness, --acp]
pass_env: [MY_API_KEY]
real_home: true
write_paths: ["~/.my-harness"]
denied_tools: [fetch]
```

## When a harness cannot work

A missing login or a failed launch hands the task back with no penalty and stops that driver. It does not use up the task's attempts or move it to a pricier tier.

Running out of plan usage, credit or rate allowance is handled the same way, and the swarm is told. The driver reads it from the harness's errors and stderr: "usage limit reached", "credit balance is too low", `insufficient_quota`, a 429, and the like. It also reads when the limit resets, if the harness said: a Unix time, an RFC 3339 time, or "try again in 2h 30m". It hands the task back, reports the limit, and by default sleeps until the reset, then carries on. Meanwhile the swarm answers that agent's waits with exit, and `aiculator sessions` shows it as limited, with when. The agent's own text is held to a stricter test, so that a task about rate limiting or billing doesn't read as the harness running out.

## Which harnesses have been run

Claude Code and OpenCode have run this way for real. The others are configured from their documentation or the ACP registry and are untested. To test one:

```sh
./scripts/gate3_real.sh gemini 3
```
