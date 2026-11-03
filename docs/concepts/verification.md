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

## Real harness runs

| Script | What it proves |
|---|---|
| `scripts/gate2_claude.sh` | A real Claude Code session finishes 10 tasks over MCP, with no call past 45 seconds |
| `scripts/gate3_real.sh ID` | A real harness finishes tasks through ACP, inside the sandbox |
| `ROUTE=mcp scripts/gate_join.sh` | A real session joins using only what `join` wrote |
| `ROUTE=shell scripts/gate_join.sh` | The same, for a session already open |

These use your harness accounts. `gate_join.sh` fails if a run finishes through a different route than the one being tested, because that mistake once hid a real bug.
