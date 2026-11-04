# Connectors: import and sync

Tasks can come from other platforms as well as from harnesses. There are two features, and you can use either alone:

- **`aiculator import`** loads items once, each time you run it.
- **The synchronizer** keeps them in step afterwards, in both directions.

| Source | `kind` | Import | Status back | Changes in | Live changes |
|---|---|---|---|---|---|
| Jira Cloud | `jira` | JQL | Yes | Polling | Webhook |
| Notion | `notion` | Filter | Yes | Polling | Webhook |
| Excel `.xlsx` | `xlsx` | Whole sheet or regions | No | Re-reads the file | No |
| CSV | `csv` | Whole file | No | Re-reads the file | No |

!!! warning "Tested against documented behaviour, not live accounts"
    The Jira and Notion connectors are tested against local servers written from each vendor's API documentation, and `.xlsx` against files written by openpyxl. They have not yet run against a real Jira site, a real Notion workspace or a file saved by Excel. Start with `aiculator source check` and `--dry-run`.

## Configure a source

Sources live in `.agents/sources.yaml`, which is safe to commit because it holds no secrets. Credentials are named by environment variable, and the file is refused if it contains a value instead.

=== "Jira"

    ```yaml
    sources:
      - name: jira
        kind: jira
        url: https://acme.atlassian.net
        auth: {email_env: JIRA_EMAIL, token_env: JIRA_TOKEN}
        query: project = PROJ AND status = "Ready for agent"
        fields:
          title: summary
          spec: description
          priority:
            from: priority.name
            map: {Highest: 100, High: 50, Medium: 10, Low: 1}
          labels:
            team: customfield_10010
          props:
            story_points: customfield_10016
        defaults:
          tier: 1
          checks: ["go test ./..."]
          require_review: true
          aging: [1h=5, 4h=20]
    ```

=== "Notion"

    ```yaml
    sources:
      - name: roadmap
        kind: notion
        data_source: 1f2e3d4c-0000-0000-0000-000000000000
        auth: {token_env: NOTION_TOKEN}
        filter:
          property: Status
          status: {equals: Ready}
        fetch_body: true          # Costs one more request per page
        fields:
          title: Name
          spec: body
          status: Status
          tags: Skills
          priority:
            from: Priority
            map: {P0: 100, P1: 50, P2: 10}
    ```

=== "Eisenhower table"

    One row per task, with a column for each axis.

    ```yaml
    sources:
      - name: matrix
        kind: xlsx
        path: eisenhower.xlsx
        sheet: Tasks
        fields:
          key: ID
          title: Task
          spec: Notes
          priority:
            rules:
              - when: {Urgent: "yes", Important: "yes"}
                value: 100
              - when: {Important: "yes"}
                value: 50
              - when: {Urgent: "yes"}
                value: 25
            default: 1
          labels:
            quadrant:
              rules:
                - when: {Urgent: "yes", Important: "yes"}
                  value: do
                - when: {Important: "yes"}
                  value: schedule
                - when: {Urgent: "yes"}
                  value: delegate
              default: delete
          props:
            due: Due
    ```

=== "Eisenhower grid"

    One column per quadrant, with `regions` saying which cells are which.

    ```yaml
    sources:
      - name: grid
        kind: xlsx
        path: matrix.xlsx
        regions:
          - {name: do, range: "A3:A40"}
          - {name: schedule, range: "B3:B40"}
          - {name: delegate, range: "C3:C40"}
          - {name: delete, range: "D3:D40"}
        fields:
          title: value
          priority:
            from: region
            map: {do: 100, schedule: 50, delegate: 25, delete: 1}
          labels:
            quadrant: region
    ```

### Field mapping

| Field | Sets |
|---|---|
| `title` | The task's title. Required |
| `spec` | Its instructions |
| `priority`, `tier` | Numbers |
| `tags` | Required capability tags |
| `labels` | Named labels |
| `props` | Named properties |
| `key` | What identifies a row in a file. Without it, the title does |
| `status` | Which field holds the status, when it isn't the system's own |

Each can be a field name, or an object with `from`, `map`, `rules` and `default`. Names ignore case, and `priority.name` follows a nested field. Rules run in order, and the first whose `when` matches gives the value.

`skip_when` leaves items out, such as `skip_when: [{Status: Done}]`.

!!! danger "Outside data cannot choose what runs"
    `checks`, `touches`, `budget`, `require_review` and `allow_empty` can only come from `defaults`, in this reviewed file. No mapping can set them, because checks are commands the swarm runs, and anyone who can edit a ticket would otherwise be able to choose them. A config that tries is refused. `spec/alloy/swarm.als` models this rule, and a test ties that model to the code.

## Import

```sh
aiculator source check jira            # Are the settings and credentials right?
aiculator import jira --dry-run        # What would happen? Changes nothing
aiculator import jira
aiculator import jira --query 'key = PROJ-1'
aiculator import jira --update         # Bring earlier imports up to date
```

