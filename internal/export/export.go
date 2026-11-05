// Package export writes records as JSON, YAML, CSV, TSV, Excel, HTML or Markdown.
//
// Task titles and specs can come from agents or from outside systems, and an export is
// often opened somewhere that acts on what it reads. So every format here treats a value as
// text: HTML is escaped, Excel cells are stored as strings, and in CSV a value that a
// spreadsheet would run as a formula is neutralized.
package export

import (
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"maps"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"unicode"

	"go.yaml.in/yaml/v3"
)

// Formats lists what Write accepts.
var Formats = []string{"json", "yaml", "csv", "tsv", "xlsx", "html", "md"}

// Record is one row, in the shape of its JSON form.
type Record = map[string]any

// FormatFor infers a format from a file name, or returns "" if the name does not say.
func FormatFor(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".json":
		return "json"
	case ".yaml", ".yml":
		return "yaml"
	case ".csv":
		return "csv"
	case ".tsv":
		return "tsv"
	case ".xlsx":
		return "xlsx"
	case ".html", ".htm":
		return "html"
	case ".md", ".markdown":
		return "md"
	}
	return ""
}

// Lookup finds a value by a dotted path, such as labels.team. A key that itself holds the
// dots wins, so an exact name is never shadowed.
func Lookup(r Record, path string) (any, bool) {
	if v, ok := r[path]; ok {
		return v, true
	}
	var cur any = r
	for _, part := range strings.Split(path, ".") {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		if cur, ok = m[part]; !ok {
			return nil, false
		}
	}
	return cur, true
}

// FieldNames lists every path that appears in these records, including labels.x and meta.x.
func FieldNames(records []Record) []string {
	seen := map[string]bool{}
	var walk func(prefix string, m map[string]any)
	walk = func(prefix string, m map[string]any) {
		for k, v := range m {
			name := prefix + k
			seen[name] = true
			if inner, ok := v.(map[string]any); ok {
				walk(name+".", inner)
			}
		}
	}
	for _, r := range records {
		walk("", r)
	}
	return slices.Sorted(maps.Keys(seen))
}

// Select keeps these fields, flat, under their dotted names.
func Select(records []Record, fields []string) []Record {
	out := make([]Record, len(records))
	for i, r := range records {
		row := make(Record, len(fields))
		for _, f := range fields {
			if v, ok := Lookup(r, f); ok {
				row[f] = v
			} else {
				row[f] = nil
			}
		}
		out[i] = row
	}
	return out
}

// Text renders a value for a cell.
func Text(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case bool:
		return strconv.FormatBool(x)
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	case int:
		return strconv.Itoa(x)
	case int64:
		return strconv.FormatInt(x, 10)
	}
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprint(v)
	}
	return string(b)
}

// Options shapes the output.
type Options struct {
	Format  string
	Columns []string // Column order for the table formats. Empty means every field, sorted.
	Title   string   // Shown by HTML, and names the Excel sheet.
}

// Write writes the records in the chosen format.
func Write(w io.Writer, records []Record, o Options) error {
	cols := o.Columns
	if len(cols) == 0 {
		seen := map[string]bool{}
		for _, r := range records {
			for k := range r {
				seen[k] = true
			}
		}
		cols = slices.Sorted(maps.Keys(seen))
	}
	switch o.Format {
	case "json":
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		enc.SetEscapeHTML(false)
		if records == nil {
			records = []Record{}
		}
		return enc.Encode(records)
	case "yaml":
		enc := yaml.NewEncoder(w)
		enc.SetIndent(2)
		defer enc.Close()
		if records == nil {
			records = []Record{}
		}
		return enc.Encode(records)
	case "csv":
		return writeDelimited(w, ',', records, cols)
	case "tsv":
		return writeDelimited(w, '\t', records, cols)
	case "xlsx":
		return writeXLSX(w, records, cols, o.Title)
	case "html":
		return writeHTML(w, records, cols, o.Title)
	case "md":
		return writeMarkdown(w, records, cols)
	}
	return fmt.Errorf("unknown format %q: use %s", o.Format, strings.Join(Formats, ", "))
}

