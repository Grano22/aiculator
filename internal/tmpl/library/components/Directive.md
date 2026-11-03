{% set word = (level | default("must")) | upper %}
> **{{ word }}**{% if id is defined %} `{{ id }}`{% endif %}: {{ children | trim }}
