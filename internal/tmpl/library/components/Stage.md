### Stage {{ n }}{% if title is defined %}: {{ title }}{% endif %}

{% if id is defined %}
<!-- anchor: {{ id }} -->

{% endif %}
{% if agent is defined %}
**Subagent:** {{ agent }}{% if parallel is defined %}. Run up to {{ parallel }} at once.{% endif %}

{% elif parallel is defined %}
**Run up to {{ parallel }} subagents at once.**

{% endif %}
{{ children | default("") | trim }}
{% if input is defined %}

**Input:** {{ input }}
{% endif %}
{% if output is defined %}

**Output:** {{ output }}
{% endif %}
{% if done is defined %}

**Done when:** {{ done }}
{% endif %}
