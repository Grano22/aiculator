# Debugging

Aiculator's logging works like Kubernetes: `-v=0` to `-v=9`, with `--vmodule` to raise one component on its own. If you know `kubectl -v=8`, you already know how to use this.

```sh
aiculator task ls -v=6                  # See the HTTP calls this command makes
aiculator up --v=4                      # Start a server with debug logging
aiculator debug verbosity 5 --for 10m   # Trace a running server for ten minutes
aiculator logs -f --component engine    # Follow what it is doing
```

## Levels

The levels mean what [kubectl's](https://kubernetes.io/docs/reference/kubectl/quick-reference/#kubectl-output-verbosity-and-debugging) mean. `aiculator debug levels` prints this list.

| Level | kubectl's meaning | What it shows here |
|---|---|---|
| **0** | Always visible to an operator | Errors, and verbosity changes |
| **1** | A reasonable default | Server start, serious warnings such as a lost planner or a stale revision |
| **2** | Steady state; recommended | **A server's default.** Task transitions, plans, MCP tool calls, sessions, merges |
| **3** | Changes in detail | Every commit and grant, rejected commands and why, permission decisions, checks starting |
| **4** | Debug | Why an agent was told to wait or exit, worktrees, check output, sandbox commands |
| **5** | Trace | Every git command, why a task was not eligible, compare-and-swap revisions |
| **6** | Requested resources | Method, URL, status and time of each HTTP call |
| **7** | HTTP headers | Also headers, with credentials removed |
| **8** | HTTP contents | Also bodies, MCP arguments and agent text, cut to 1 KiB, with credentials removed |
| **9** | HTTP contents, untruncated | Also bodies up to 1 MiB, with credentials removed |

A command defaults to 0 and a server to 2. Precedence is the flag, then `AICULATOR_V`, then the default. All these forms work: `-v=6`, `-v 6`, `--v=6` and `--v 6`.

Logs go to stderr, so stdout stays clean for scripts, even at level 9.

## What it looks like

At level 5 on a server, one task being granted:

```text
level=INFO msg="task created" v=2 component=engine task=fix-login title="Fix the login redirect" tier=1 plan_epoch=0
level=INFO msg=commit v=3 component=engine task=fix-login kind=publish actor=system:plan took=65.542µs
level=INFO msg=compare-and-swap v=5 component=engine task=fix-login from_rev=1 to_rev=2 lease=0
level=INFO msg="task transition" v=2 component=engine task=fix-login from=draft to=ready tier=1 attempt=0 actor=system:plan reason=""
level=INFO msg=commit v=3 component=engine task=fix-login kind=grant actor=worker1 took=59.333µs
level=INFO msg=compare-and-swap v=5 component=engine task=fix-login from_rev=2 to_rev=3 lease=1
level=INFO msg="task transition" v=2 component=engine task=fix-login from=ready to=claimed tier=1 attempt=1 actor=worker1 reason=""
```

At level 6 on a command:

```text
level=INFO msg=request v=6 component=client method=GET url=http://127.0.0.1:7777/v1/tasks status=200 took=752.833µs bytes=412
```

At level 8, asking for a token. The caller gets the token on stdout, and the log shows none of it:

```text
level=INFO msg="request headers" v=7 component=client method=POST path=/v1/tokens headers="Authorization: [redacted]; Content-Type: application/json"
level=INFO msg="request body" v=8 component=client method=POST path=/v1/tokens body="{\"agent\":\"demo\",\"tier\":1}"
level=INFO msg=request v=6 component=client method=POST url=http://127.0.0.1:7777/v1/tokens status=200 took=736.75µs bytes=134
level=INFO msg="response headers" v=7 component=client path=/v1/tokens headers="Content-Length: 134; Content-Type: application/json; Date: ..."
level=INFO msg="response body" v=8 component=client path=/v1/tokens status=200 body="{\"token\":\"[redacted]\"}"
```

## One component at a time

Raising everything to 5 is noisy. `--vmodule` sets a level per component, can use wildcards, and can also lower one:

```sh
aiculator up --vmodule engine=5,work=4
aiculator up --v=5 --vmodule http=0
```

The components are `engine`, `work`, `driver`, `swarm`, `sandbox`, `mcp`, `http`, `client` and `store`.

## On a running server

No restart is needed, and anything in progress carries on:

```sh
aiculator debug verbosity                        # Show the current setting
aiculator debug verbosity 5 --for 10m            # Trace, then go back by itself
aiculator debug verbosity --vmodule engine=5 --for 5m
aiculator debug verbosity 2                      # Change it for good
```

Use `--for`. It means a debugging session cannot leave a server logging heavily because someone forgot. Changing the level needs the admin token, and the change itself is logged.

## Reading the logs

`aiculator logs` reads the log of a server started with `aiculator up`:

```sh
aiculator logs --tail 50
aiculator logs -f --component engine
aiculator logs -f --grep fix-login
```

For other tools, `--log-format json` writes one JSON object per line, and `--log-file PATH` writes to a file:

```sh
aiculator logs -f | grep 'task transition'
aiculator up --log-format json --v=3
```

`aiculator top` never writes logs to the screen, because they would corrupt it. Use `--log-file` to capture them.

## Common problems

| Question | Try |
|---|---|
| Why does this task never start? | `aiculator debug verbosity --vmodule engine=5 --for 2m`, then look for `not eligible`. It names the tier, tags and file conflict. |
| Why does this agent keep waiting? | At level 4, `wait answered` gives the action and reason |
| Why was a command refused? | At level 3, `command rejected` gives the state and reason |
| Why did the checks fail? | At level 4, `check output` holds what the command printed |
| What is git doing? | At level 5, `git` lists each command and how long it took |
| Why was a tool call denied? | At level 3 on the driver, `permission decision` gives the tool and kind |
| What is this client sending? | `-v=8` on the command |

## What is safe to share

Credentials are removed where lines are written, so no call site can leak one by mistake. Tests read the logs at level 9 on both the client and the server, after real calls over HTTP and MCP, and fail if a token appears. Anything named like a credential is removed, as is anything shaped like a token or a bearer header, wherever it appears.

!!! warning "Levels 8 and 9 show content"
    Task titles, specs, notes and agent messages all appear. Credentials are removed, but your code and instructions may be in there, so read a level 8 or 9 log before sharing it.

!!! warning "The log file is not rotated"
    `.swarm/server.log` grows as long as the server runs. Level 9 fills it quickly, which is another reason to use `--for`.

A disabled log call costs about 2 ns and allocates nothing, so debug logging can stay in the hottest paths.
