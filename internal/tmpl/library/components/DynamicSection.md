## {{ title | default("Current state") }}
<!-- anchor: {{ id }} -->

**This section is alive: keep it true.** Update it {{ update | default("whenever you change something it describes, in the same step as the change") }}. Treat a stale section as a bug.

{% if file is defined %}
It lives in `{{ file }}`, between the marker comments `<!-- dynamic:{{ id }}:start -->` and `<!-- dynamic:{{ id }}:end -->`. Create the file and markers if they do not exist yet.

{% endif %}
- Change only what lies between the markers, and leave the markers themselves in place.
- Replace what is out of date instead of adding to it, so that this always reads as the present state and never as a history.
{% if keep is defined %}
- It must always cover: {{ keep }}.
{% endif %}
- If you are a subagent, report what should change to the agent that started you, unless you were told to edit it.
{% if file is not defined %}

<!-- dynamic:{{ id }}:start -->
{{ children | default("Nothing recorded yet.") | trim }}
<!-- dynamic:{{ id }}:end -->
{% endif %}
