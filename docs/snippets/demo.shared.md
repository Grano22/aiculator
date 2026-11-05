---
name: demo
description: Demonstrates every template feature by walking through shipping a feature, with questions, subagents, retries and a live status section. Use when the user asks to see the demo template or how the template features work.
compatibility: Works in any harness that reads skills. These are instructions to the model, not technical limits.
metadata:
  version: "1.0.0"
  aiculator.kind: demo
  aiculator.generated-from: "demo"
---

# Ship a feature in the checkout service

## Contents

- Track the work: line 46, anchor `track-work`
- Step A: name the feature: line 70, anchor `name-feature`
- Step B: choose an approach: line 84, anchor `choose-approach`
- Step C: choose the API style: line 106, anchor `choose-api`
- Step D: choose the checks: line 127, anchor `choose-checks`
- Step E: settle the details: line 150, anchor `choose-settings`
- Step F: get the plan approved: line 172, anchor `approve-plan`
- Subagents: line 189, anchor `subagents`
- Step G: build it: research, build, review: line 200, anchor `build-flow`
- Step G2: split the build, if it allows: line 244, anchor `split-work`
- Commands: timeouts and retries: line 291, anchor `backoff`
- Checks to run: line 308, anchor `checks`
- Flow: ship it: line 315, anchor `ship-flow`
- Step H: clean up: line 339, anchor `delete-branch`
- Output format: line 355, anchor `output-format`
- Progress: line 366, anchor `progress`
- Release notes: line 380, anchor `changelog`
- How to format your replies: line 391, anchor `formatting`

Rendered for your harness, so the questions below fall back to numbered lists where needed.

> **MUST** `ship-safely`: Never push to the main branch. Every change goes through a review.

> **MUST NOT** `no-false-claims`: Say something works unless you ran it and saw it work.

> **Note:** This skill is a demo of the template features, built around a realistic workflow.

House style for the checkout service: small commits, plain language, and a test for every fix.

> **CUSTOM COMPONENT:** This one comes from `components/DemoBadge.md`, next to this template.

## Track the work
<!-- anchor: track-work -->

Keep a visible task list for this work, so that the user can see where things stand without asking.

Use your harness's task list tool if it has one: `TaskCreate` in Claude Code, `todowrite` in OpenCode, `write_todos` in Gemini CLI.

- Create the list before you start, with one item per step the user would recognize.
- Mark an item in progress when you begin it, and done as soon as it is finished, never in batches at the end.
- Keep exactly one item in progress at a time.
- If the plan changes, change the list in the same step.

Start with these items:

- Settle the details with the user
- Get the plan approved
- Build and review it
- Open the pull request

Two exceptions:

- **If you have no task list tool:** keep a Markdown checklist (`- [ ]` and `- [x]`) and repeat it whenever it changes.
- **If you are a subagent:** keep your list short, and include its final state in your report.

## Step A: name the feature
<!-- anchor: name-feature -->

**Ask the user:** What should this feature's flag and branch be called?

This needs the user's own words, such as a name, a path or a description, which a list of options cannot offer.

Ask in your reply, as one clear question, and end your turn.
Give an example of the form you want: faster-checkout

- Ask for one thing at a time, and say what you will do with the answer.
- If you can find the answer yourself, do that instead of asking.
- **If you are a subagent:** you cannot reach the user. Report what you need, and why.

## Step B: choose an approach
<!-- anchor: choose-approach -->

**Ask the user:** How should I build this feature?

Show this as a single-choice list that the user selects from, not as a question in your reply.

Use your harness's question tool if it has one: `AskUserQuestion` in Claude Code, `question` in OpenCode, `ask_user` in Gemini CLI.
Exactly one option can be chosen. If the tool has a multi-select setting, turn it off.

Offer these options:

- Smallest change (Recommended): touch as little as possible
- Refactor first: clean the area up, then add the feature
- Feature flag: ship it switched off

- Put the option you recommend first, and add "(Recommended)" to its label.
- Options must exclude each other. If several could be true together, this is the wrong question type.
- After asking, stop and wait. Do not assume an answer or carry on without one.
- **If you have no question tool:** write a numbered list, ask for one number, and end your turn.
- **If you are a subagent:** you cannot reach the user. Use the smallest change that works, and say so in your report.

## Step C: choose the API style
<!-- anchor: choose-api -->

**Ask the user:** Which API style reads best here?

The options look different and the user has to see them to choose, such as layouts, code styles or output formats. Show each one.

Use your harness's question tool if it has one: `AskUserQuestion` in Claude Code, `question` in OpenCode, `ask_user` in Gemini CLI.
If the tool has a preview field, put each option's example there.

Offer these options:

- Options object: `pay({ amount, currency })`
- Plain arguments: `pay(amount, currency)`

