# Verification

Run the whole set with:

```sh
go test -race ./...     # Includes the crash test
./scripts/formal.sh     # TLA+ and Alloy; needs Java
```

## Crash testing

`TestKill9Gate` starts a real server process, hammers it from several clients, and kills it with `SIGKILL` at a random moment, 100 times. After every restart it checks that:

- Every stored task is valid.
- Nothing a client was told succeeded has been lost.
- No lease number was ever granted twice.

!!! note "Power loss is a separate question"
    `kill -9` does not test it, because written data survives in the page cache. Use `aiculator serve --sync-always` if you need that, at some cost in speed.

## Linearizability

Porcupine checks that concurrent operations behave like one sequential FIFO queue. A second test confirms the checker catches a deliberately broken history, so that a pass means something.

## Formal models

| Model | What it proves |
|---|---|
| `spec/tla/Swarm.tla` | Unique leases, fencing against zombies, and done always means independently verified, across crashes and lost replies |
| `spec/tla/SwarmLiveness.tla` | A task always ends done or parked, even with hostile agents, if an agent that keeps getting chances eventually finishes |
| `spec/tla/Planner.tla` | An agent told to exit never misses work from that plan |
| `spec/alloy/swarm.als` | Tier and tag eligibility, plan deadlocks, and file lease conflicts |

Models named `Bug` break a rule on purpose and must fail, which proves the checker can catch that defect. The planner model found a real bug: agents could exit on a hint of no work, and then the planner could add work anyway.

!!! warning "What these results do not say"
    They are bounded: they hold for the models at the sizes in each `.cfg` file and `check` command. Only the transition table is tied to the code automatically, by `TestSpecMatchesCode`. The other rules were written by hand from the code, and the tests have to cover that gap.

## Performance budgets

`go test -run TestPerf ./internal/engine ./cmd/aiculator` holds the code to these budgets, and CI runs it on every push. The cases are deliberately large, to expose work that grows badly with the number of tasks. Budgets leave room for a busy CI machine, so a failure means a real regression.

| What | Measured on an M-series Mac | Budget |
|---|---|---|
| Add a task and wake a waiting agent, p99 | 0.32 ms | 5 ms, or 50 ms in CI |
| One add-and-grant cycle | 55 µs, about 18,000 per second | 2 ms |
| One grant among 32 competing agents | 18 µs | 2 ms |
| Grant with 20,000 tasks waiting, p99 | 32 µs | 10 ms |
| Wake with 2,000 agents waiting, p99 | 0.78 ms | 25 ms |
| One `plan_add` with 5,000 drafts | 0.15 ms | 3 ms |
| Commit a 5,000-task plan | 44 ms | 1 s |
| Restart with 20,000 tasks | 0.31 s | 5 s |
| Dashboard snapshot with 20,000 tasks, p99 | 6 ms | 100 ms |
| Heap per task | 1.3 KiB | 16 KiB |
| CLI start | 9 ms | 200 ms |
| Server ready | 20 ms | 3 s |

These cover the swarm's own machinery. Most of a task's real time goes to the model and your checks.

## Real harness runs

| Script | What it proves |
|---|---|
| `scripts/gate2_claude.sh` | A real Claude Code session finishes 10 tasks over MCP, with no call past 45 seconds |
| `scripts/gate3_real.sh ID` | A real harness finishes tasks through ACP, inside the sandbox |
| `ROUTE=mcp scripts/gate_join.sh` | A real session joins using only what `join` wrote |
| `ROUTE=shell scripts/gate_join.sh` | The same, for a session already open |

These use your harness accounts. `gate_join.sh` fails if a run finishes through a different route than the one being tested, because that mistake once hid a real bug.
