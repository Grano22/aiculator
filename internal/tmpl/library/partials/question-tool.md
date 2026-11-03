{% if harness.question_tool == "AskUserQuestion" %}
Use the `AskUserQuestion` tool. It takes 1 to 4 questions, each with a `header` of at most 12 characters and 2 to 4 options. Each option has a `label` of 1 to 5 words and a `description`. It adds an "Other" choice by itself, so do not add one.
{% elif harness.question_tool == "ask_user" %}
Use the `ask_user` tool. It takes 1 to 4 questions, each with a `header` of at most 16 characters, `type: "choice"`, and 2 to 4 options. Each option has a `label` of 1 to 5 words and a `description`.
{% elif harness.question_tool == "question" %}
Use the `question` tool. Each question has a header, the question text and a list of options, and the user can also type their own answer.
{% else %}
Use your harness's question tool if it has one: `AskUserQuestion` in Claude Code, `question` in OpenCode, `ask_user` in Gemini CLI.
{% endif %}
