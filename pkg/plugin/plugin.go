// Package plugin lets features be compiled into the aiculator binary. A plugin registers
// itself from init, and a blank import in cmd/aiculator includes it in the build.
//
// Plugins are compiled in because Go's dynamic plugin package needs cgo, and the
// binary is static. For something that needs no recompile, any program on PATH named
// aiculator-NAME runs as "aiculator NAME", the way git does it.
package plugin

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"

	"github.com/Grano22/aiculator/pkg/observe"
)

// Plugin is the one thing every plugin implements.
type Plugin interface {
	Name() string // Lowercase letters, digits and hyphens.
	Description() string
	// Setup runs once in the server. A failure here disables this plugin only.
	Setup(h *Host) error
}

// Commander is implemented by plugins that add CLI commands.
type Commander interface {
	Commands(cli *CLI) []*cobra.Command
}

// CLI is what a plugin's commands can use.
type CLI struct {
	// ViewerToken mints a read-only token and returns the server's URL with it.
	ViewerToken func(ctx context.Context, ttl time.Duration) (url, token string, err error)
	// Snapshot reads the live dashboard data.
	Snapshot func(ctx context.Context) (observe.Snapshot, error)
	// Fleet reads every realm on this machine, as JSON that the caller passes on.
	Fleet func(ctx context.Context, ttl time.Duration) (any, error)
}

// Event is a committed task change.
type Event struct {
	TaskID string    `json:"task_id"`
	Title  string    `json:"title"`
	State  string    `json:"state"`
	Tier   int       `json:"tier"`
	Owner  string    `json:"owner,omitempty"`
	Reason string    `json:"reason,omitempty"`
	Time   time.Time `json:"time"`
}

var (
	regMu    sync.Mutex
	registry []Plugin
	nameRE   = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)
)

// Register adds a plugin. Call it from init. A bad or duplicate name is a programming
// error, so it panics at startup rather than failing later.
func Register(p Plugin) {
	regMu.Lock()
	defer regMu.Unlock()
	if !nameRE.MatchString(p.Name()) {
		panic(fmt.Sprintf("plugin name %q: use lowercase letters, digits and hyphens", p.Name()))
	}
	if slices.ContainsFunc(registry, func(x Plugin) bool { return x.Name() == p.Name() }) {
		panic(fmt.Sprintf("plugin %q is registered twice", p.Name()))
	}
	registry = append(registry, p)
}

// All returns the compiled-in plugins, sorted by name.
func All() []Plugin {
	regMu.Lock()
	defer regMu.Unlock()
	out := slices.Clone(registry)
	slices.SortFunc(out, func(a, b Plugin) int { return strings.Compare(a.Name(), b.Name()) })
	return out
}

// Router is the part of the server a plugin's routes attach to.
type Router interface {
	Handle(pattern string, h http.Handler)
}

// Host is the plugin's view of the running server.
type Host struct {
	plugin string
	router Router
	guard  func(http.Handler) http.Handler
	events *[]eventSink
	mu     *sync.Mutex

	Version  string
	Log      *slog.Logger
	Snapshot func(ctx context.Context) (observe.Snapshot, error)
}

// Prefix is where this plugin's routes live. Keeping each plugin under its own path
// means it cannot shadow the API or another plugin.
func (h *Host) Prefix() string { return "/plugins/" + h.plugin + "/" }

// Handle serves a route with no authentication, under Prefix. Use it only for what
// is safe for anyone on this machine to read, such as static assets.
func (h *Host) Handle(sub string, handler http.Handler) {
	h.router.Handle(h.Prefix()+strings.TrimPrefix(sub, "/"), h.safe(handler))
}

// HandleViewer serves a route that needs a viewer or admin token, under Prefix.
func (h *Host) HandleViewer(sub string, handler http.Handler) {
	h.router.Handle(h.Prefix()+strings.TrimPrefix(sub, "/"), h.safe(h.guard(handler)))
}

// safe keeps a panicking handler from affecting anything beyond its own request.
func (h *Host) safe(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				h.Log.Error("plugin handler panicked", "plugin", h.plugin, "panic", v)
				http.Error(w, "plugin error", http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// OnEvent subscribes to task changes. Delivery is best effort: a slow plugin misses
// events rather than slowing the server. It never blocks the swarm.
func (h *Host) OnEvent(fn func(Event)) {
	h.mu.Lock()
	defer h.mu.Unlock()
	*h.events = append(*h.events, eventSink{plugin: h.plugin, fn: fn, ch: make(chan Event, 256)})
}

type eventSink struct {
	plugin string
	fn     func(Event)
	ch     chan Event
}

// Status is how a plugin's setup went.
type Status struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Running     bool   `json:"running"`
	Error       string `json:"error,omitempty"`
}

// Manager runs the plugins inside the server.
type Manager struct {
	mu       sync.Mutex
	sinks    []eventSink
	statuses []Status
	log      *slog.Logger
}

// Config is what the server gives the manager.
type Config struct {
	Router   Router
	Guard    func(http.Handler) http.Handler
	Version  string
	Log      *slog.Logger
	Snapshot func(ctx context.Context) (observe.Snapshot, error)
}

// Start sets up these plugins. One failing, or even panicking, disables only itself.
func Start(plugins []Plugin, c Config) *Manager {
	m := &Manager{log: c.Log}
	for _, p := range plugins {
		st := Status{Name: p.Name(), Description: p.Description(), Running: true}
		h := &Host{plugin: p.Name(), router: c.Router, guard: c.Guard, events: &m.sinks, mu: &m.mu,
			Version: c.Version, Log: c.Log.With("plugin", p.Name()), Snapshot: c.Snapshot}
		if err := setup(p, h); err != nil {
			st.Running, st.Error = false, err.Error()
			c.Log.Error("plugin disabled", "plugin", p.Name(), "err", err)
		}
		m.statuses = append(m.statuses, st)
	}
	for _, s := range m.sinks {
		go m.deliver(s)
	}
	return m
}

func setup(p Plugin, h *Host) (err error) {
	defer func() {
		if v := recover(); v != nil {
			err = fmt.Errorf("setup panicked: %v", v)
		}
	}()
	return p.Setup(h)
}

func (m *Manager) deliver(s eventSink) {
	for e := range s.ch {
		func() {
			defer func() {
				if v := recover(); v != nil {
					m.log.Error("plugin event handler panicked", "plugin", s.plugin, "panic", v)
				}
			}()
			s.fn(e)
		}()
	}
}

// Publish offers an event to every plugin, never waiting for one.
func (m *Manager) Publish(e Event) {
	for _, s := range m.sinks {
		select {
		case s.ch <- e:
		default:
		}
	}
}

// Stop ends event delivery. Publish must not be called afterwards.
func (m *Manager) Stop() {
	for _, s := range m.sinks {
		close(s.ch)
	}
}

// Statuses reports each plugin's condition.
func (m *Manager) Statuses() []Status { return slices.Clone(m.statuses) }
