# Templates

Templates let you write instructions once and reuse them. Use them for the rules you keep repeating: when to use subagents, how to retry, what format to reply in, and which steps to follow.

!!! warning "Instructions, not enforcement"
    A template produces text for a model, and a model can still ignore text. Clear, specific rules are followed far more often, but anything that must hold needs a real check, such as a task's `--check` command.

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
