# Swarm

One static Go binary that runs a queue of coding tasks across AI harnesses. It assigns each task to an agent of the right tier, isolates it in a git worktree and sandbox, verifies the result itself, and merges it.

An agent saying "done" counts for nothing: the swarm runs the acceptance checks.

## Quick start

```sh
CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o bin/swarm ./cmd/swarm

# Start the server against a repository.
bin/swarm serve --repo /path/to/repo

# In another terminal, from the same directory, add a task.
bin/swarm task add --title "Add a health endpoint" \
  --spec "Add GET /healthz returning 200." --check "go test ./..." --tier 1

# Run a harness through ACP. It only launches when there is work.
export SWARM_TOKEN=$(bin/swarm token --agent worker1 --tier 1)
bin/swarm drive --harness claude --agent worker1

bin/swarm task ls
```

Finished work lands on the `swarm/integration` branch. Your own branches are never touched.

To attach a Claude Code session directly, point it at `http://127.0.0.1:7777/mcp` with `Authorization: Bearer <token>`. `scripts/gate2_claude.sh` shows a complete example.

## How it works

- **Commit path:** one goroutine owns all state. Each command passes the pure state machine in `internal/core`, then one compare-and-swap write to KV, and only then wakes the waiter. The event log is derived from the KV stream, so a crash cannot leave the two disagreeing.
- **Dispatch:** `wait_for_task` returns a task already leased to the agent, so there is no claim race. Agents get `task`, `wait` with a reason, or `exit`.
- **Tiers:** a task goes to agents of its own tier. Higher tiers take it only after a set wait. Two failures raise the tier, and after the top tier the task is parked for a human.
- **Planner signal:** the planner takes a lease before reading its input and publishes its phase and expected tasks per tier. A plan becomes visible all at once when it commits. If the planner dies, the lease expires and waiters see `planner_lost`.
- **Zombies:** the swarm hashes the worktree itself. An agent with a live heartbeat and unchanged files loses its lease.
- **Handoff:** work is saved under `refs/swarm/<task>/`. A new attempt starts from there and first merges in whatever has landed since.
- **Harness faults:** a missing login or failed launch hands the task back with no penalty and stops that driver.

## Layout

| Path | Purpose |
|---|---|
| `internal/core` | Pure state machine and invariants |
| `internal/store` | Embedded NATS JetStream, KV and event log |
| `internal/engine` | Single writer, dispatcher, leases, planner lease |
| `internal/work` | Worktrees, saved work, checks, merge queue |
| `internal/sandbox` | Seatbelt, bubblewrap, Docker |
| `internal/auth` | ed25519 agent tokens |
| `internal/swarm` | Service that joins the engine and work layer |
| `internal/api` | HTTP and MCP endpoints |
| `internal/driver` | ACP driver and permission policy |
| `cmd/swarm` | The binary, and the `kill -9` gate test |
| `cmd/fakeagent` | ACP agent that is slow, crashes, goes zombie or lies |

## Tests

```sh
go test -race ./...                                 # Includes the 100-crash gate
go test ./internal/core -run=^$ -fuzz=FuzzApply -fuzztime=60s
go test ./internal/engine -run=^$ -bench=GrantCycle
./scripts/license_audit.sh                          # Regenerates THIRD_PARTY_NOTICES.md
./scripts/gate2_claude.sh                           # Uses your Claude account
./scripts/gate3_real.sh claude 3                    # Also gemini or codex; uses that account
```

## Licenses

`scripts/license_audit.sh` fails the build on GPL, AGPL, LGPL, BSL, SSPL or unknown licenses in anything linked into the binary. `THIRD_PARTY_NOTICES.md` carries the required license texts and NOTICE files, and must ship with the binary.

Harnesses and sandboxes run as separate programs and are not linked. Each has its own license and terms of use.

This project itself has no license file yet, so no one else has rights to use it until you choose one.
