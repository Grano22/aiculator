# Templates

Templates let you write instructions once and reuse them. Use them for the rules you keep repeating: when to use subagents, how to retry, what format to reply in, and which steps to follow.

!!! warning "Instructions, not enforcement"
    A template produces text for a model, and a model can still ignore text. Clear, specific rules are followed far more often, but anything that must hold needs a real check, such as a task's `--check` command.

!!! example "See every feature working together"
    The [template demo](template-demo.md) walks through one template that uses all of this.

## Quick start

```sh
aiculator template --list
aiculator template --render agent-discipline --set retries=5   # Preview; writes nothing
aiculator template --install agent-discipline                  # Render and install
```

`--install` takes the same options as `skill --install`: patterns, `--harness`, `--global`, `--path` and `--copy`.

## Where templates live

Templates go in `.agents/templates/`. `.agent/templates/` and `.agent/template/` also work.

```text
.agents/templates/
├── my-rules.md.j2            # A single-file template
├── release-flow/             # A template with extra files
│   ├── SKILL.md.j2
│   └── references/guide.md   # Copied into the skill
├── partials/                 # For {% include %}
└── components/               # Your own <Components>
```

Single files can end in `.md.j2`, `.j2`, `.jinja` or `.mdx`.

## Anatomy of a template

```jinja
---
template:                 # Removed from the output
  vars:
    retries: 3
name: my-rules
description: Working rules for this project. Use when the user asks to apply them.
metadata:
  version: "1.0.0"
---

# My rules

<Toc />

<Directive id="main-rule">Never push to main.</Directive>

{% if harness.id == "claude" %}
This line appears only in the Claude Code copy.
{% endif %}

{% include "partials/house-style.md" %}

<Backoff timeout="120s" retries="{{ retries }}" />
```

Rendering runs in two passes: Jinja2 first, then components. So `{{ retries }}` can feed a component's props.

## Components

### Subagents

Says when to use subagents.

```html
<Subagents use="when" max="4"
  when="the work splits into independent parts"
  not_when="the steps depend on each other" />
```

`use` is `always`, `when` or `never`.

### Backoff

Sets a command timeout and a retry policy. The swarm computes the table of waits, because a model told to "double it each time" tends to get the numbers wrong.

```html
<Backoff timeout="120s" retries="3" base="2s" factor="2" limit="60s" />
```

Add `on="..."` to say which failures to retry.

### OutputFormat

Makes the model repeat a format in every reply.

```html
<OutputFormat>
Done: what changed
Checked: what you ran and the result
</OutputFormat>
```

Use `repeat="Final answers"` to apply it to only some replies.

### Flow and Step

Forces an order of steps, with interactive questions and exceptions for subagents, which cannot reach the user.

```html
<Flow name="confirm before acting" id="confirm-flow">
<Step n="1" title="Plan" id="plan"
  ask="Does this plan look right?"
  subagent="Do not ask. Continue, and report your plan with the result.">
List the changes you intend to make.
</Step>
</Flow>
```

### Directive

Marks a rule the model can cite by name. `level` is any word, and defaults to `must`.

```html
<Directive level="must not" id="no-false-claims">Say something works unless you ran it.</Directive>
```

### Toc and Anchor

`<Toc />` writes a table of contents with exact line numbers and anchors, so a model can jump to a section without reading the whole file:

```text
## Contents

- Subagents: line 28, anchor `subagents`
- Commands: timeouts and retries: line 39, anchor `backoff`
```

`<Anchor id="name" />` adds a searchable marker anywhere. Most components add their own.

### Note

```html
<Note>Something worth pointing out.</Note>
```

## Forcing a kind of question

Bundled templates tell a model which interface to use, so you get a list to select from, and not a paragraph to answer.

| Template | Component | What the user sees |
|---|---|---|
| `ui-select` | `<AskOne>` | One choice from a list |
| `ui-checkbox` | `<AskMany>` | Any number of choices, as checkboxes |
| `ui-tabs` | `<AskTabs>` and `<Tab>` | Several questions in one form, with tabs |
| `ui-confirm` | `<AskConfirm>` | A yes or no choice before a risky action |
| `ui-text` | `<AskText>` | A place to type, when options cannot cover the answer |
| `ui-preview` | `<AskPreview>` | Choices with a visible example of each |
| `ui-todos` | `<TodoList>` | A live task list |
| `ui-plan` | `<PlanMode>` | A plan to approve before anything changes |

```sh
aiculator template --install 'ui-*'
aiculator template --install 'ui-*' --per-harness --harness claude,opencode   # Names the exact tool
```