- Make each preview real: a code snippet, a small ASCII mockup or a sample of the output.
- Keep previews about the same length, so that they compare fairly.
- **If the tool has no previews:** put each option in your reply, under its own label with its example in a code block, and then ask which one the user wants.
- After asking, stop and wait.
- **If you are a subagent:** choose the option that best fits the task, and report your choice with its example.

## Step D: choose the checks
<!-- anchor: choose-checks -->

**Ask the user:** Which checks should I run before review?

Show this as a checkbox list, where the user can tick any number of options, not as a question in your reply.

Use your harness's question tool if it has one: `AskUserQuestion` in Claude Code, `question` in OpenCode, `ask_user` in Gemini CLI.
Several options can be chosen. If the tool has a multi-select setting, turn it on. If it has none, use the fallback below.

Offer these options:

- Unit tests
- Lint
- Security scan

- Phrase the question so that choosing several makes sense, such as "Which of these should I include?"
- At least 1 must be chosen. If fewer are, ask again.
- Options must be independent, so that any combination is valid. If some exclude each other, ask them separately.
- After asking, stop and wait. Do not assume an answer or carry on without one.
- **If you have no question tool, or it has no multi-select:** write a numbered list, ask for all the numbers that apply, separated by commas, and end your turn.
- **If you are a subagent:** you cannot reach the user. Choose only what the task clearly requires, and report those choices to the agent that started you.

## Step E: settle the details
<!-- anchor: choose-settings -->

Ask related questions together in a **single call**, so that they appear as tabs the user can move between before submitting. Do not ask them one at a time, and do not ask them in your reply.

Use your harness's question tool if it has one: `AskUserQuestion` in Claude Code, `question` in OpenCode, `ask_user` in Gemini CLI.
Put every question in one call, each with its own short header, which is the tab's label.

Ask these:

- **Rollout**: How should it roll out? *(one can be chosen)*
  All at once (Recommended), or gradually

- **Docs**: Which docs need updating? *(several can be chosen)*
  README, or the changelog, or the API reference

- Ask at most 4 together. For more, group the related ones and make a second call only after the first is answered.
- Each question must stand on its own, because the user may answer them in any order.
- After asking, stop and wait for all the answers. Do not assume any of them.
- **If you have no question tool:** write the questions as numbered sections with lettered options, ask for answers in the form `1: a, 2: b c`, and end your turn.
- **If you are a subagent:** you cannot reach the user. Answer each one with the recommended option, and report your choices to the agent that started you.

## Step F: get the plan approved
<!-- anchor: approve-plan -->

Do not change anything until the user has approved a plan.

Use your harness's plan mode if it has one: `EnterPlanMode` and `ExitPlanMode` in Claude Code, `enter_plan_mode` and `exit_plan_mode` in Gemini CLI.

The plan must cover:

- The answers from steps A to E
- The checks you will run, and in what order

- Say which files will change, how you will verify the result, and what could go wrong.
- If the user asks for changes, revise the plan and ask again. Do not start on a partial approval.
- **If you have no plan mode:** write the plan as a numbered list, ask for approval, and end your turn.
- **If you are a subagent:** you cannot get approval, so report your plan and change nothing, unless your task says to make the changes.

## Subagents
<!-- anchor: subagents -->

Decide per piece of work, using these rules. If neither rule applies, do the work yourself.

- **Use a subagent when:** the work splits into independent parts, or needs broad searching
- **Do not use a subagent when:** the steps depend on each other
- **Run at most 3 at once.** Wait for one to finish before starting another.

If your harness has no way to run subagents, do the work in order yourself and say so once.

## Step G: build it: research, build, review
<!-- anchor: build-flow -->

You are the orchestrator. Run the stages below in order, and delegate each one to a subagent. Keep your own work to coordinating and checking results, so that your context stays clear.

- **Give each subagent everything it needs.** It starts with none of this conversation, so its prompt must state the goal, the inputs, the exact output you want, and when to stop.
- **Do not guess a subagent's result.** Wait for it, read it, and check it against the stage's "Done when" before moving on.
- **Run a stage in parallel only if it says so.** Parallel subagents must not change the same files.
- **Subagents cannot reach the user.** Questions that need the user are for you to ask between stages. A subagent makes a reasonable choice, and reports it.
- **If a stage fails,** retry it once with what you learned added to the prompt. If it fails again, stop and report what happened, rather than continuing on a bad result.
- **If your harness cannot run subagents,** do each stage yourself, in order, and say so once.

### Stage 1: Research
<!-- anchor: research -->

**Run up to 3 subagents at once.**

Find the code this feature touches.

**Output:** Short findings with file paths, and no changes to any file

