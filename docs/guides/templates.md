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

```sh
aiculator template --install 'ui-*'
aiculator template --install 'ui-*' --per-harness --harness claude,opencode   # Names the exact tool
```

They name the harness's real tool and settings when it has one, and that comes from each harness's docs:

| Harness | Tool | Single and multiple choice |
|---|---|---|
| Claude Code | `AskUserQuestion` | `multiSelect` |
| Gemini CLI | `ask_user` | `type: "choice"` and `multiSelect` |
| OpenCode | `question` | Its docs do not state multi-select, so the skill falls back if it has none |
| Others | None named | A numbered list, then stop and wait |

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
```

Every question also covers two cases: a harness with no question tool, and a subagent that cannot reach the user.

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

Print mode has nobody to answer, so Claude Code does not offer `AskUserQuestion` there. For the mode probes, a small server offers a tool with the same name and schema, and records the arguments. So this proves what the model asks for, but not how the real interactive UI looks. Other harnesses have not been tested this way.

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
