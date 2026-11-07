// Package arch holds tests of the code's structure, in the spirit of ArchUnit: which packages
// may depend on which, and what a package may never do. They keep the design true as the
// code grows, which review alone does not.
package arch

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

const module = "github.com/Grano22/aiculator/"

type pkg struct {
	ImportPath string
	Dir        string
	Imports    []string
	GoFiles    []string
}

func load(t *testing.T) map[string]pkg {
	t.Helper()
	cmd := exec.Command("go", "list", "-json", module+"...")
	cmd.Dir = "../.."
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	all := map[string]pkg{}
	dec := json.NewDecoder(out)
	for {
		var p pkg
		if err := dec.Decode(&p); err == io.EOF {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		all[strings.TrimPrefix(p.ImportPath, module)] = p
	}
	if err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}
	if len(all) < 20 {
		t.Fatalf("found only %d packages", len(all))
	}
	return all
}

// local returns the module's own packages that p imports.
func local(p pkg) []string {
	var out []string
	for _, i := range p.Imports {
		if rest, ok := strings.CutPrefix(i, module); ok {
			out = append(out, rest)
		}
	}
	return out
}

// The rules, written as what each package may use. A package left out may use anything below
// cmd, which keeps new code cheap to add while the important layers stay fixed.
var allowed = map[string][]string{
	// The state machine is pure: no I/O, no clock, nothing of ours.
	"internal/core": {},
	// Leaves, usable from anywhere, depending on nothing of ours.
	"internal/meta": {}, "internal/export": {}, "internal/parallel": {}, "internal/prompt": {},
	"internal/harness": {}, "internal/vlog": {}, "pkg/observe": {},
	"internal/store":  {"internal/core", "internal/meta"},
	"internal/engine": {"internal/core", "internal/store", "internal/vlog", "pkg/observe"},
	// The code graph knows nothing of the swarm; shared memory reads the store and git beside
	// it, like backups.
	"internal/codegraph": {},
	"internal/memory":    {"internal/codegraph", "internal/core", "internal/store"},
	// Backups read the store beside the swarm, and are never on its path.
	"internal/backup": {"internal/core", "internal/meta", "internal/store"},
	"internal/swarm":  {"internal/core", "internal/engine", "internal/meta", "internal/store", "internal/vlog", "internal/work", "pkg/observe"},
	// The API knows nothing of connectors or drivers: connectors reach it through an interface.
	"internal/api": {"internal/auth", "internal/core", "internal/engine", "internal/meta", "internal/store", "internal/swarm", "internal/vlog", "pkg/observe"},
	// Dashboards read snapshots and nothing else.
	"internal/tui":   {"pkg/observe"},
	"internal/realm": {"internal/client", "pkg/observe"},
	// The public plugin API must not leak internals, or it could never stay stable.
	"pkg/plugin": {"pkg/observe"},
	// Plugins see only the public API.
	"plugins/webdash":        {"pkg/plugin"},
	"examples/plugins/hello": {"pkg/plugin"},
}

func TestEachPackageUsesOnlyWhatItMay(t *testing.T) {
	all := load(t)
	for name, may := range allowed {
		p, ok := all[name]
		if !ok {
			t.Errorf("%s no longer exists: update the rules", name)
			continue
		}
		for _, i := range local(p) {
			if !slices.Contains(may, i) {
				t.Errorf("%s imports %s, which it may not", name, i)
			}
		}
	}
}

// Side effects stay to the side. The core layers must never depend on connectors, drivers or
// anything else that talks to the outside, so that none of it can slow or break a claim.
func TestCoreLayersDoNotReachOutward(t *testing.T) {
	all := load(t)
	outward := []string{"internal/memory", "internal/codegraph", "internal/backup", "internal/connect", "internal/driver", "internal/api", "internal/realm", "internal/tui", "internal/export", "internal/parallel", "plugins/webdash"}
	for _, name := range []string{"internal/core", "internal/store", "internal/engine", "internal/swarm", "internal/meta"} {
		deps := map[string]bool{}
		var walk func(string)
		walk = func(n string) {
			for _, i := range local(all[n]) {
				if !deps[i] {
					deps[i] = true
					walk(i)
				}
			}
		}
		walk(name)
		for _, o := range outward {
			if deps[o] {
				t.Errorf("%s depends on %s, directly or through others", name, o)
			}
		}
	}
}

func TestNothingImportsInternalFromPkg(t *testing.T) {
	for name, p := range load(t) {
		if !strings.HasPrefix(name, "pkg/") && !strings.HasPrefix(name, "plugins/") && !strings.HasPrefix(name, "examples/") {
			continue
		}
		for _, i := range local(p) {
			if strings.HasPrefix(i, "internal/") {
				t.Errorf("%s imports %s: public code must not depend on internals", name, i)
			}
		}
	}
}

// calls reports where files in a package call any of these functions, ignoring tests.
func calls(t *testing.T, p pkg, banned map[string][]string) []string {
	t.Helper()
	var found []string
	fset := token.NewFileSet()
	for _, name := range p.GoFiles {
		f, err := parser.ParseFile(fset, filepath.Join(p.Dir, name), nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatal(err)
		}
		aliases := map[string]string{}
		for _, imp := range f.Imports {
			path := strings.Trim(imp.Path.Value, `"`)
			local := filepath.Base(path)
			if imp.Name != nil {
				local = imp.Name.Name
			}
			aliases[local] = path
		}
		ast.Inspect(f, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			id, ok := sel.X.(*ast.Ident)
			if !ok {
				return true
			}
			if fns, ok := banned[aliases[id.Name]]; ok && (len(fns) == 0 || slices.Contains(fns, sel.Sel.Name)) {
				found = append(found, fset.Position(sel.Pos()).String()+": "+aliases[id.Name]+"."+sel.Sel.Name)
			}
			return true
		})
	}
	return found
}

// core.Apply must give the same answer every time, which is what lets it be fuzzed, model
// checked and replayed. So it may not read a clock, draw a random number, or touch the system.
func TestCoreIsDeterministic(t *testing.T) {
	core := load(t)["internal/core"]
	for _, c := range calls(t, core, map[string][]string{
		"time":         {"Now", "Since", "Until", "Sleep", "After", "Tick", "NewTimer", "NewTicker"},
		"math/rand":    nil,
		"math/rand/v2": nil,
		"crypto/rand":  nil,
		"os":           nil,
		"net":          nil,
		"net/http":     nil,
		"os/exec":      nil,
	}) {
		t.Errorf("core must stay deterministic: %s", c)
	}
}

// Output goes through the logger, which removes credentials. A stray print could carry one.
func TestLibrariesDoNotPrint(t *testing.T) {
	for name, p := range load(t) {
		if !strings.HasPrefix(name, "internal/") || name == "internal/prompt" {
			continue // prompt exists to write to the terminal.
		}
		for _, c := range calls(t, p, map[string][]string{"fmt": {"Print", "Printf", "Println"}, "log": nil}) {
			t.Errorf("%s: print through vlog, which redacts secrets", c)
		}
	}
}