**Done when:** you could explain the change, with evidence for each claim

### Stage 2: Build
<!-- anchor: build -->

Make the change the user chose in step B.

**Output:** The changes made, and how they were checked

**Done when:** the code is changed and the tests pass

### Stage 3: Review
<!-- anchor: review -->

**Subagent:** a fresh subagent that did not write the code
Look for bugs in the changes.

**Output:** Real problems, each with a file, a line and why it matters

**Done when:** each problem is fixed, or you have explained why it stays

When every stage is done, combine the results and report them yourself. Never pass a subagent's text on unchecked.

## Step G2: split the build, if it allows
<!-- anchor: split-work -->

Use this to split one piece of work among subagents **in this working tree**. They share files, and there is no merge step to catch a collision, so the plan decides what may run together, not your judgment.

1. **List the jobs.** Write each one down with every file it will change, as a file or a directory ending in `/`. Add `reads` for files it relies on staying the same. Be honest and include what is easy to forget: tests, and shared files such as lockfiles, generated code and indexes. A job that changes files you cannot predict gets `exclusive: true`, and `deps` say what must finish first.

   ```yaml
   jobs:
     - id: api
       touches: [internal/api/users.go]
     - id: docs
       touches: [docs/users.md]
       reads: [internal/api/users.go]
     - id: format
       exclusive: true
       deps: [api, docs]
   ```

2. **Get the plan.**

   ```sh
   aiculator parallel plan --file jobs.yaml --max 3 --granularity auto -o plan.json
   ```

   If it says `Sequential`, do the jobs yourself, one at a time. Do not run them together anyway. Do not reword the jobs to get a different answer. Two jobs in one file conflict, because two subagents editing one file can overwrite each other, and naming parts of the file does not change that.

3. **For each wave, in order:**
   1. Record the state: `aiculator parallel snapshot`, and keep what it prints.
   2. Start one subagent per job in that wave, all in the same turn.
   3. Wait for every one of them.
   4. Check them: `aiculator parallel verify --plan plan.json --wave N --since SNAPSHOT`.
   5. If it reports files no job claimed, stop. Look at what changed, fix it yourself, and do not start the next wave until the tree is right.
   6. If it lists a job that changed nothing, check whether that subagent really did its work.
   7. Run the tests before the next wave.

**Tell every subagent, in its prompt:**

- Exactly which files it may change, and that it must change nothing else, even to fix something it notices. It reports that to you instead.
- To read a file again just before changing it, and to make small, targeted edits rather than rewriting the file.
- Not to run `git add`, `git commit`, `git stash`, `git checkout` or `git reset`, formatters over the whole project, or anything that installs dependencies. Others are working in the same tree.
- Not to call any swarm tools. A lease belongs to you, and only you report progress or submit.

- **If your harness cannot run subagents,** do the jobs yourself, in the planned order.
- **If you are a subagent yourself,** do not start more. Do your own part, and report back.
- **For separate tasks that take a long time,** use the swarm's own worktrees instead, because those can also be reviewed and merged one by one.

## Commands: timeouts and retries
<!-- anchor: backoff -->

Give every command a time limit of **120s**. A command that hits the limit has failed; do not wait longer.

Retry only for: timeouts, network errors and rate limits.
Do not retry a failure that will repeat, such as a failing test, a syntax error or a denied permission. Read the error and change something first.

| Attempt | Wait before it |
|---|---|
| 1 | None |
| 2 | 2s |
| 3 | 4s |
| 4 | 8s |

After the last attempt, stop retrying. Report the command, the final error and what you tried.

## Checks to run
<!-- anchor: checks -->

1. Run unit tests, and stop if it fails.
2. Run lint, and stop if it fails.
3. Run security scan, and stop if it fails.

## Flow: ship it
<!-- anchor: ship-flow -->

Do these steps in order, one at a time. Do not skip a step or merge steps, except as stated here. After each step, say which step you finished and which comes next.

- **Steps marked "Ask the user":** ask, then stop and wait for the answer. Use your harness's question tool if it has one, and otherwise end your turn with the question. Do not guess the answer or continue without it.
- **If you are a subagent:** you were started by another agent and cannot reach the user. Follow the "As a subagent" line in each step instead of asking. Report every default you chose to the agent that started you.

### Step 1: Confirm
<!-- anchor: confirm -->

Summarize what changed and what you checked.

**Ask the user:** Is this ready to ship?

**As a subagent:** Do not ask. Report that it is ready, with your evidence.

### Step 2: Ship
<!-- anchor: ship -->

Open the pull request.

**As a subagent:** Do not ship. Only the agent that started you does that.

## Step H: clean up
<!-- anchor: delete-branch -->

**Ask the user:** Delete the merged feature branch?