They name the harness's real tool and settings when it has one, and that comes from each harness's docs:

| Harness | Questions | Task list | Plan mode |
|---|---|---|---|
| Claude Code | `AskUserQuestion`, with `multiSelect` | `TaskCreate`, `TaskUpdate` | `EnterPlanMode`, `ExitPlanMode` |
| Gemini CLI | `ask_user`, with `type` `choice`, `text` or `yesno` | `write_todos` | `enter_plan_mode`, `exit_plan_mode` |
| OpenCode | `question`; its docs do not state multi-select | `todowrite` | None in its tool docs |
| Others | None named: a numbered list, then stop and wait | A Markdown checklist | A numbered plan, then stop and wait |

On some Claude Code models the task tools must be switched on, so `ui-todos` also tells the model what to do without them. Whether `AskUserQuestion` offers `preview` depends on the session, so `<AskPreview>` falls back to labeled code blocks.

`ui-plan` deliberately has no `allowed-tools`. In Claude Code that field skips the permission prompt, and the prompt on `ExitPlanMode` is your approval of the plan.

Without `--per-harness`, one shared skill names all three tools, because it cannot know which harness reads it.

To ask a specific question, use the components in your own template:

```html
<AskOne question="Which database?" subagent="Use SQLite, and say so in your report.">
- PostgreSQL (Recommended): best for most apps
- SQLite: no server to run
</AskOne>

<AskMany question="Which checks should I run?" min="1">
- Unit tests
- Security scan
</AskMany>

<AskTabs>
<Tab header="Language" question="Which language?">Go, or Python</Tab>
<Tab header="Extras" question="Which extras?" multi="true">Docker, or CI</Tab>
</AskTabs>

<AskConfirm question="Delete 3 merged branches?" />

<AskText question="What should the service be called?" placeholder="my-service" />

<AskPreview question="Which layout?">
- Compact: `a | b`
- Spacious: `a  |  b`
</AskPreview>

<TodoList>
- Write the tests
- Make them pass
</TodoList>

<PlanMode>
- Which files change
- How you will verify it
</PlanMode>
```

Every question also covers two cases: a harness with no question tool, and a subagent that cannot reach the user.

## Forcing rich Markdown

`rich-markdown` sets how a model formats its replies, using the right form for each thing. `<RichMarkdown>` includes every rule below, and `<Render>` adds a single one.

```sh
aiculator template --install rich-markdown                    # For a terminal
aiculator template --install rich-markdown --set surface=rich # Where full Markdown renders
```

```html
<RichMarkdown only="code,diff,table,fileref" surface="terminal" />

<Render kind="callout">Use this for production warnings only.</Render>
```

| `kind` | What the model is told | With `surface="terminal"` (the default) | With `surface="rich"` |
|---|---|---|---|
| `code` | Fenced block with a language | Same | Same |
| `inline` | `code`, bold and italics, for the right things | Same | Same |
| `shell` | A `sh` block to paste, with output apart | Same | Same |
| `diff` | A `diff` block | Same | Same |
| `table` | Comparisons, with short cells | Same | Same |
| `list` | Numbered when order matters | Same | Same |
| `checklist` | `- [ ]` and `- [x]` | Same | Same |
| `headings` | `##` and `###` for longer answers | Same | Same |
| `callout` | A quote with a bold label | Same | Also GitHub's `> [!WARNING]` |
| `quote` | Exact errors, logs and documents | Same | Same |
| `fileref` | `path/to/file.go:42` | Same | Same |
| `tree` | A directory layout in a `text` block | Same | Same |
| `link` | Descriptive text, and only URLs it has seen | Same | Same |
| `diagram` | Flows and relationships | ASCII in a `text` block | A `mermaid` block |
| `image` | Pictures | A path and a description | `![alt](path)` |
| `details` | Long optional material | Left out, or at the end | `<details>` |
| `math` | Formulas | Plain text or code | `$...$` and `$$...$$` |
| `footnote` | Asides and sources | Short sentences and links | `[^1]` |
| `rule` | Separate parts of a reply | Same | Same |

An unknown `kind` fails the render, so a typo cannot silently drop a rule.

!!! warning "What this controls, and what it does not"
    These rules decide what the model writes. How it then looks depends on your harness and where it runs, which most harnesses do not document. Terminal rules are the cautious default for that reason, so check a sample in your own harness before you set `surface=rich`.

A long skill can keep its contents list short with `<Toc depth="2" />`, which lists only the `##` sections.

## Subagent flows

`subagent-flow` makes a model act as an orchestrator: it runs stages in order, delegates each one, and checks each result before moving on.

