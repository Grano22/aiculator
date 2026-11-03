## {{ title | default("Ask for free text") }}
<!-- anchor: {{ id | default("ask-text") }} -->

{% if question is defined %}
**Ask the user:** {{ question }}

{% endif %}
This needs the user's own words, such as a name, a path or a description, which a list of options cannot offer.

{% if harness.question_tool == "ask_user" %}
Use the `ask_user` tool with `type: "text"`{% if placeholder is defined %} and `placeholder: "{{ placeholder }}"`{% endif %}.
{% elif harness.question_tool == "question" %}
Use the `question` tool, which lets the user type their own answer.
{% else %}
Ask in your reply, as one clear question, and end your turn.{% if harness.question_tool == "AskUserQuestion" %} `AskUserQuestion` is built for choices, so use it only if a few likely answers are worth offering, since it always lets the user type their own.{% endif %}
{% endif %}
{% if placeholder is defined %}

Give an example of the form you want: {{ placeholder }}
{% endif %}

- Ask for one thing at a time, and say what you will do with the answer.
- If you can find the answer yourself, do that instead of asking.
- **If you are a subagent:** {{ subagent | default("you cannot reach the user. Report what you need, and why.") }}
