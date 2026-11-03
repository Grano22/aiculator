## {{ title | default("Track the work in a task list") }}
<!-- anchor: {{ id | default("todo-list") }} -->

Keep a visible task list for this work, so that the user can see where things stand without asking.

{% include "partials/todo-tool.md" %}

- Create the list before you start, with one item per step the user would recognize.
- Mark an item in progress when you begin it, and done as soon as it is finished, never in batches at the end.
- Keep exactly one item in progress at a time.
- If the plan changes, change the list in the same step.
{% if children is defined and children | trim %}

Start with these items:

{{ children | trim }}
{% endif %}

Two exceptions:

- **If you have no task list tool:** keep a Markdown checklist (`- [ ]` and `- [x]`) and repeat it whenever it changes.
- **If you are a subagent:** {{ subagent | default("keep your list short, and include its final state in your report.") }}
