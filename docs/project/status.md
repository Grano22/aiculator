# Status

## Built

- Core, tiers, the planner signal and the work layer
- MCP, shell and ACP routes, with joining from an open session
- Skills, templates and MCP config commands
- Sandboxing, agent tokens and secret hiding
- Crash tests, formal models and fuzzing
- CI, releases and docs

## Tested on real harnesses

| Harness | Shell | MCP | ACP |
|---|---|---|---|
| Claude Code | Yes | Yes | Yes |
| OpenCode | Yes | Yes | Yes |
| All others | No | No | No |

The other harnesses are configured from their docs or a community table. Use `./scripts/gate3_real.sh ID` to test one.

## Not built yet

- Mailboxes and the memory board
- The dashboard and OpenTelemetry traces
- Thompson sampling, budgets per plan, and the circuit breaker
- Automatic re-splitting of failed tasks by a planner
- Mutation testing and network chaos with toxiproxy
- The A2A protocol and cluster mode

## Not yet tried

- Windows and FreeBSD builds compile, but have not been run.
- bubblewrap and Docker sandboxes have had only their arguments checked.
- The GitHub workflows pass a linter and their scripts are tested, but they have not run on GitHub.

## Roadmap

No phase starts until the previous one passes its gate.

| # | Phase | Gate |
|---|---|---|
| 1 | Core | Linearizability, 100 crashes, no double-claim |
| 2 | MCP | A real session finishes 10 tasks, with no call past 45 seconds |
| 3 | ACP drivers | Fake agents behave identically, and real agents meet a pass rate |
| 4 | Work layer | A zombie is caught, and a stalled task hands over with no work lost |
| 5 | Routing | Cost per verified task falls on a fixed benchmark |
| 6 | Operations | A live chaos demo heals itself |
| 7 | Distribution | Phase 1's tests pass on three nodes through a network split |
