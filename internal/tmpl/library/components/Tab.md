- **{{ header }}**: {{ question }}{{ " " }}{% if multi is defined and multi == "true" %}*(several can be chosen)*{% else %}*(one can be chosen)*{% endif %}

{{ children | default("") | trim | indent(2, true) }}
