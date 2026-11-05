# Export

`aiculator export` writes tasks and other data to a file. You choose what goes in, and the format.

```sh
aiculator export                       # All tasks, as JSON
aiculator export -o tasks.xlsx         # The file name picks the format
aiculator export --format html -o report.html
```

| Format | Use `--format` | Or a file ending in |
|---|---|---|
| JSON, the default | `json` | `.json` |
| YAML | `yaml` | `.yaml`, `.yml` |
| CSV | `csv` | `.csv` |
| Tab-separated | `tsv` | `.tsv` |
| Excel | `xlsx` | `.xlsx` |
| An HTML table | `html` | `.html`, `.htm` |
| A Markdown table | `md` | `.md` |

## What to export

Tasks are the default. You can also export `agents`, `usage`, `flow` or `realms`:

```sh
aiculator export realms --format md
aiculator export agents -o agents.csv
```

JSON and YAML keep the whole record, and the table formats start with the most useful columns. To choose your own, use `--fields`. Dots reach inside, and `--list-fields` shows what exists:

```sh
aiculator export --list-fields
aiculator export --fields id,title,labels.team,owner -o tasks.csv
aiculator export --with-meta --fields id,meta.story_points -o points.xlsx
```

`--with-meta` adds each task's [properties](task-data.md) as `meta.NAME`. It costs one request per task.

## Which rows

```sh
aiculator export --state running,review
aiculator export --label team=core
aiculator export --where tier=2
aiculator export --sort -priority --limit 10
```

## Across projects

`--all` includes tasks from every running [realm](realms.md), with a `realm` column. `--realm NAME` exports just one, from any folder:

```sh
aiculator export --all -o everything.xlsx
aiculator --realm shop export -o shop.csv
```

## Opening an export safely

Task titles and specs can come from agents, or from tickets anyone could edit, and an export often gets opened somewhere that acts on what it reads. So every value is treated as text:

| Format | What protects you |
|---|---|
| CSV, TSV | A value a spreadsheet would run as a formula, such as `=HYPERLINK(...)`, starts with a `'` |
| Excel | Every text cell is stored as a string, never as a formula |
| HTML | Every value is escaped, and the page's policy stops anything in it from running |
| Markdown | Values can't end their cell or form links or tags |

That leading `'` means a CSV value can differ slightly from the original. For an exact copy of the data, use JSON or YAML.

An empty field shows as blank. A task's record leaves out values that are zero, so a priority of 0 is blank too.
