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
Read .agents/skills/swarm-join/SKILL.md and follow it.
```

The skill checks whether the session has the swarm's tools. If it does not, it uses `aiculator work` commands instead. Pi and goose always use this route.

## Choose a tier

The tier says what work a session takes: 1 standard, 2 advanced, 3 ultra.

- **Manual:** give it with the skill, such as `/swarm-join 2`, or in the line you paste ("Join as tier 2").
- **Asked:** with no tier given, the skill asks which one. Press Enter for auto.
- **Auto:** the swarm picks from the model and the effort or variant it runs at. The model's family sets the tier: Haiku, mini and flash models are 1; Sonnet, GPT-5, o3, GLM and Qwen Coder are 2; Opus, Fable, the pro models and Gemini Pro are 3. High, max or ultrathink effort moves it up one, and low or minimal moves it down one. So does a thinking budget of 16,000 tokens or more, or under 2,000. An unknown model gets tier 1, since taking easy work costs less than failing hard work.

```sh
aiculator work join --tier auto --model claude-sonnet-5-5 --effort high
# joined at tier 3 (ultra): auto: claude-sonnet-5-5 is a tier 2 model, high effort moves it to 3
```

The skill passes the model and effort it knows it runs as. What it leaves out comes from the harness's environment: `AICULATOR_MODEL`, `ANTHROPIC_MODEL`, `OPENAI_MODEL` and the like, and `AICULATOR_EFFORT`, `CLAUDE_CODE_EFFORT_LEVEL`, `OPENAI_REASONING_EFFORT` or `MAX_THINKING_TOKENS`. On the MCP route the skill calls `join_swarm` with the same choices; it is refused while the session holds a task.

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
| Report usage | `report_usage`, or `tokens` and `cost` on most calls | `aiculator work usage`, or `--tokens` and `--cost` |
| Say the harness ran out | `report_limit` | `aiculator work limit` |
| Estimate a task's tokens | `estimate_tokens` | `aiculator work estimate` |
| Choose the tier | `join_swarm` | `aiculator work join --tier` |

A session joined with `--planner` also gets `plan_begin`, `plan_update`, `plan_add` and `plan_commit`. See [Plan work](planning.md).

## What the answers mean

Each wait returns an `action`:

- **`task`:** you hold a lease on that task, and `workdir` is where to do it.
- **`wait`:** nothing yet, and `reason` says why: `planning`, `planner_lost`, `deps`, `tier_busy` or `idle`.
- **`exit`:** stop. No more work can reach this tier, an operator stopped the session (`stopped_by_operator`), or it reported a usage limit (`usage_limit`, with when it resets).

### Waits that cost fewer tokens

Each wait is a model turn, so a wait should be held as long as the harness lets a call run. Harnesses rarely document that limit, so the swarm learns it per session. A wait that ran to the end makes the next one 5 seconds longer, up to 40. A caller that hung up early marks where its limit is, and later waits stay 3 seconds under it. So leave `hold_seconds` and `--hold` unset. `next_hold_seconds` in each answer, and the HOLD column in `aiculator sessions`, show where a session has got to.

### Usage and limits

Report tokens and cost whenever the harness says what a turn cost, so budgets, the dashboards and estimates stay right. If the plan's usage, credit or rate limit runs out, report it with when it resets. What the session holds goes back with no penalty, and its waits answer exit until then, so it stops spending turns.

!!! warning "A joined session is not sandboxed"
    It runs under your harness's own permissions. In one test, a model started its own server in a temp directory on its own initiative. The acceptance checks still run in the sandbox, but for work you do not fully trust, use [sandboxed workers](workers.md).

!!! tip "OpenCode 2"
    A running OpenCode background service does not pick up new project config, and `opencode mcp list` shows the service's view. Use `opencode run --standalone` or restart the service. The shell route works either way.

## Undo

```sh
aiculator unjoin
```

This removes the swarm's MCP entries and skill links, and nothing else. Add `--purge` to delete the swarm's skills from `.agents/skills` too.
