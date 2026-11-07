# Waiting without spending tokens

A worker that waits for tasks through tool calls pays for every call: each one is a model turn, and each turn reads the whole conversation again. An MCP call can last only about 40 seconds before the harness cuts it off, so an idle MCP worker makes around 90 calls an hour that do nothing.

`aiculator work next` moves the waiting out of the model. A small helper process holds the connection to the swarm, and the model runs one command per task:

```sh
aiculator work join --tier auto --model claude-sonnet-5-5   # prints the agent name
aiculator work next --agent NAME                            # returns when there is a task
# ... the model does the task ...
aiculator work done submit --agent NAME --note "what was done"
aiculator work next --agent NAME                            # and so on
```

The `swarm-work` skill teaches exactly this loop. The older `swarm-join` skill stays for sessions that work through MCP tools alone.

## What it costs to wait

Each call to `work next` waits as long as the harness lets a command run, then returns with exit code 3 for the model to run it again. The number of idle turns follows from that limit:

| How the session waits | Idle turns an hour |
|---|---|
| MCP `wait_for_task`, held about 25 to 40 s | about 90 to 140 |
| `work next`, a harness with a 2-minute limit | about 30 |
| `work next`, a 10-minute limit | about 6 |
| `work next`, a 1-hour limit (`aiculator init` offers it for Claude Code) | 1 |
| `work next` run in the background, with a notice when it ends | 0 |

These follow from the limits; they are not a measurement of any one harness. A task always costs the turns of doing it, and the call that brought it.

## How long a call waits

`work next` learns the harness's limit, the same way MCP waits learn theirs:

- It starts at 1 minute 50 seconds, just under Claude Code's default of 2 minutes.
- A call allowed to run to its end makes the next one 2 minutes longer, up to 55 minutes.
- A call the harness cut off marks where its limit is, and later calls stay 5 seconds under it.
- `--wait` sets the limit by hand.

**Claude Code** stops a command after 2 minutes unless its settings allow more. `aiculator init` offers to set `BASH_DEFAULT_TIMEOUT_MS` and `BASH_MAX_TIMEOUT_MS` to an hour, in `~/.claude/settings.json`, or in the project's `.claude/settings.local.json` with `--scoped`. It keeps your other settings, never lowers a value already higher, and backs up the file first. These are Claude Code's own settings, and their behavior is up to Claude Code. The swarm doesn't depend on them: without them, `work next` simply returns more often. The skill also tells the model to pass the timeout on each call, and to run the command in the background where it can.

**Codex** takes a timeout on each command (`timeout_ms`), which the skill tells the model to pass.

## Nothing is lost to a cut-off

The helper, not the command, holds the connection and takes the task. If the harness kills `work next` just as a task arrives:

1. The helper keeps the task, and keeps its lease alive.
2. The next call returns it at once.
3. A task nobody comes back for within 3 minutes goes back to the queue, with no penalty.

## What the helper does while the model works

- **Keeps the lease:** it heartbeats, and saves progress every 2 minutes. The swarm still hashes the files itself, so a model that stops changing files is still caught as a zombie.
- **Reports usage:** it reads the tokens the session used from the harness's own log and reports them with progress and with `work done`, so the model never has to. It reads Claude Code's logs in `~/.claude/projects` and Codex's in `~/.codex/sessions`. It finds the right session by the agent's name in it, so several sessions in one folder are kept apart. Only token counts, the model, the effort and the plan's limits are read.
- **Reports limits:** when Codex's log shows the plan's limit used up, the helper tells the swarm when it resets, and `work next` says to stop until then.
- **Keeps to a budget:** `work next --token-budget 200000` gives the session a budget. The helper hands back, with no penalty, a task whose [estimate](task-data.md) is more than what is left, and says to stop.

The helper stops on its own after two idle hours. `aiculator work helper status --agent NAME` shows what it holds and has seen, and `work helper stop` stops it.

!!! note "Harness logs are undocumented"
    The log formats belong to the harnesses and can change in any release. A reader that cannot make sense of a log reports nothing rather than a guess, and usage then comes only from what the model reports.
