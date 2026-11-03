## {{ title | default("Subagent flow") }}{% if name is defined %}: {{ name }}{% endif %}

<!-- anchor: {{ id | default("subagent-flow") }} -->

You are the orchestrator. Run the stages below in order, and delegate each one to a subagent. Keep your own work to coordinating and checking results, so that your context stays clear.

- **Give each subagent everything it needs.** It starts with none of this conversation, so its prompt must state the goal, the inputs, the exact output you want, and when to stop.
- **Do not guess a subagent's result.** Wait for it, read it, and check it against the stage's "Done when" before moving on.
- **Run a stage in parallel only if it says so.** Parallel subagents must not change the same files.
- **Subagents cannot reach the user.** Questions that need the user are for you to ask between stages. A subagent makes a reasonable choice, and reports it.
- **If a stage fails,** retry it once with what you learned added to the prompt. If it fails again, stop and report what happened, rather than continuing on a bad result.
- **If your harness cannot run subagents,** do each stage yourself, in order, and say so once.

{{ children | trim }}

When every stage is done, combine the results and report them yourself. Never pass a subagent's text on unchecked.