```html
<SubagentFlow name="research, build, review">
<Stage n="1" title="Research" parallel="3"
  output="Short findings with file paths" done="you could explain the change">
Split the unknowns into separate questions.
</Stage>

<Stage n="2" title="Review" agent="a fresh subagent that did not write the code"
  done="each problem is fixed or explained">
Ask it to look for bugs.
</Stage>
</SubagentFlow>
```

To split one task among subagents in the same worktree, use the `parallel-subagents` template or `<ParallelWork>`. See [Parallel subagents](parallel.md).

## Sections a model keeps current

`<DynamicSection>` marks text the model must keep true as it works. `living-section` is a ready-made one, for a progress file.

```html
<DynamicSection id="status" title="Status" file="PROGRESS.md"
  update="after every commit"
  keep="what is done, what is blocked, and the next step" />
```

With `file`, the section lives in that project file, which is usually what you want. Without it, the section lives in the skill itself, between markers:

```html
<DynamicSection id="decisions" title="Decisions so far">Nothing yet.</DynamicSection>
```

Re-rendering keeps what a model wrote there, and `--check` does not count it as drift. A model's edit can shift the line numbers in a contents list until the next render, though the anchors stay right.

## How well this works

These are instructions, so a model can still ignore them. `scripts/gate_ui_templates.sh` tests them on a real Claude Code session, using Haiku, the cheapest model:

| Probe | Result on the real model |
|---|---|
| `ui-select` | One call with `multiSelect: false`, recommendation first |
| `ui-checkbox` | One call with `multiSelect: true` |
| `ui-tabs` | One call holding 3 questions |
| No question tool | Looks for the tool, then writes the numbered list |
| As a subagent | Does not ask, and reports its choice |
| `living-section` | Writes `PROGRESS.md` with the markers |
| `subagent-flow` | Three subagents, including a separate reviewer, and the bug fixed |
| `ui-confirm` | One choice, `Delete build directory` or `Keep it`, before acting |
| `ui-text` | Asks in its reply, and creates nothing |
| `ui-preview` | Every option carries a `preview` |
| `ui-todos` | `TaskCreate` per step, and `TaskUpdate` as each one starts and ends |
| `ui-plan` | Presents a numbered plan, and changes no file |
| `rich-markdown` | A `diff` block, a `sh` block, a table and `calc.go:3` in one reply |

Print mode has nobody to answer, so Claude Code does not offer `AskUserQuestion` there. For the mode probes, a small server offers a tool with the same name and schema, and records the arguments. So this proves what the model asks for, but not how the real interactive UI looks. Other harnesses have not been tested this way.

Two more limits follow from this. The `ui-plan` probe proves the plan comes first and nothing changes, but not the real `EnterPlanMode` tool, which the model did not call in print mode. The `rich-markdown` probe checks the text the model wrote, not how a terminal displays it.

## Your own components

Create `components/Name.md` in a template directory, using Jinja2. Props become variables, and the tag's content becomes `children`. A project component with a bundled name replaces the bundled one.

```jinja title=".agents/templates/components/Warn.md"
> **Warning{% if topic is defined %} about {{ topic }}{% endif %}:** {{ children | trim }}
```

```html
<Warn topic="deploys">Check the staging logs first.</Warn>
```

Tags inside code fences and inline code are left alone, so you can show examples.

## Per-harness copies

A skill installed the normal way is one file shared through links, so `harness.id` is `generic` in it. To vary by harness, render a separate copy into each harness's own directory:

```sh
aiculator template --install my-rules --per-harness --harness claude,opencode
aiculator template --render my-rules --for claude     # Preview one
```

| Variable | Value |
|---|---|
| `harness.id`, `harness.name` | Which harness, or `generic` |
| `harness.has_mcp`, `harness.has_acp` | What it supports |
| `harness.reads_agents` | Whether it reads `.agents/skills` |
| `harness.question_tool` | Its question tool, if confirmed; empty otherwise |
| `skill.name` | The template's name |
| `aiculator.version` | The running version |

## Safety

- **Rendering cannot run code** or read outside the template directories, so rendering a template from an untrusted repository does not endanger your machine. What the skill then tells a model to do still deserves review.
- **A typo in a variable name is an error,** and it does not silently render as nothing.
- **A render is linted first.** A template with a mistake cannot damage an installed skill.
- **Hand-written skills are never overwritten.** Only directories a template generated are replaced.

## Keep skills current

Rendered skills are ordinary files, so a changed template leaves them stale. This fails if any have drifted, which suits CI:

```sh
aiculator template --check
```
