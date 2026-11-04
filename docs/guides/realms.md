# Realms: many projects on one machine

A **realm** is one project with its own server and its own store. A project becomes a realm when its server starts, and is named after its folder.

```sh
aiculator realm ls                     # Every project, and which are running
aiculator top --all                    # All of them, live in the terminal
aiculator web --all                    # All of them, in the browser
aiculator --realm shop task ls         # Act on one from any folder
aiculator realm rename billing-api billing
aiculator realm forget old-project     # Takes it off the list; its data stays
```

## What you see

`aiculator top --all` has three views, which you switch with `1` to `3` or Tab:

| View | Shows |
|---|---|
| Realms | Each project: running or not, tasks ready, being worked on, in review, done and parked, and agents working |
| Tasks | Tasks in progress or needing attention, with their project and owner |
| Agents | Each agent, with its project and current task |

Use `--once` to print one frame, `--view tasks` to choose the view, and `--json` for the raw data.

`aiculator web --all` shows the same in a browser, and each running realm links to its own full dashboard. It runs for as long as the command does, on `127.0.0.1`, behind a token in the link it prints.

A single project's `aiculator top` also names its realm.

## How projects stay apart

Realms are not merged into one server. That would make one crash, or one damaged store, every project's problem.

| What could go wrong | What stops it |
|---|---|
| A repository inside another uses the outer one's server | The search for a project's data stops at the edge of the repository |
| A stray `~/.swarm` claims every folder below it | The search also stops at your home directory |
| After a crash, a project's old address now reaches another project's server | Each server says which it is, and the client checks |
| That old port is still taken when the project restarts | The server picks a free port instead of failing |
| A token from one project is used on another | Each project signs with its own key, so it is refused |
| Two servers start on one project | The data directory is locked |
| Servers starting together damage the list of realms | Each realm is its own file, written atomically |
| A stuck server freezes the view of the others | Each is read separately, with a 2-second limit |

The list in `~/.aiculator/realms` holds names and paths and no tokens. Sandboxed checks can't read it.

## Agents and realms

A session belongs to the realm of the folder it started in, and can't see the others. Every task it gets says which project the work belongs to, in `realm` and `realm_path`. A session that started in the wrong place can notice that.

To pin a session to one project wherever it starts, add `--realm` to its MCP command:

```sh
aiculator --realm shop mcp --auto --tier 1
```

### Letting a session move between projects

This is off by default, because it gives a session work in folders you may not have opened it for. To allow it, add `--realm-switch` to the MCP command:

```sh
aiculator mcp --auto --tier 1 --realm-switch
```

The session then has two more tools: `realm_list` and `realm_use`. `realm_use` is refused while the session holds a task, because a lease belongs to one realm. It has to submit, fail or release the task first.

!!! warning "A joined session runs under its harness's permissions"
    Moving to a realm doesn't change what the session may do, or where its harness lets it write. Check that its permissions cover the other project before you allow this.

## Limits

- **The views read, and don't change anything.** To act on a realm, use `--realm NAME`.
- **The tasks view shows work in progress and work needing attention.** Queued and finished tasks are counted in the realms view.
- **One machine only.** The list is local, and remote servers aren't included.
- **Totals reset per project.** Rates are per server, so there is no combined history across restarts.
- **`realm_list` shows a project as running if it has an address file**, which can be out of date after a crash. `aiculator realm ls` checks properly.
