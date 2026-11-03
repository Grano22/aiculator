## Output format
<!-- anchor: output-format -->

{% if repeat | default("always") == "always" %}
Every reply must use exactly this format, including short ones and replies late in a long session. Before you send a reply, compare it with this format and fix any difference.
{% else %}
{{ repeat }} use exactly this format:
{% endif %}

```text
{{ children | trim }}
```
