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

- Step A: choose an approach: line 44, anchor `choose-approach`
- Step B: choose the checks: line 66, anchor `choose-checks`
- Step C: settle the details: line 89, anchor `choose-settings`
- Subagents: line 111, anchor `subagents`
- Step D: build it: research, build, review: line 122, anchor `build-flow`
  - Stage 1: Research: line 134, anchor `research`
  - Stage 2: Build: line 145, anchor `build`
  - Stage 3: Review: line 154, anchor `review`
- Commands: timeouts and retries: line 166, anchor `backoff`
- Checks to run: line 183, anchor `checks`
- Flow: ship it: line 190, anchor `ship-flow`
  - Step 1: Confirm: line 198, anchor `confirm`
  - Step 2: Ship: line 207, anchor `ship`
- Output format: line 214, anchor `output-format`
- Progress: line 225, anchor `progress`
- Release notes: line 239, anchor `changelog`

Rendered for your harness, so the questions below fall back to numbered lists where needed.

> **MUST** `ship-safely`: Never push to the main branch. Every change goes through a review.

> **MUST NOT** `no-false-claims`: Say something works unless you ran it and saw it work.

> **Note:** This skill is a demo of the template features, built around a realistic workflow.

House style for the checkout service: small commits, plain language, and a test for every fix.

> **CUSTOM COMPONENT:** This one comes from `components/DemoBadge.md`, next to this template.

## Step A: choose an approach
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

## Step B: choose the checks
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

## Step C: settle the details
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

## Subagents
<!-- anchor: subagents -->

Decide per piece of work, using these rules. If neither rule applies, do the work yourself.

- **Use a subagent when:** the work splits into independent parts, or needs broad searching
- **Do not use a subagent when:** the steps depend on each other
- **Run at most 3 at once.** Wait for one to finish before starting another.

If your harness has no way to run subagents, do the work in order yourself and say so once.

## Step D: build it: research, build, review
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

Make the change the user chose in step A.

**Output:** The changes made, and how they were checked

**Done when:** the code is changed and the tests pass

### Stage 3: Review
<!-- anchor: review -->

**Subagent:** a fresh subagent that did not write the code
Look for bugs in the changes.

**Output:** Real problems, each with a file, a line and why it matters

**Done when:** each problem is fixed, or you have explained why it stays

When every stage is done, combine the results and report them yourself. Never pass a subagent's text on unchecked.

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
