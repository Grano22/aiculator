## {{ title | default("Ask for one choice") }}
<!-- anchor: {{ id | default("ask-one") }} -->

{% if question is defined %}
**Ask the user:** {{ question }}

{% endif %}
Show this as a single-choice list that the user selects from, not as a question in your reply.

{% include "partials/question-tool.md" %}
{% if harness.question_tool == "AskUserQuestion" or harness.question_tool == "ask_user" %}
Set `multiSelect: false`, so that exactly one option can be chosen.
{% else %}
Exactly one option can be chosen. If the tool has a multi-select setting, turn it off.
{% endif %}
{% if children is defined and children | trim %}

Offer these options:

{{ children | trim }}
{% endif %}

- Put the option you recommend first, and add "(Recommended)" to its label.
- Options must exclude each other. If several could be true together, this is the wrong question type.
- After asking, stop and wait. Do not assume an answer or carry on without one.
- **If you have no question tool:** write a numbered list, ask for one number, and end your turn.
- **If you are a subagent:** you cannot reach the user. {{ subagent | default("Choose the recommended option, and report that choice to the agent that started you.") }}
