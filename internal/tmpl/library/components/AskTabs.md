## {{ title | default("Ask several questions together") }}
<!-- anchor: {{ id | default("ask-tabs") }} -->

Ask related questions together in a **single call**, so that they appear as tabs the user can move between before submitting. Do not ask them one at a time, and do not ask them in your reply.

{% include "partials/question-tool.md" %}
{% if harness.question_tool == "AskUserQuestion" or harness.question_tool == "ask_user" %}
Put every question in the `questions` array of one call, each with its own short `header`, which is the tab's label. Set `multiSelect` for each question on its own.
{% else %}
Put every question in one call, each with its own short header, which is the tab's label.
{% endif %}
{% if children is defined and children | trim %}

Ask these:

{{ children | trim }}
{% endif %}

- Ask at most 4 together. For more, group the related ones and make a second call only after the first is answered.
- Each question must stand on its own, because the user may answer them in any order.
- After asking, stop and wait for all the answers. Do not assume any of them.
- **If you have no question tool:** write the questions as numbered sections with lettered options, ask for answers in the form `1: a, 2: b c`, and end your turn.
- **If you are a subagent:** you cannot reach the user. {{ subagent | default("Answer each one with the recommended option, and report your choices to the agent that started you.") }}
