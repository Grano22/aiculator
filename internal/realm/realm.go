// Package realm gives each project on a machine a name, and a way to see them all together.
//
// A realm is one project with its own server and its own store. They are deliberately not
// merged into one server: one crash or one damaged store stays in one project, and a token
// from one project is refused by every other, since each signs with its own key. This
// package is only a directory of them, plus a read-only view across them.
package realm

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/Grano22/aiculator/internal/client"
	"github.com/Grano22/aiculator/pkg/observe"
)

// Realm is one project in the directory. It holds no secrets: a realm's tokens stay in its
// own data directory.
type Realm struct {
	Name    string    `json:"name"`
	Path    string    `json:"path"`
	DataDir string    `json:"data_dir"`
	Created time.Time `json:"created"`
}

// ErrNotFound means no realm has that name.
var ErrNotFound = errors.New("no such realm")

// Dir is where the directory lives. Each realm is one file, written atomically, so that
// servers starting at the same moment cannot damage each other's entries.
func Dir() string {
	if d := os.Getenv("AICULATOR_REALMS"); d != "" {
		return d
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".aiculator", "realms")
}

// Instance identifies a server by its data directory, so that a client can tell its own
// server from another project's that took over the same port after a crash.
func Instance(dataDir string) string {
	h := sha256.Sum256([]byte(dataDir))
	return hex.EncodeToString(h[:8])
}

func fileFor(dataDir string) string { return filepath.Join(Dir(), Instance(dataDir)+".json") }

var (
	nameRE   = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)
	unsafeRE = regexp.MustCompile(`[^a-z0-9._-]+`)
)

func slug(path string) string {
	s := strings.Trim(unsafeRE.ReplaceAllString(strings.ToLower(filepath.Base(path)), "-"), "-._")
	if s == "" {
		s = "project"
	}
	if len(s) > 48 {
		s = s[:48]
	}
	return s
}

// List returns every realm, sorted by name. A damaged entry is skipped, not fatal.
func List() ([]Realm, error) {
	dir := Dir()
	if dir == "" {
		return nil, nil
	}
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	out := make([]Realm, 0, len(entries))
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		var r Realm
		if json.Unmarshal(b, &r) == nil && r.Name != "" && r.DataDir != "" {
			out = append(out, r)
		}
	}
	slices.SortFunc(out, func(a, b Realm) int { return strings.Compare(a.Name, b.Name) })
	return out, nil
}

func write(r Realm) error {
	if err := os.MkdirAll(Dir(), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(Dir(), ".realm-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), fileFor(r.DataDir))
}

// Register records a project, and returns its realm. A project keeps the name it has. A new
// one is named after its folder, with a short suffix if another project has that name.
func Register(path, dataDir string) (Realm, error) {
	all, err := List()
	if err != nil {
		return Realm{}, err
	}
	if i := slices.IndexFunc(all, func(r Realm) bool { return r.DataDir == dataDir }); i >= 0 {
		return all[i], nil
	}
	name := slug(path)
	if slices.ContainsFunc(all, func(r Realm) bool { return r.Name == name }) {
		name += "-" + Instance(dataDir)[:4]
	}
	r := Realm{Name: name, Path: path, DataDir: dataDir, Created: time.Now().UTC()}
	return r, write(r)
}

// Get finds a realm by name.
func Get(name string) (Realm, error) {
	all, err := List()
	if err != nil {
		return Realm{}, err
	}
	names := make([]string, 0, len(all))
	for _, r := range all {
		if r.Name == name {
			return r, nil
		}
		names = append(names, r.Name)
	}
	if len(names) == 0 {
		return Realm{}, fmt.Errorf("%w: %q, and none exist yet; a project becomes one when its server starts", ErrNotFound, name)
	}
	return Realm{}, fmt.Errorf("%w: %q; these exist: %s", ErrNotFound, name, strings.Join(names, ", "))
}

// Rename gives a realm a new name.
func Rename(from, to string) error {
	if !nameRE.MatchString(to) {
		return fmt.Errorf("realm name %q: use lowercase letters, digits, dots, dashes and underscores", to)
	}
	r, err := Get(from)
	if err != nil {
		return err
	}
	if _, err := Get(to); err == nil {
		return fmt.Errorf("a realm named %s already exists", to)
	}
	r.Name = to
	return write(r)
}

// Forget removes a realm from the directory. The project and its data stay, and it comes
// back the next time its server starts.
func Forget(name string) error {
	r, err := Get(name)
	if err != nil {
		return err
	}
	return os.Remove(fileFor(r.DataDir))
}

// View is what one realm looks like right now.
type View struct {
	Realm    Realm             `json:"realm"`
	Live     bool              `json:"live"`
	Missing  bool              `json:"missing,omitempty"` // Its folder is gone.
	Error    string            `json:"error,omitempty"`
	Snapshot *observe.Snapshot `json:"snapshot,omitempty"`
	// Link opens this realm's own dashboard, when asked for.
	Link string `json:"link,omitempty"`
}

// Local connects to a realm's server as its owner, using the files in its data directory.
func Local(r Realm) (*client.Client, error) {
	addr, err := os.ReadFile(filepath.Join(r.DataDir, "addr"))
	if err != nil {
		return nil, errors.New("not running")
	}
	tok, err := os.ReadFile(filepath.Join(r.DataDir, "admin.token"))
	if err != nil {
		return nil, errors.New("not running")
	}
	return client.New(strings.TrimSpace(string(addr)), strings.TrimSpace(string(tok))), nil
}

// perRealm bounds how long one stuck server can hold up the view of all the others.
const perRealm = 2 * time.Second

// Gather reads every realm at once. A realm that is down shows as down, and never delays or
// fails the rest. Each server checks the token itself, so a stale address that now reaches
// another project's server gets a refusal, and the realm shows as not running.
func Gather(ctx context.Context, each func(context.Context, Realm, *View)) ([]View, error) {
	all, err := List()
	if err != nil {
		return nil, err
	}
	out := make([]View, len(all))
	var wg sync.WaitGroup
	for i, r := range all {
		out[i].Realm = r
		if _, err := os.Stat(r.Path); err != nil {
			out[i].Missing = true
			continue
		}
		wg.Go(func() {
			c, cancel := context.WithTimeout(ctx, perRealm)
			defer cancel()
			cl, err := Local(r)
			if err != nil {
				out[i].Error = err.Error()
				return
			}
			var s observe.Snapshot
			if err := cl.Get(c, "/v1/observe", &s); err != nil {
				out[i].Error = "not running"
				return
			}
			out[i].Live, out[i].Snapshot = true, &s
			if each != nil {
				each(c, r, &out[i])
			}
		})
	}
	wg.Wait()
	return out, nil
}
