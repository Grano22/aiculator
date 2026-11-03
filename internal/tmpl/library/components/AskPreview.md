## {{ title | default("Ask with previews") }}
<!-- anchor: {{ id | default("ask-preview") }} -->

{% if question is defined %}
**Ask the user:** {{ question }}

{% endif %}
The options look different and the user has to see them to choose, such as layouts, code styles or output formats. Show each one.

{% include "partials/question-tool.md" %}
{% if harness.question_tool == "AskUserQuestion" %}
If the tool offers a `preview` field on its options, put each option's example there. Previews need a single-choice question. Whether this field is offered depends on the session's settings.
{% else %}
If the tool has a preview field, put each option's example there.
{% endif %}
{% if children is defined and children | trim %}

Offer these options:

{{ children | trim }}
{% endif %}

- Make each preview real: a code snippet, a small ASCII mockup or a sample of the output.
- Keep previews about the same length, so that they compare fairly.
- **If the tool has no previews:** put each option in your reply, under its own label with its example in a code block, and then ask which one the user wants.
- After asking, stop and wait.
- **If you are a subagent:** {{ subagent | default("choose the option that best fits the task, and report your choice with its example.") }}
