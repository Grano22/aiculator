## {{ title | default("Ask for confirmation") }}
<!-- anchor: {{ id | default("ask-confirm") }} -->

{% if question is defined %}
**Ask the user:** {{ question }}

{% endif %}
Ask this as a yes or no choice, and do not continue until you have the answer.

{% if harness.question_tool == "ask_user" %}
Use the `ask_user` tool with `type: "yesno"`.
{% elif harness.question_tool == "AskUserQuestion" %}
Use the `AskUserQuestion` tool, with `multiSelect: false` and two options: the action, named specifically, and a way to decline.
{% else %}
{% include "partials/question-tool.md" %}
Offer two options: the action, named specifically, and a way to decline.
{% endif %}

- Name what will happen in the option itself, such as "Delete 3 branches", not just "Yes".
- Say what cannot be undone.
- Silence is not a yes. If the answer is unclear, ask again.
- **If you have no question tool:** ask for a clear yes or no, and end your turn.
- **If you are a subagent:** {{ subagent | default("do not take the action. Report that it needs confirmation.") }}
