{% set s = surface | default("terminal") %}
{% if kind == "code" %}
### Code
<!-- anchor: render-code -->

Put all code in a fenced block with a language, so that it is highlighted and easy to copy. Never indent it instead. Say which file it belongs to on the line above.

````markdown
In `internal/calc.go`:

```go
func add(a, b int) int { return a + b }
```
````
{% elif kind == "inline" %}
### Names in text
<!-- anchor: render-inline -->

Put file names, commands, flags, functions and values in `inline code`. Use **bold** only for the one thing the reader must not miss, *italics* rarely, and ~~strikethrough~~ only for something explicitly withdrawn.
{% elif kind == "shell" %}
### Commands
<!-- anchor: render-shell -->

Give commands in a `sh` block that can be pasted as it is: no `$` prompt, and one step per line. Show their output in a separate `text` block. Say beforehand if a command changes anything.

```sh
go test ./...
```
{% elif kind == "diff" %}
### Changes
<!-- anchor: render-diff -->

To show a change to code, use a `diff` block with just enough context to place it.

```diff
-func add(a, b int) int { return a - b }
+func add(a, b int) int { return a + b }
```
{% elif kind == "table" %}
### Tables
<!-- anchor: render-table -->

Use a table to compare items across two or more attributes. Keep cells to a few words, put explanations in the text around it, and right-align numbers. Do not use a table for a plain list, or for one row.

```markdown
| Option | Speed | Cost |
|---|--:|--:|
| A | 12 ms | $3 |
| B | 40 ms | $1 |
```
{% elif kind == "list" %}
### Lists
<!-- anchor: render-list -->

Use a numbered list when order matters, and bullets when it does not. Keep items parallel and short. A list of one item should be a sentence, and do not nest deeper than two levels.
{% elif kind == "checklist" %}
### Checklists
<!-- anchor: render-checklist -->

Show progress or remaining steps as a checklist.

```markdown
- [x] Add the endpoint
- [ ] Write the tests
```
{% elif kind == "headings" %}
### Headings
<!-- anchor: render-headings -->

Use `##` and `###` to divide longer answers into parts a reader can scan. Do not skip levels. A short answer needs no heading, and it should open with the answer, not with a heading.
{% elif kind == "callout" %}
### Callouts
<!-- anchor: render-callout -->

Set off a warning or key note as a quote, starting with a bold label. Use this only for what the reader must not miss, and rarely.

```markdown
> **Warning:** This deletes the production table.
```
{% if s == "rich" %}
Where GitHub Markdown renders, you may use its alert form, `> [!WARNING]`.
{% endif %}
{% elif kind == "quote" %}
### Quotes
<!-- anchor: render-quote -->

Quote errors, logs and documents exactly. Use a `text` code block for machine output, and a `>` quote for what a person or document said.
{% elif kind == "fileref" %}
### File references
<!-- anchor: render-fileref -->

Refer to code as `path/to/file.go:42`, with the line number, because many harnesses make this clickable. Use the path from the project root, and do not wrap it in a link.
{% elif kind == "tree" %}
### File trees
<!-- anchor: render-tree -->

Show a directory layout in a `text` block, using box-drawing characters.

```text
cmd/
├── main.go
└── serve.go
```
{% elif kind == "link" %}
### Links
<!-- anchor: render-link -->

Write links as `[what it is](https://example.com)`, with text that says where they go, not "here". Only link to URLs you have actually seen, and do not invent them.
{% elif kind == "diagram" %}
### Diagrams
<!-- anchor: render-diagram -->

{% if s == "rich" %}
Draw flows and relationships as a `mermaid` block, since this surface renders it. Keep to about ten nodes, and use words in the labels.

```mermaid
flowchart TD
    A[Request] --> B{Valid?}
    B -- Yes --> C[Save]
    B -- No --> D[Reject]
```
{% else %}
A terminal may show Mermaid as plain source, so draw flows as ASCII in a `text` block. Use a Mermaid block only when the user asks, or when the answer is going into a document that renders it.

```text
Request --> Valid? --yes--> Save
              |
              no--> Reject
```
{% endif %}
{% elif kind == "image" %}
### Images
<!-- anchor: render-image -->

{% if s == "rich" %}
Embed images with `![what it shows](path)`, and always write alt text that says what the image shows.
{% else %}
A terminal may not show images, so give the file's path and say what it shows. Do not rely on an image to carry the point.
{% endif %}
{% elif kind == "details" %}
### Collapsible sections
<!-- anchor: render-details -->

{% if s == "rich" %}
Put long, optional material such as full logs in a `<details>` block with a `<summary>`, so that the main answer stays short.
{% else %}
A terminal may show `<details>` tags as plain text, so do not use them. Leave optional material out and offer it, or put it at the end under its own heading.
{% endif %}
{% elif kind == "math" %}
### Math
<!-- anchor: render-math -->

{% if s == "rich" %}
Write formulas as `$...$` inline and `$$...$$` on their own line, where the surface renders LaTeX.
{% else %}
A terminal may show LaTeX as plain source, so write formulas in plain text or a code block, such as `p99 = sorted[n * 99 / 100]`.
{% endif %}
{% elif kind == "footnote" %}
### Footnotes and sources
<!-- anchor: render-footnote -->

{% if s == "rich" %}
Use footnotes (`[^1]`) only for an aside that would break the flow. Cite sources as links at the point they are used.
{% else %}
A terminal may not render footnotes, so put an aside in its own short sentence, and cite sources as links at the point they are used.
{% endif %}
{% elif kind == "rule" %}
### Horizontal rules
<!-- anchor: render-rule -->

Use a `---` rule only between truly separate parts of a reply. Headings usually do this better.
{% else %}
{{ unknown_render_kind }}
{% endif %}
{% if children is defined and children | trim %}

{{ children | trim }}
{% endif %}