Ask this as a yes or no choice, and do not continue until you have the answer.

Use your harness's question tool if it has one: `AskUserQuestion` in Claude Code, `question` in OpenCode, `ask_user` in Gemini CLI.
Offer two options: the action, named specifically, and a way to decline.

- Name what will happen in the option itself, such as "Delete 3 branches", not just "Yes".
- Say what cannot be undone.
- Silence is not a yes. If the answer is unclear, ask again.
- **If you have no question tool:** ask for a clear yes or no, and end your turn.
- **If you are a subagent:** do not take the action. Report that it needs confirmation.

## Output format
<!-- anchor: output-format -->

Every reply must use exactly this format, including short ones and replies late in a long session. Before you send a reply, compare it with this format and fix any difference.

```text
Done: what changed
Checked: what you ran and the result
Next: what remains, or "nothing"
```

## Progress
<!-- anchor: progress -->

**This section is alive: keep it true.** Update it after each step above. Treat a stale section as a bug.

- Change only what lies between the markers, and leave the markers themselves in place.
- Replace what is out of date instead of adding to it, so that this always reads as the present state and never as a history.
- It must always cover: the current step, and anything blocking it.
- If you are a subagent, report what should change to the agent that started you, unless you were told to edit it.

<!-- dynamic:progress:start -->
Not started.
<!-- dynamic:progress:end -->

## Release notes
<!-- anchor: changelog -->

**This section is alive: keep it true.** Update it whenever the plan changes. Treat a stale section as a bug.

It lives in `RELEASE_NOTES.md`, between the marker comments `<!-- dynamic:changelog:start -->` and `<!-- dynamic:changelog:end -->`. Create the file and markers if they do not exist yet.

- Change only what lies between the markers, and leave the markers themselves in place.
- Replace what is out of date instead of adding to it, so that this always reads as the present state and never as a history.
- If you are a subagent, report what should change to the agent that started you, unless you were told to edit it.

## How to format your replies
<!-- anchor: formatting -->

Choose the form that makes each thing easiest to read, using the rules below. Do not decorate: a short answer in a plain sentence is often best, and formatting should never replace a clear explanation.

This output goes to a terminal. Assume code blocks and tables work, and that diagrams, images, collapsible sections and LaTeX may not.

### Code
<!-- anchor: render-code -->

Put all code in a fenced block with a language, so that it is highlighted and easy to copy. Never indent it instead. Say which file it belongs to on the line above.

````markdown
In `internal/calc.go`:

```go
func add(a, b int) int { return a + b }
```
````

### Commands
<!-- anchor: render-shell -->

Give commands in a `sh` block that can be pasted as it is: no `$` prompt, and one step per line. Show their output in a separate `text` block. Say beforehand if a command changes anything.

```sh
go test ./...
```

### Changes
<!-- anchor: render-diff -->

To show a change to code, use a `diff` block with just enough context to place it.

```diff
-func add(a, b int) int { return a - b }
+func add(a, b int) int { return a + b }
```

### Tables
<!-- anchor: render-table -->

Use a table to compare items across two or more attributes. Keep cells to a few words, put explanations in the text around it, and right-align numbers. Do not use a table for a plain list, or for one row.

```markdown
| Option | Speed | Cost |
|---|--:|--:|
| A | 12 ms | $3 |
| B | 40 ms | $1 |
```

### Checklists
<!-- anchor: render-checklist -->

Show progress or remaining steps as a checklist.

```markdown
- [x] Add the endpoint
- [ ] Write the tests
```

### Callouts
<!-- anchor: render-callout -->

Set off a warning or key note as a quote, starting with a bold label. Use this only for what the reader must not miss, and rarely.

```markdown
> **Warning:** This deletes the production table.
```

### File trees
<!-- anchor: render-tree -->

Show a directory layout in a `text` block, using box-drawing characters.

```text
cmd/
├── main.go
└── serve.go
```

### Diagrams
<!-- anchor: render-diagram -->

A terminal may show Mermaid as plain source, so draw flows as ASCII in a `text` block. Use a Mermaid block only when the user asks, or when the answer is going into a document that renders it.

```text
Request --> Valid? --yes--> Save
              |
              no--> Reject
```

### Images
<!-- anchor: render-image -->

A terminal may not show images, so give the file's path and say what it shows. Do not rely on an image to carry the point.

### Collapsible sections
<!-- anchor: render-details -->

A terminal may show `<details>` tags as plain text, so do not use them. Leave optional material out and offer it, or put it at the end under its own heading.

### File references
<!-- anchor: render-fileref -->

Refer to code as `path/to/file.go:42`, with the line number, because many harnesses make this clickable. Use the path from the project root, and do not wrap it in a link.

In this demo, cite the checkout code this way in every review finding.