// GuardFormula stops a spreadsheet from running text as a formula. Excel and others treat a
// cell starting with these as one when they open a CSV, and a ticket title could be
// =HYPERLINK(...) or worse. A leading quote is the usual way to say "this is text".
func GuardFormula(s string) string {
	if s == "" {
		return s
	}
	switch s[0] {
	case '=', '+', '-', '@', '\t', '\r':
		return "'" + s
	}
	return s
}

func writeDelimited(w io.Writer, comma rune, records []Record, cols []string) error {
	cw := csv.NewWriter(w)
	cw.Comma = comma
	if err := cw.Write(cols); err != nil {
		return err
	}
	row := make([]string, len(cols))
	for _, r := range records {
		for i, c := range cols {
			v := r[c]
			text := Text(v)
			if _, isString := v.(string); isString || v == nil {
				text = GuardFormula(text)
			} else if _, isNumber := v.(float64); !isNumber {
				if _, isBool := v.(bool); !isBool {
					text = GuardFormula(text) // A list or object rendered as text.
				}
			}
			row[i] = text
		}
		if err := cw.Write(row); err != nil {
			return err
		}
	}
	cw.Flush()
	return cw.Error()
}

// mdCell keeps a value on one line, and keeps it from ending the cell or forming markup.
func mdCell(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)
	return strings.NewReplacer(`\`, `\\`, `|`, `\|`, `<`, `&lt;`, `>`, `&gt;`, "`", "\\`", "[", `\[`, "]", `\]`).Replace(s)
}

func writeMarkdown(w io.Writer, records []Record, cols []string) error {
	if len(cols) == 0 {
		_, err := io.WriteString(w, "No records.\n")
		return err
	}
	var b strings.Builder
	head := make([]string, len(cols))
	for i, c := range cols {
		head[i] = mdCell(c)
	}
	b.WriteString("| " + strings.Join(head, " | ") + " |\n|" + strings.Repeat(" --- |", len(cols)) + "\n")
	for _, r := range records {
		cells := make([]string, len(cols))
		for i, c := range cols {
			cells[i] = mdCell(Text(r[c]))
		}
		b.WriteString("| " + strings.Join(cells, " | ") + " |\n")
	}
	_, err := io.WriteString(w, b.String())
	return err
}

// html/template escapes every value, so a title cannot become markup. The page loads nothing
// from elsewhere, and its policy stops anything inside it from running.
var htmlPage = template.Must(template.New("export").Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta http-equiv="Content-Security-Policy" content="default-src 'none'; style-src 'unsafe-inline'">
<title>{{.Title}}</title>
<style>
:root { color-scheme: light dark; --line: #d0d7de; --head: #f6f8fa; }
@media (prefers-color-scheme: dark) { :root { --line: #30363d; --head: #161b22; } }
body { font: 14px/1.5 system-ui, sans-serif; margin: 24px; }
h1 { font-size: 18px; margin: 0 0 4px; }
p { margin: 0 0 16px; opacity: .7; }
.wrap { overflow-x: auto; }
table { border-collapse: collapse; }
th, td { border: 1px solid var(--line); padding: 6px 10px; text-align: left; vertical-align: top; }
th { background: var(--head); position: sticky; top: 0; white-space: nowrap; }
</style>
</head>
<body>
<h1>{{.Title}}</h1>
<p>{{len .Rows}} rows</p>
<div class="wrap"><table>
<thead><tr>{{range .Cols}}<th scope="col">{{.}}</th>{{end}}</tr></thead>
<tbody>{{range .Rows}}
<tr>{{range .}}<td>{{.}}</td>{{end}}</tr>{{end}}
</tbody>
</table></div>
</body>
</html>
`))

func writeHTML(w io.Writer, records []Record, cols []string, title string) error {
	rows := make([][]string, len(records))
	for i, r := range records {
		rows[i] = make([]string, len(cols))
		for j, c := range cols {
			rows[i][j] = Text(r[c])
		}
	}
	if title == "" {
		title = "Export"
	}
	return htmlPage.Execute(w, struct {
		Title string
		Cols  []string
		Rows  [][]string
	}{title, cols, rows})
}

// ErrNeedsFile means a binary format was asked for on a terminal.
var ErrNeedsFile = errors.New("xlsx is a binary format: choose a file with -o")
