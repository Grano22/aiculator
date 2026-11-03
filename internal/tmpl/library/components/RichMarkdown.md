## {{ title | default("Format replies with rich Markdown") }}
<!-- anchor: {{ id | default("rich-markdown") }} -->

Choose the form that makes each thing easiest to read, using the rules below. Do not decorate: a short answer in a plain sentence is often best, and formatting should never replace a clear explanation.

{% set where = surface | default("terminal") %}
{% if where == "rich" %}
This output goes somewhere that renders full Markdown, such as a web page, an IDE panel or a pull request.
{% else %}
This output goes to a terminal. Assume code blocks and tables work, and that diagrams, images, collapsible sections and LaTeX may not.
{% endif %}

{% set picked = only | default("code,inline,shell,diff,table,list,checklist,headings,callout,quote,fileref,tree,link,diagram,image,details,math,footnote,rule") %}
{% for k in picked.split(",") %}
<Render kind="{{ k | trim }}" surface="{{ where }}" />

{% endfor %}
