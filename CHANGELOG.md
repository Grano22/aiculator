# Changelog

All notable changes are recorded here. The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and versions follow [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

Add changes under **Unreleased** as you make them. `scripts/release.sh` moves them under the new version, and that section becomes the GitHub release notes.

## [Unreleased]

## [0.1.0] - 2026-11-03

First version: a swarm that runs coding tasks across harnesses and verifies the results itself.

### Added

- **Core:** task state machine, embedded NATS JetStream store, single-writer dispatcher, leases with fencing, zombie detection and a derived event log.
- **Tiers:** standard, advanced and ultra, with escalation after failures, stealing after a wait, and parking for a human.
- **Planner signal:** agents are told wait or exit, with the planner's phase and expected tasks per tier.
- **Work layer:** a git worktree per task, saved work under `refs/swarm/`, handoff between attempts, machine checks in a clean checkout, and a merge queue onto `swarm/integration`.
- **Sandbox:** Seatbelt, bubblewrap and Docker, with secrets hidden from sandboxed commands.
- **Channels:** an HTTP API, MCP tools, an ACP driver, and the `aiculator work` shell route.
- **Joining:** `aiculator join` connects harnesses, including sessions that are already open.
- **Harnesses:** a registry of 24, of which Claude Code and OpenCode have been run end to end.
- **Skills:** `aiculator skill` manages skills in `.agents/skills`, with wildcard install, a spec linter and three bundled skills.
- **Templates:** `aiculator template` renders Jinja2 and MDX-style templates into skills, with components for subagent rules, retries, output formats and flows.
- **MCP config:** `aiculator mcp add`, `print` and `remove`, for any harness or config file, and for custom servers.
- **Verification:** TLA+ and Alloy models, Porcupine, fuzzing, and a 100-crash `kill -9` test.
- **License:** released under the MIT License.
- **Automation:** CI, CodeQL, Dependabot, Snyk, release builds for 8 platforms, and versioned docs.
