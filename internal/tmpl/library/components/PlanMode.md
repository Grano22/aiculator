## {{ title | default("Plan first, and get approval") }}
<!-- anchor: {{ id | default("plan-mode") }} -->

Do not change anything until the user has approved a plan.

{% if harness.plan_enter %}
Call `{{ harness.plan_enter }}` before you start. Investigate without changing files, then present your plan with `{{ harness.plan_exit }}`, which asks the user to approve it.
{% elif harness.id == "generic" %}
Use your harness's plan mode if it has one: `EnterPlanMode` and `ExitPlanMode` in Claude Code, `enter_plan_mode` and `exit_plan_mode` in Gemini CLI.
{% else %}
Write your plan as a numbered list in your reply, then stop and wait for approval.
{% endif %}
{% if children is defined and children | trim %}

The plan must cover:

{{ children | trim }}
{% endif %}

- Say which files will change, how you will verify the result, and what could go wrong.
- If the user asks for changes, revise the plan and ask again. Do not start on a partial approval.
- **If you have no plan mode:** write the plan as a numbered list, ask for approval, and end your turn.
- **If you are a subagent:** {{ subagent | default("you cannot get approval, so report your plan and change nothing, unless your task says to make the changes.") }}