A task's ID comes from the item's key, such as `jira-PROJ-123`, so importing again is safe. Items already imported are left alone, and `--update` changes only what they say, never their progress.

Jira attachments and Notion files become foreign resources, which are only downloaded when asked for. See [Labels, priority and attachments](task-data.md).

Dependencies come across too. In Jira, a `Blocks` link makes the blocked issue wait, and tasks are created in dependency order. A cycle is reported and left out. If an item is blocked by something the import did not include, the task comes in without that dependency, because it would otherwise wait forever. The import warns you, and the task's `unresolved_deps` property records it.

!!! warning "Export your credentials before `aiculator up`"
    The import reads credentials from your shell. The synchronizer and attachment downloads run in the server, which reads them from the environment it was started in.

## Sync

Add a `sync` section, and the server keeps that source in step:

```yaml
    sync:
      direction: both                # both, push or pull
      push:
        mode: realtime               # realtime, or interval with every: 1m
        updates: partial             # partial sends each change, last sends only the end
      pull:
        mode: interval               # once, interval or webhook
        every: 5m
      create: false                  # Also create tasks for new items
      status:                        # Which remote status each state becomes
        claimed: In Progress
        running: In Progress
        review: In Review
        done: Done
        canceled: Cancelled
      on_remote:                     # What a remote status does here
        Cancelled: cancel
```

A state left out of `status` is not sent.

| You want | Set |
|---|---|
| Status on the other side within milliseconds | `push.mode: realtime` |
| Fewer calls, sent in batches | `push.mode: interval`, with `every` |
| Only the final result | `push.updates: last` |
| Each change along the way | `push.updates: partial` |
| One read when the server starts | `pull.mode: once` |
| A read every so often | `pull.mode: interval`, with `every` |
| Changes as they happen | `pull.mode: webhook` |

Check how it's going:

```sh
aiculator sync status
```

### Who wins

Each side owns different things, so there is nothing to merge:

| What | Owner |
|---|---|
| Title, description, labels and priority | The other system |
| How far along a task is | The swarm |
| Stopping a task | Either, through `on_remote` |

If someone moves a remote status to something not listed in `on_remote`, the swarm doesn't fight them, and its next state change sets the status again. The only action is `cancel`, because a remote status must not be able to mark work as done without verification.

Before each push the swarm reads the item, so that it doesn't overwrite a cancel it hasn't seen yet. A very small window remains between that read and the write.

### Webhooks

Webhooks need the server to be reachable from the internet, and it listens on `127.0.0.1` by default, so you need a tunnel or a reverse proxy in front of it. The address is `POST /v1/hooks/SOURCE`.

Every delivery must carry a valid signature, checked against the raw bytes before anything in it is believed. A delivery only says which item changed, and the swarm then reads that item once. A read also runs at each start, which catches what was missed.

=== "Jira"

    Register the webhook with a secret, and set the same secret here:

    ```yaml
    sync:
      hook_secret_env: JIRA_HOOK_SECRET
      pull: {mode: webhook}
    ```

=== "Notion"

    1. Set `hook_secret_env`, but leave the variable unset for now.
    2. Create the subscription in Notion. It sends a one-time token to the server.
    3. Read it with `aiculator sync hook-token SOURCE`, and paste it into Notion's form.
    4. Set the variable to that token, and restart the server.

## How sync stays out of the way

The synchronizer is a side effect. It reads the commit stream through a durable consumer, on its own goroutines, after each commit.

- **A slow or dead remote can't slow a claim.** Tests stop the remote entirely and measure claims.
- **Nothing is lost.** A change that wasn't sent is delivered again after a restart, soon after a crash and at once after a clean stop.
- **Bursts cost one call.** Newer changes replace older ones that haven't been sent, so the last state wins.
- **Changes don't echo.** The link records the last status either side saw, and a status the remote already has is never sent.
- **Rate limits are respected.** `Retry-After` is honoured, pushes run four at a time, and failures back off for up to 5 minutes.
- **One bad source stays contained.** A source with missing credentials shows up in `sync status`, and the rest carry on.

`spec/tla/Sync.tla` checks that sync always settles, that the two sides agree once it has, that a cancel the swarm has seen stops the task, and that no change echoes, including across crashes.

## Measured

On an Apple Silicon laptop, with the remote's own time left out:

| Measure | Result |
|---|---|
| Commit to remote call, realtime | p50 2.4 ms, p99 3.5 ms |
| Claim p50, without and with sync | 20 µs and 27 µs |
| Import | About 10,000 tasks per second |
| Claim p99 during an import, with 20,000 tasks waiting | 1.3 ms |

`internal/connect/perf_test.go` and `internal/swarm/perf_test.go` hold these to a budget.

## Limits

- **Comments aren't synced.** Only status goes back.
- **A deleted item isn't noticed.** Its task carries on.
- **A Notion-hosted file's link expires after about an hour.** Fetch it soon after importing, or import again with `--update`.
- **Spreadsheets are read-only**, and an `.xlsx` formula gives its last saved result.
- **Sync settings load at start.** Restart the server after changing them.
