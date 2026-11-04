# Labels, priority and attachments

A task can carry more than a title and a spec. These features are kept apart from the task's working state, so adding data never slows a claim.

| You want | Use | Where it lives |
|---|---|---|
| A small fact to filter or sort by | A label | On the task record, up to 64 labels of 256 bytes each |
| Priority that rises with waiting | Aging steps | On the task record, or the server's flags |
| Structured or larger data | A property | The metadata store, up to 256 KiB per task |
| A file | A blob | The blob store, up to 64 MiB each |
| Something that lives elsewhere | A foreign resource | A reference, fetched once when asked for |

## Labels

```sh
aiculator task add --title "Fix login" --label team=core --label sprint=42
aiculator task amend ID --label sprint=43 --label team=
```

`name=` removes a label. `amend` also changes the title, spec, priority and aging, and never touches a task's progress: an agent working on it keeps its lease.

## Priority that rises with waiting

Every waiting task already gains one priority point per minute, which you can change with `--aging-unit`. This keeps low-priority work from waiting forever.

Aging steps add jumps at chosen times, so that a task overtakes others once it has waited long enough:

```sh
aiculator up --aging 1h=5,4h=20                          # Every task
aiculator task add --title "Report" --aging 30m=50       # This task, replacing the server's steps
aiculator task priority ID                               # How its priority is worked out now
```

The highest step reached counts, and steps do not add up: `1h=5,4h=20` means +5 after an hour and +20, not +25, after four.

Nothing is rewritten as tasks wait. The boost comes from the task's steps and the time it became ready, so a restart rebuilds the same queue. Each boost costs one heap move, so a claim stays as fast with aging on as with it off. A test checks the order against a brute-force sort of every waiting task.

When a task goes back to ready, because it failed or was released, its wait starts again.

## Properties

```sh
aiculator task prop ID points=5 team=core 'tags=["a","b"]'
aiculator task prop ID points=     # Removes it
aiculator task meta ID
```

Values that parse as JSON keep their type. Two writers changing different properties cannot undo each other.

## Files

```sh
aiculator task attach ID design.pdf
aiculator task fetch ID design.pdf -o copy.pdf
```

A blob is named by the hash of its contents, so storing a file twice costs nothing.

## Foreign resources

A foreign resource refers to something elsewhere, such as an HTTP link or a Jira attachment. Nothing is downloaded until someone asks for it. The bytes are then kept as a blob, so the other system is contacted only once.

```sh
aiculator task resource ID spec-page https://example.com/spec --kind http
aiculator task fetch ID spec-page
```

!!! warning "Links are treated as untrusted"
    A link can come from a ticket that anyone could have edited, so the server refuses to connect to private, loopback or cloud metadata addresses. It checks at connect time, after DNS, and that includes redirects. Only `http` and `https` are accepted.

## From an agent

Agents read this data over MCP, with `task_meta` and `fetch_resource`. Small files come back in the answer, and larger ones through `GET /v1/blobs/{sha256}`.

Agents can read but not write. Writing needs an admin or planner token.

| Route | Who | What |
|---|---|---|
| `GET /v1/tasks/{id}/meta` | Any token | Read properties |
| `POST /v1/tasks/{id}/meta` | Admin, planner | Change properties, with `null` removing one |
| `POST /v1/blobs?name=` | Admin, planner | Store bytes |
| `GET /v1/blobs/{sha256}` | Any token | Read bytes, always served as a download |
| `POST /v1/tasks/{id}/resources/{name}` | Any token | Fetch a foreign resource once |
| `POST /v1/tasks/{id}/amend` | Admin | Change what a task says |
| `GET /v1/tasks/{id}/priority` | Admin | Explain a priority |

## Limits

Blobs are not deleted when their task is. A hash can be shared by many tasks, and no cleanup exists yet, so the blob store only grows.
