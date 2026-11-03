## Subagents
<!-- anchor: subagents -->

{% set mode = use | default("when") %}
{% if mode == "always" %}
Delegate this work to subagents wherever your harness can run them. Do not do it in the main session.
{% elif mode == "never" %}
Do not start subagents for this work. Do all of it yourself, in this session.
{% else %}
Decide per piece of work, using these rules. If neither rule applies, do the work yourself.
{% endif %}
{% if when is defined %}

- **Use a subagent when:** {{ when }}
{% endif %}
{% if not_when is defined %}
- **Do not use a subagent when:** {{ not_when }}
{% endif %}
{% if max is defined %}
- **Run at most {{ max }} at once.** Wait for one to finish before starting another.
{% endif %}
{% if children is defined and children | trim %}

{{ children | trim }}
{% endif %}

If your harness has no way to run subagents, do the work in order yourself and say so once.
