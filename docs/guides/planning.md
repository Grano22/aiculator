# Plan work

A plan turns a spec into many tasks, and keeps waiting workers informed while that happens. The `swarm-plan` skill walks a model through it, or you can drive it by hand.

## The steps

```sh
# 1. Take the lease BEFORE reading the spec. Workers now wait instead of exiting.
aiculator plan begin --ttl 5m          # Prints the epoch

# 2. Renew it as you go, and tell workers what you are doing.
aiculator plan update --epoch 1 --ttl 5m --phase "reading code"

# 3. Publish what is coming.
aiculator plan update --epoch 1 --ttl 5m --phase splitting --expected 1=12 --expected 2=3

# 4. Add tasks. Workers cannot see them yet.
aiculator plan add --epoch 1 --id api --title "Add the API" --tier 2 --check "go test ./api"
aiculator plan add --epoch 1 --id ui --title "Add the UI" --dep api --check "npm test"

# 5. Publish the whole plan at once.
aiculator plan commit --epoch 1
```

## What workers see

```json
{
  "action": "wait",
  "reason": "planning",
  "planning": {
    "epoch": 1,
    "phase": "splitting",
    "expected_tasks": {"1": 12, "2": 3},
    "age_ms": 180,
    "stale": false
  }
}
```

## Rules the swarm enforces

- **A hint is a promise.** Agents may exit once nothing is expected at or below their tier. After that, the swarm refuses tasks and hint changes that would have reached them.
- **One planner at a time.** A second one is refused while the lease is live.
- **A crashed planner cannot do damage.** If the lease expires, its drafts are canceled, the epoch rises, and its old epoch is refused. Waiting workers see `planner_lost`.
- **A plan cannot deadlock.** Commit rejects dependency cycles and unknown dependencies.

## Writing good tasks

- Start low on the tier. Failures escalate automatically.
- Always give a `--check` that fails before the work and passes after it.
- Declare `--touches path`, so that overlapping tasks do not run at the same time.
- Keep tasks small, because a worker sees only its own spec and the code.
