## Flow{% if name is defined %}: {{ name }}{% endif %}

<!-- anchor: {{ id | default("flow") }} -->

Do these steps in order, one at a time. Do not skip a step or merge steps, except as stated here. After each step, say which step you finished and which comes next.

- **Steps marked "Ask the user":** ask, then stop and wait for the answer. Use your harness's question tool if it has one, and otherwise end your turn with the question. Do not guess the answer or continue without it.
- **If you are a subagent:** you were started by another agent and cannot reach the user. Follow the "As a subagent" line in each step instead of asking. Report every default you chose to the agent that started you.

{{ children | trim }}
