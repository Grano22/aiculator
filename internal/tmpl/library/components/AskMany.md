## {{ title | default("Ask for several choices") }}
<!-- anchor: {{ id | default("ask-many") }} -->

{% if question is defined %}
**Ask the user:** {{ question }}

{% endif %}
Show this as a checkbox list, where the user can tick any number of options, not as a question in your reply.

{% include "partials/question-tool.md" %}
{% if harness.question_tool == "AskUserQuestion" or harness.question_tool == "ask_user" %}
Set `multiSelect: true`, so that several options can be chosen.
{% else %}
Several options can be chosen. If the tool has a multi-select setting, turn it on. If it has none, use the fallback below.
{% endif %}
{% if children is defined and children | trim %}

Offer these options:

{{ children | trim }}
{% endif %}

- Phrase the question so that choosing several makes sense, such as "Which of these should I include?"
{% if min is defined %}
- At least {{ min }} must be chosen. If fewer are, ask again.
{% endif %}
- Options must be independent, so that any combination is valid. If some exclude each other, ask them separately.
- After asking, stop and wait. Do not assume an answer or carry on without one.
- **If you have no question tool, or it has no multi-select:** write a numbered list, ask for all the numbers that apply, separated by commas, and end your turn.
- **If you are a subagent:** you cannot reach the user. {{ subagent | default("Choose only what the task clearly requires, and report those choices to the agent that started you.") }}
