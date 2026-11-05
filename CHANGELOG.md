# Changelog

All notable changes are recorded here. The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and versions follow [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

Add changes under **Unreleased** as you make them. `scripts/release.sh` moves them under the new version, and that section becomes the GitHub release notes.

## [Unreleased]

### Added

- **Architecture and path tests:** import rules between packages are checked in tests, the core is kept free of clocks, randomness and I/O, and every transition in the TLA+ spec is reached through the real state machine. A coverage gate counts the binary that the end-to-end tests run, and holds each package to a minimum.
- **Repair at startup:** a task record that can't be read, or that breaks a rule, is quarantined and replaced by a safe version. A record that can't be read becomes a parked stand-in, and a Done task without an independent check is parked for a person to decide.
- **`aiculator doctor`:** reports repaired and quarantined records, overdue leases, orphan worktrees, event log lag and stale `pid` or `addr` files, with or without a server running. `--fix` cleans up what is safe to clean up.
- **Backups of chosen tasks:** rules in `.agents/backups.yaml` pick tasks by id pattern, label, tag, state or priority, or all of them, and keep a journal of each change, snapshots at an interval, or both, with retention by count and age. `aiculator backup versions` and `backup restore` put a copy back safely: in-flight work goes back to Ready, the lease moves on, and the replaced record is quarantined.
- **Standby server:** `aiculator up --standby` keeps a second server waiting, which takes over on the same address if the first one dies. The leader is whoever holds the data directory lock, which `spec/tla/Election.tla` checks against an election based on health checks. `/v1/healthz` reports the leadership `term`.

### Changed

- **Faster heartbeats:** a heartbeat that only extends a lease is kept in memory while the stored deadline is more than half a lease away, which takes it from 19µs to 1.5µs in the engine. Verified tokens are remembered, so checking one costs 35ns instead of 32µs. Together, a heartbeat over HTTP went from 105µs to 49µs, with a third fewer allocations, and followers such as sync and backups read far fewer revisions. Task records are encoded from a pointer, which cuts the bytes allocated per commit by 10%.

## [0.6.0] - 2026-11-05

### Added

- **`aiculator export`:** writes tasks, agents, usage, flow or realms as JSON, YAML, CSV, TSV, Excel, HTML or Markdown, with `--fields`, `--state`, `--label`, `--where`, `--sort`, `--limit`, `--with-meta` and `--all`. Values are always treated as text, so an export can't run a formula or a script from a ticket's title.
- **Parallel subagents in one worktree:** `aiculator parallel plan` works out which jobs can safely run at the same time, at directory or file level, and `parallel verify` checks with git that a wave changed only what it claimed. It comes with a `swarm-parallel` skill, a `parallel-subagents` template and a `<ParallelWork>` component.
- **`FUNDING.yml`:** a Sponsor button on the repository, for Ko-fi and Buy Me a Coffee.

## [0.5.1] - 2026-11-05

### Added

- **Realms:** every project becomes a named realm when its server starts. `aiculator realm ls`, `aiculator top --all` and `aiculator web --all` show all projects, with their tasks and agents, and `--realm NAME` acts on one from any folder. Each keeps its own server and store. Sessions stay in one realm unless started with `--realm-switch`.
- **`aiculator init`:** sets up a machine once, asking which installed harnesses to use, whether to install the skills, and whether to add MCP config. It writes nothing until every answer is in, and `--yes` takes the defaults for a script. `--scoped` sets up one project instead, and can also make its git repository and start the server.
- **`aiculator join` reuses what exists:** skills and MCP config a harness can already find are left alone, at either level, so after `init` it only has to start the server. On its own, it works as before.
- **The MCP bridge starts anywhere:** `mcp --auto` now looks for a server when a tool is called. A config for every project no longer shows as a broken server in folders with no swarm, and a server started later is found with no restart.
- **Labels and aging steps:** tasks can carry labels, and priority can jump after a wait, per task or across the server, with `--aging 1h=5,4h=20`. `aiculator task amend` changes what a task says without touching its progress.
- **Properties, blobs and foreign resources:** tasks can carry structured data, files, and references to things elsewhere, which are fetched once and kept. These live outside the task record, so they add nothing to a commit.
- **Connectors:** `aiculator import` loads tasks once from Jira, Notion, Excel or CSV, with configurable field mapping, including rules for an Eisenhower matrix. Tested against local servers built from the vendors' API docs, not yet against live accounts.
- **Synchronizer:** keeps a source in step in both directions, by realtime or interval push, with each change or only the last, and by a single read, polling or signed webhooks. It runs beside the core, so a slow remote can't slow a claim. `spec/tla/Sync.tla` models it.

### Changed

- A heartbeat with 500 agents waiting is about 6 times faster, because it no longer re-matches every waiter, and `core.Apply` no longer copies each list.

### Fixed

- A repository nested inside another no longer uses the outer one's server. Tasks added there went to the wrong project, and agents would have worked in the wrong repository. A stray `.swarm` in the home directory no longer claims every folder below it.
- A project no longer takes another project's server for its own when its old address reaches one after a crash, and no longer fails to start when its old port is taken.
- Replaying the event log could miss the newest events on a busy machine, because the wait for it to catch up could finish too early.
- A clean stop of the synchronizer could leave an unsent change stuck for 15 seconds after a restart, because it could be handed back to the worker that was shutting down.

## [0.5.0] - 2026-11-04

This tag failed its checks before anything was built, so it was never published. Its changes are in 0.5.1.

## [0.4.0] - 2026-11-03

### Added

- **Package manager publishing:** a release now installs and tests packages for Homebrew, Scoop, Chocolatey, WinGet, the Arch User Repository, and signed apt, dnf and apk repositories. It publishes each one whose secrets are set, and skips the rest.
- **Templates for rich output:** new components `<TodoList>`, `<PlanMode>`, `<AskConfirm>`, `<AskText>`, `<AskPreview>`, `<Render>` and `<RichMarkdown>`, with templates `ui-confirm`, `ui-text`, `ui-preview`, `ui-todos`, `ui-plan` and `rich-markdown`. They name each harness's real tool, and `rich-markdown` covers 19 Markdown forms, with terminal fallbacks for those a terminal may not show.
- **Template demo:** `.agent/template/demo.mdx` uses every template feature in one realistic workflow, with a docs page that shows its real source and rendered output.

### Changed

- `.gitignore` now covers Go, operating system, editor and secret files, and `.DS_Store` is no longer tracked.

### Fixed

- `aiculator down` now waits for the server process to exit. It used to return when the health endpoint closed, while the store could still be writing to `.swarm`.
- A second server on the same data directory is now refused. It used to start, and two writers could share one store.
- The Docker sandbox clears the image's entrypoint, so commands run as given. The default `alpine/git` image would otherwise pass them to `git`. Custom images that rely on an entrypoint for setup no longer run it.

## [0.3.0] - 2026-11-03

### Added

- **Verbosity:** `-v=0` to `-v=9` as in kubectl, `--vmodule` per component, `--log-format json` and `--log-file`. `aiculator debug verbosity` changes a running server, and can go back by itself after `--for`. `aiculator logs` reads and follows the log of a server started with `aiculator up`. Credentials never reach a log at any level.
- **Question templates:** `ui-select`, `ui-checkbox` and `ui-tabs` make a model ask through a selectable list, checkboxes or a tabbed form. They name each harness's real tool where its docs confirm one, fall back to a numbered list otherwise, and cover subagents, which cannot reach the user.
- **Flow templates:** `subagent-flow` runs staged work with a checked result at each stage, and `living-section` keeps a status section in a project file current.
- **Components:** `AskOne`, `AskMany`, `AskTabs`, `Tab`, `SubagentFlow`, `Stage` and `DynamicSection`. A re-render keeps what a model wrote in a dynamic section, and `template --check` does not count it as drift.
- **Real-model gate:** `scripts/gate_ui_templates.sh` tests the templates on a real Claude Code session.
- **Debugging guide:** new in the docs, with real output from each level.

## [0.2.0] - 2026-11-03

Live dashboards, plugins, and a module path that `go install` can use.

### Added

- **Terminal dashboard:** `aiculator top` shows flow, throughput, latency with jitter, health probes, task and agent liveness, usage and budgets.
- **Web dashboard:** `aiculator web` opens the same data in a browser, embedded in the binary, with a read-only link that expires.
- **Plugins:** compile features in through `pkg/plugin`, or put a program named `aiculator-NAME` on `PATH`. The web dashboard is a plugin, and `-tags noweb` leaves it out.
- **Metrics:** `/metrics` for Prometheus, `/v1/observe` for a JSON snapshot, `/v1/readyz` for readiness, and `aiculator events` for a live stream of task changes.
- **Usage:** tokens are tracked per task, tier and agent, including what harnesses report over ACP.
- **Viewer tokens:** read-only tokens for dashboards and scrapers.
- **Showcase:** the docs now open with examples and screenshots, regenerated by `scripts/screenshots.sh`.

### Changed

- The Go module path is now `github.com/Grano22/aiculator`, so `go install` and outside plugins work.
- The `cost` and `tokens` arguments on tools and commands now mean usage since the last report.

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
- **Budgets:** performance tests with limits on latency, throughput, scaling, memory and startup, and a package size budget enforced by every build.
- **License:** released under the MIT License.
- **Automation:** CI, CodeQL, Dependabot, Snyk, release builds for 8 platforms, and versioned docs.
