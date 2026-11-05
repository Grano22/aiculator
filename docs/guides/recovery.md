# Recovery and failover

The swarm keeps one writer per project and checks its own records each time it starts.
This page covers what happens after a crash, how to check a project's health, and how to
keep a second server ready to take over.

## Damaged records are repaired at startup

When the server starts, it checks every task record before accepting work. Two kinds of
damage are fixed:

- **A record that can't be read.** The original bytes go to quarantine. In its place is a
  parked task titled *Unreadable record, kept in quarantine*, so that the problem shows
  up in `aiculator top` and isn't silently dropped.
- **A record that breaks a rule.** For example, a task that is claimed without a lease, or
  in review with nobody who submitted it. It is moved to the nearest safe state, usually
  back to Ready. A task marked Done without an independent check is never left as Done.
  It is parked so that a person decides. The original record is kept in quarantine
  before it is changed.

Repairs never lower a task's lease number, so a worker holding an old lease can't write
over the repaired record.

## `aiculator doctor`

```sh
aiculator doctor          # report; exits non-zero if something is wrong
aiculator doctor --fix    # also fix what can be fixed safely
aiculator doctor --json
```

With the server running, `doctor` reports:

- records repaired at the last start, and what is in quarantine
- leases more than a minute overdue (zombie workers)
- worktrees whose task is gone, done or canceled
- how far the event log lags behind the store
- how many tasks are parked

`--fix` removes the orphan worktrees. With the server stopped, `doctor` still checks the
files in `.swarm`: a stale `pid`, an `addr` that points at nothing or at another project's
server, and whether the lock is free. `--fix` removes the stale files but never the data.

## A standby server

```sh
aiculator up --standby
```

This starts the server if it isn't running, and then a second one that waits. If the
first one stops or dies, the standby takes over on the same address, normally within a
second, and open sessions reconnect to it. Leases survive the switch: no lease expires
in the first seconds after a takeover (`--restart-grace`), so workers have time to
reconnect.

`aiculator down` stops the standby first and then the leader, so a clean stop doesn't
hand over. `/v1/healthz` reports a `term` that goes up by one each time a server becomes
leader, so a client can tell that a takeover happened.

### Why the lock decides who leads

The leader is whichever server holds the lock on `.swarm`, not whichever one answers
health checks. A leader can pause without dying: a stopped terminal, a debugger, heavy
swapping or a slow disk. A paused leader doesn't answer, so a standby that took over on
a missed health check would be writing while the old leader still believes it leads.
When the old leader wakes, there are two writers. The kernel only frees the lock when
the process has exited, so with the lock that can't happen.

`spec/tla/Election.tla` models both versions. The lock version keeps at most one writer
and always ends up with a leader. The health-check version (`ElectionBug_health.cfg`)
fails in five steps: lead, pause, take over, wake, two writers.

!!! warning "One machine only"
    Both servers must run on the same machine against a local directory. File locks
    on network filesystems such as NFS or SMB aren't reliable enough to decide who
    writes.
