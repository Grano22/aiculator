## Commands: timeouts and retries
<!-- anchor: backoff -->

{% set n = retries | default(3) %}
{% set first = base | default("2s") %}
{% set grow = factor | default(2) %}
{% set ceiling = limit | default("60s") %}
Give every command a time limit of **{{ timeout | default("120s") }}**. A command that hits the limit has failed; do not wait longer.
{% if on is defined %}

Retry only for: {{ on }}.
{% else %}

Retry only for failures that can pass on their own: timeouts, network errors, rate limits and busy locks.
{% endif %}
Do not retry a failure that will repeat, such as a failing test, a syntax error or a denied permission. Read the error and change something first.

| Attempt | Wait before it |
|---|---|
| 1 | None |
{% for row in backoff_rows(n, first, grow, ceiling) %}
| {{ row.attempt }} | {{ row.wait }} |
{% endfor %}

After the last attempt, stop retrying. Report the command, the final error and what you tried.
