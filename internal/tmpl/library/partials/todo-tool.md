{% if harness.todo_tool == "TaskCreate" %}
Use `TaskCreate` to add each step and `TaskUpdate` to change its status. Claude Code shows the list live. On some models these tools must be switched on, so if you do not have them, use the fallback below.
{% elif harness.todo_tool == "write_todos" %}
Use the `write_todos` tool, whose `todos` argument is an array of items with a `description` and a `status`.
{% elif harness.todo_tool == "todowrite" %}
Use the `todowrite` tool to create and update the list.
{% else %}
Use your harness's task list tool if it has one: `TaskCreate` in Claude Code, `todowrite` in OpenCode, `write_todos` in Gemini CLI.
{% endif %}
