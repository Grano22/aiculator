### Step {{ n }}{% if title is defined %}: {{ title }}{% endif %}

{% if id is defined %}
<!-- anchor: {{ id }} -->

{% endif %}
{{ children | default("") | trim }}
{% if ask is defined %}

**Ask the user:** {{ ask }}
{% endif %}
{% if subagent is defined %}

**As a subagent:** {{ subagent }}
{% endif %}
