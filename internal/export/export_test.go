package export

import (
	"archive/zip"
	"bytes"
	"encoding/csv"
	"encoding/json"
	"io"
	"slices"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

func sample() []Record {
	return []Record{
		{"id": "a", "title": "Fix & <b>bold</b>", "priority": float64(5), "labels": map[string]any{"team": "core"}, "ok": true},
		{"id": "b", "title": "=HYPERLINK(\"http://evil\")", "priority": float64(-1), "labels": map[string]any{}, "ok": false},
	}
}

func render(t *testing.T, format string, r []Record, cols ...string) string {
	t.Helper()
	var b bytes.Buffer
	if err := Write(&b, r, Options{Format: format, Columns: cols, Title: "Tasks"}); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

func TestFormatFor(t *testing.T) {
	for name, want := range map[string]string{"a.json": "json", "a.yml": "yaml", "A.XLSX": "xlsx", "a.htm": "html", "a.md": "md", "a.tsv": "tsv", "a": "", "a.txt": ""} {
		if got := FormatFor(name); got != want {
			t.Errorf("%s: %q", name, got)
		}
	}
}

func TestLookupAndSelect(t *testing.T) {
	r := sample()
	if v, ok := Lookup(r[0], "labels.team"); !ok || v != "core" {
		t.Errorf("%v %v", v, ok)
	}
	got := Select(r, []string{"id", "labels.team", "missing"})
	if got[0]["labels.team"] != "core" || got[1]["labels.team"] != nil || len(got[0]) != 3 {
		t.Errorf("%+v", got)
	}
	names := FieldNames(r)
	for _, want := range []string{"id", "labels", "labels.team", "priority"} {
		if !slices.Contains(names, want) {
			t.Errorf("missing %s in %v", want, names)
		}
	}
}

func TestJSONAndYAMLRoundTrip(t *testing.T) {
	var j []Record
	if err := json.Unmarshal([]byte(render(t, "json", sample())), &j); err != nil {
		t.Fatal(err)
	}
	if j[0]["title"] != "Fix & <b>bold</b>" {
		t.Errorf("JSON changed text: %v", j[0]["title"])
	}
	var y []Record
	if err := yaml.Unmarshal([]byte(render(t, "yaml", sample())), &y); err != nil {
		t.Fatal(err)
	}
	if y[1]["id"] != "b" {
		t.Errorf("%+v", y)
	}
	for _, f := range []string{"json", "yaml"} {
		if got := strings.TrimSpace(render(t, f, nil)); got != "[]" {
			t.Errorf("%s with no records: %q", f, got)
		}
	}
}

func TestCSVCannotRunFormulas(t *testing.T) {
	for _, format := range []string{"csv", "tsv"} {
		r := csv.NewReader(strings.NewReader(render(t, format, sample(), "id", "title", "priority", "ok")))
		if format == "tsv" {
			r.Comma = '\t'
		}
		rows, err := r.ReadAll()
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(rows[0], []string{"id", "title", "priority", "ok"}) {
			t.Errorf("%v", rows[0])
		}
		if !strings.HasPrefix(rows[2][1], "'=") {
			t.Errorf("%s: a formula got through: %q", format, rows[2][1])
		}
		// A real negative number stays a number, or the column stops adding up.
		if rows[2][2] != "-1" {
			t.Errorf("%s: number changed: %q", format, rows[2][2])
		}
	}
	for s, want := range map[string]string{"=1": "'=1", "+1": "'+1", "-x": "'-x", "@x": "'@x", "\tx": "'\tx", "x=1": "x=1", "": ""} {
		if got := GuardFormula(s); got != want {
			t.Errorf("%q: %q", s, got)
		}
	}
}

func TestHTMLEscapesEverything(t *testing.T) {
	r := []Record{{"<script>alert(1)</script>": "<img src=x onerror=alert(1)>"}}
	var b bytes.Buffer
	if err := Write(&b, r, Options{Format: "html", Title: "</title><script>x</script>"}); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	for _, bad := range []string{"<script>", "<img"} {
		if strings.Contains(out, bad) {
			t.Errorf("%s got through:\n%s", bad, out)
		}
	}
	if !strings.Contains(out, "Content-Security-Policy") || !strings.Contains(out, "1 rows") {
		t.Errorf("%s", out)
	}
}

func TestMarkdownStaysInItsCell(t *testing.T) {
	out := render(t, "md", []Record{{"a": "x|y\nz", "b": "<script> [l](u) `c`"}}, "a", "b")
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 3 {
		t.Fatalf("a value broke the table:\n%s", out)
	}
	if !strings.Contains(lines[2], `x\|y z`) || strings.Contains(out, "<script>") || strings.Contains(out, "[l]") {
		t.Errorf("%s", out)
	}
	if got := render(t, "md", nil); !strings.Contains(got, "No records") {
		t.Errorf("%q", got)
	}
}

func TestColName(t *testing.T) {
	for i, want := range map[int]string{0: "A", 25: "Z", 26: "AA", 27: "AB", 701: "ZZ", 702: "AAA"} {
		if got := colName(i); got != want {
			t.Errorf("%d: %s", i, got)
		}
	}
}

func TestXLSXIsAWellFormedFile(t *testing.T) {
	long := strings.Repeat("x", maxCell+50)
	r := []Record{{"a": "=1+1", "n": float64(3), "b": true, "bad": "ctrl\x01char & <x>", "long": long, "list": []any{"p", "q"}}}
	var b bytes.Buffer
	if err := Write(&b, r, Options{Format: "xlsx", Columns: []string{"a", "n", "b", "bad", "long", "list", "none"}, Title: "A/B: tasks*"}); err != nil {
		t.Fatal(err)
	}
	z, err := zip.NewReader(bytes.NewReader(b.Bytes()), int64(b.Len()))
	if err != nil {
		t.Fatal(err)
	}
	if len(z.File) != 6 {
		t.Fatalf("%d parts", len(z.File))
	}
	read := func(name string) string {
		for _, f := range z.File {
			if f.Name == name {
				rc, _ := f.Open()
				defer rc.Close()
				d, _ := io.ReadAll(rc)
				return string(d)
			}
		}
		t.Fatalf("no %s", name)
		return ""
	}
	sheet := read("xl/worksheets/sheet1.xml")
	if strings.Contains(sheet, "<f>") {
		t.Error("a value became a formula")
	}
	if !strings.Contains(sheet, `t="inlineStr"><is><t xml:space="preserve">=1+1`) {
		t.Errorf("text should stay text: %.300s", sheet)
	}
	if strings.Contains(sheet, "\x01") || !strings.Contains(sheet, "&amp;") || strings.Contains(sheet, strings.Repeat("x", maxCell+1)) {
		t.Error("bad characters or length got through")
	}
	if wb := read("xl/workbook.xml"); strings.ContainsAny(strings.Split(strings.Split(wb, `name="`)[1], `"`)[0], `/:*`) {
		t.Errorf("sheet name: %s", wb)
	}
	var again bytes.Buffer
	if err := Write(&again, r, Options{Format: "xlsx", Columns: []string{"a", "n", "b", "bad", "long", "list", "none"}, Title: "A/B: tasks*"}); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(b.Bytes(), again.Bytes()) {
		t.Error("the same data gave a different file")
	}
}

func TestUnknownFormat(t *testing.T) {
	if err := Write(io.Discard, nil, Options{Format: "pdf"}); err == nil || !strings.Contains(err.Error(), "xlsx") {
		t.Errorf("%v", err)
	}
}
