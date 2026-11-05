package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/Grano22/aiculator/internal/core"
	"github.com/Grano22/aiculator/internal/export"
	"github.com/Grano22/aiculator/internal/meta"
	"github.com/Grano22/aiculator/internal/realm"
	"github.com/Grano22/aiculator/pkg/observe"
)

// The columns a table shows when none are asked for. JSON and YAML keep the whole record.
var exportColumns = map[string][]string{
	"tasks":  {"id", "title", "state", "tier", "priority", "owner", "attempt", "failures", "labels", "created_at", "updated_at"},
	"agents": {"id", "tier", "state", "task", "done", "failed", "tokens", "cost"},
	"usage":  nil,
	"flow":   nil,
	"realms": {"name", "state", "ready", "working", "review", "done", "parked", "agents", "path"},
}

func toRecords(v any) ([]export.Record, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var out []export.Record
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func snapshotOf(ctx context.Context) (observe.Snapshot, error) {
	var s observe.Snapshot
	c, err := adminClient()
	if err != nil {
		return s, fmt.Errorf("%w; start it with: aiculator up", err)
	}
	return s, c.Get(ctx, "/v1/observe", &s)
}

// metaValue reduces a property to what fits a cell.
func metaValue(p meta.Prop) any {
	switch {
	case p.Blob != nil:
		return p.Blob.Name
	case p.Resource != nil:
		return p.Resource.URL
	}
	return p.Value
}

func exportTasks(ctx context.Context, all, withMeta bool) ([]export.Record, error) {
	if !all {
		c, err := adminClient()
		if err != nil {
			return nil, fmt.Errorf("%w; start it with: aiculator up", err)
		}
		return fetchTasks(ctx, c, "", withMeta)
	}
	views, err := realm.Gather(ctx, nil)
	if err != nil {
		return nil, err
	}
	var out []export.Record
	for _, v := range views {
		if !v.Live {
			continue
		}
		c, err := realm.Local(v.Realm)
		if err != nil {
			continue
		}
		rows, err := fetchTasks(ctx, c, v.Realm.Name, withMeta)
		if err != nil {
			fmt.Fprintf(os.Stderr, "skipped realm %s: %v\n", v.Realm.Name, err)
			continue
		}
		out = append(out, rows...)
	}
	return out, nil
}

type taskGetter interface {
	Get(ctx context.Context, path string, out any) error
}

func fetchTasks(ctx context.Context, c taskGetter, realmName string, withMeta bool) ([]export.Record, error) {
	var ts []core.Task
	if err := c.Get(ctx, "/v1/tasks", &ts); err != nil {
		return nil, err
	}
	rows, err := toRecords(ts)
	if err != nil {
		return nil, err
	}
	for i, r := range rows {
		if realmName != "" {
			r["realm"] = realmName
		}
		if withMeta {
			var m meta.Meta
			if err := c.Get(ctx, "/v1/tasks/"+ts[i].ID+"/meta", &m); err != nil {
				return nil, err
			}
			props := make(map[string]any, len(m.Props))
			for k, p := range m.Props {
				props[k] = metaValue(p)
			}
			r["meta"] = props
		}
	}
	return rows, nil
}

func exportRealms(ctx context.Context) ([]export.Record, error) {
	views, err := realm.Gather(ctx, nil)
	if err != nil {
		return nil, err
	}
	out := make([]export.Record, 0, len(views))
	for _, v := range views {
		r := export.Record{"name": v.Realm.Name, "path": v.Realm.Path, "state": "stopped"}
		switch {
		case v.Missing:
			r["state"] = "missing"
		case v.Live:
			r["state"] = "running"
			st := v.Snapshot.States
			r["ready"], r["working"] = float64(st["ready"]), float64(st["claimed"]+st["running"])
			r["review"], r["done"] = float64(st["review"]+st["merging"]), float64(st["done"])
			r["parked"], r["agents"] = float64(st["parked"]), float64(len(v.Snapshot.Agents))
		}
		out = append(out, r)
	}
	return out, nil
}

func exportCmd() *cobra.Command {
	var (
		output, format, sortBy string
		fields, states, labels []string
		where                  []string
		limit                  int
		withMeta, all, list    bool
	)
	cmd := &cobra.Command{
		Use:   "export [tasks|agents|usage|flow|realms]",
		Short: "Write tasks or other data to a file: JSON, YAML, CSV, TSV, Excel, HTML or Markdown",
		Long: `Exports what you choose, in the format you choose. Tasks are the default, and JSON is the
default format, unless the file name in -o says otherwise.

Values are always treated as text: HTML is escaped, Excel cells are stored as strings, and in
CSV a value that a spreadsheet would run as a formula starts with a quote.`,
		Example: `  aiculator export                                   # All tasks, as JSON
  aiculator export -o tasks.xlsx                     # The file name picks the format
  aiculator export --format html -o report.html --state running,review
  aiculator export --fields id,title,labels.team,owner --format csv
  aiculator export --label team=core --sort priority -o core.yaml
  aiculator export --with-meta --fields id,meta.points -o points.csv
  aiculator export --all -o everything.xlsx          # Tasks from every realm
  aiculator export --list-fields                     # What can be selected
  aiculator export realms --format md`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			what := "tasks"
			if len(args) == 1 {
				what = args[0]
			}
			defaults, known := exportColumns[what]
			if !known {
				return fmt.Errorf("cannot export %q: use tasks, agents, usage, flow or realms", what)
			}
			if format == "" {
				format = export.FormatFor(output)
			}
			if format == "" {
				if output != "" && output != "-" {
					return fmt.Errorf("the file name %s does not say the format: use --format %s", output, strings.Join(export.Formats, ", "))
				}
				format = "json"
			}
			if !slices.Contains(export.Formats, format) {
				return fmt.Errorf("unknown format %q: use %s", format, strings.Join(export.Formats, ", "))
			}
			if format == "xlsx" && (output == "" || output == "-") && term.IsTerminal(int(os.Stdout.Fd())) {
				return export.ErrNeedsFile
			}
			if (withMeta || all) && what != "tasks" {
				return fmt.Errorf("--with-meta and --all work with tasks, not %s", what)
			}
			ctx := cmd.Context()
			var records []export.Record
			var err error
			switch what {
			case "tasks":
				records, err = exportTasks(ctx, all, withMeta)
			case "realms":
				records, err = exportRealms(ctx)
			default:
				var s observe.Snapshot
				if s, err = snapshotOf(ctx); err == nil {
					switch what {
					case "agents":
						records, err = toRecords(s.Agents)
					case "usage":
						records, err = toRecords(s.Usage.ByTier)
					default:
						records, err = toRecords(s.Flow)
					}
				}
			}
			if err != nil {
				return err
			}
			if list {
				for _, f := range export.FieldNames(records) {
					fmt.Println(f)
				}
				return nil
			}
			if records, err = filterRecords(records, states, labels, where); err != nil {
				return err
			}
			if sortBy != "" {
				sortRecords(records, sortBy)
			}
			if limit > 0 && len(records) > limit {
				records = records[:limit]
			}
			columns := defaults
			if all && what == "tasks" {
				columns = append([]string{"realm"}, columns...)
			}
			tabular := format != "json" && format != "yaml"
			switch {
			case len(fields) > 0:
				known := export.FieldNames(records)
				for _, f := range fields {
					if len(records) > 0 && !slices.Contains(known, f) {
						fmt.Fprintf(os.Stderr, "warning: no record has a field named %s; see --list-fields\n", f)
					}
				}
				records, columns = export.Select(records, fields), fields
			case tabular && len(columns) > 0:
				records = export.Select(records, columns)
			default:
				columns = nil
			}
			var buf bytes.Buffer
			if err := export.Write(&buf, records, export.Options{Format: format, Columns: columns, Title: "Aiculator " + what}); err != nil {
				return err
			}
			if output == "" || output == "-" {
				_, err = os.Stdout.Write(buf.Bytes())
				return err
			}
			if err := writeFileAtomic(output, buf.Bytes()); err != nil {
				return err
			}
			fmt.Printf("%d rows written to %s\n", len(records), output)
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVarP(&output, "output", "o", "", "file to write; standard output if empty")
	f.StringVar(&format, "format", "", "json, yaml, csv, tsv, xlsx, html or md; from the file name, or json")
	f.StringSliceVar(&fields, "fields", nil, "what to include, such as id,title,labels.team; see --list-fields")
	f.StringSliceVar(&states, "state", nil, "only these states, such as running,review")
	f.StringArrayVar(&labels, "label", nil, "only with this label, as name=value")
	f.StringArrayVar(&where, "where", nil, "only where a field has this value, as field=value")
	f.StringVar(&sortBy, "sort", "", "sort by this field; -field for descending")
	f.IntVar(&limit, "limit", 0, "stop after this many rows")
	f.BoolVar(&withMeta, "with-meta", false, "add each task's properties, as meta.NAME; costs one request per task")
	f.BoolVar(&all, "all", false, "tasks from every running realm, with a realm column")
	f.BoolVar(&list, "list-fields", false, "list what can be selected, and write nothing")
	return cmd
}

func filterRecords(records []export.Record, states, labels, where []string) ([]export.Record, error) {
	type test struct{ field, want string }
	var tests []test
	for _, l := range labels {
		k, v, ok := strings.Cut(l, "=")
		if !ok {
			return nil, fmt.Errorf("--label %q: use name=value", l)
		}
		tests = append(tests, test{"labels." + k, v})
	}
	for _, w := range where {
		k, v, ok := strings.Cut(w, "=")
		if !ok {
			return nil, fmt.Errorf("--where %q: use field=value", w)
		}
		tests = append(tests, test{k, v})
	}
	return slices.DeleteFunc(records, func(r export.Record) bool {
		if len(states) > 0 {
			s, _ := export.Lookup(r, "state")
			if !slices.Contains(states, export.Text(s)) {
				return true
			}
		}
		for _, t := range tests {
			v, ok := export.Lookup(r, t.field)
			if !ok || export.Text(v) != t.want {
				return true
			}
		}
		return false
	}), nil
}

func sortRecords(records []export.Record, by string) {
	desc := strings.HasPrefix(by, "-")
	by = strings.TrimPrefix(by, "-")
	slices.SortStableFunc(records, func(a, b export.Record) int {
		x, _ := export.Lookup(a, by)
		y, _ := export.Lookup(b, by)
		c := 0
		xn, xok := x.(float64)
		yn, yok := y.(float64)
		switch {
		case xok && yok && xn < yn:
			c = -1
		case xok && yok && xn > yn:
			c = 1
		case !xok || !yok:
			c = strings.Compare(export.Text(x), export.Text(y))
		}
		if desc {
			return -c
		}
		return c
	})
}

// writeFileAtomic means a failure leaves any earlier file whole.
func writeFileAtomic(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".export-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return errors.Join(err, errors.New("nothing was written"))
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
