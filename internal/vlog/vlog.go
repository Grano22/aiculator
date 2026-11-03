// Package vlog is leveled logging in the style of Kubernetes: -v=0 to -v=9, with
// --vmodule to raise one component alone. The levels mean what they mean in kubectl, so
// the habit carries over: 2 is the steady state for a server, 4 is debug, 5 is trace, and
// 6 to 9 show HTTP traffic in growing detail.
//
// A disabled call costs one atomic load and allocates nothing, so logging can stay in hot
// paths. Secrets are removed at the handler, so no call site can leak one by mistake.
package vlog

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Levels, with the meanings kubectl documents. Levels 1 to 5 are general and 6 to 9
// are HTTP traffic.
const (
	LevelAlways   = 0 // Always useful to an operator.
	LevelDefault  = 1 // A quiet default.
	LevelSteady   = 2 // Steady state: the recommended level for a server.
	LevelChanges  = 3 // Extended information about changes.
	LevelDebug    = 4 // Debug.
	LevelTrace    = 5 // Trace.
	LevelRequests = 6 // Requested resources.
	LevelHeaders  = 7 // Request and response headers.
	LevelBodies   = 8 // Bodies, truncated.
	LevelFull     = 9 // Bodies, not truncated.
	MaxLevel      = LevelFull
)

// Descriptions explains each level for help text and docs.
var Descriptions = []string{
	"Always visible: errors, and what an operator must see",
	"A quiet default: server start and stop, plugins, serious warnings",
	"Steady state, the server's default: task transitions, plans, tool calls, session starts",
	"Changes: every commit and grant, rejected commands, permission decisions, checks",
	"Debug: why an agent was told to wait, worktrees, check output, sandbox commands",
	"Trace: every git command, dispatch rejections, compare-and-swap revisions",
	"Requests: method, URL, status and time for each HTTP call",
	"Headers: also HTTP headers, with credentials removed",
	"Bodies: also HTTP bodies, cut to 1 KiB, with credentials removed",
	"Full: also HTTP bodies up to 1 MiB, with credentials removed",
}

type rule struct {
	pattern string
	level   int32
}

var (
	level   atomic.Int32
	rules   atomic.Pointer[[]rule]
	handler atomic.Pointer[slog.Logger]

	tempMu    sync.Mutex
	tempTimer *time.Timer
	tempUntil time.Time
	base      struct {
		level   int32
		vmodule string
	}
	vmoduleText atomic.Pointer[string]
)

func init() {
	handler.Store(slog.New(newHandler(os.Stderr, "text")))
}

func newHandler(w io.Writer, format string) slog.Handler {
	opts := &slog.HandlerOptions{ReplaceAttr: scrubAttr}
	if format == "json" {
		return slog.NewJSONHandler(w, opts)
	}
	return slog.NewTextHandler(w, opts)
}

// Options configures logging.
type Options struct {
	Level   int
	VModule string
	Format  string // "text" or "json".
	Output  io.Writer
}

// Setup applies options and makes this the default for slog as well, so that anything
// logging through slog gets the same format and redaction.
func Setup(o Options) error {
	if o.Format != "" && o.Format != "text" && o.Format != "json" {
		return fmt.Errorf("log format %q: use text or json", o.Format)
	}
	if err := SetLevel(o.Level); err != nil {
		return err
	}
	if err := SetVModule(o.VModule); err != nil {
		return err
	}
	w := o.Output
	if w == nil {
		w = os.Stderr
	}
	l := slog.New(newHandler(w, o.Format))
	handler.Store(l)
	slog.SetDefault(l)
	return nil
}

// SetOutput keeps the format simple for callers that only move the stream, such as the
// terminal dashboard, whose screen a log line would corrupt.
func SetOutput(w io.Writer, format string) {
	l := slog.New(newHandler(w, format))
	handler.Store(l)
	slog.SetDefault(l)
}

// SetLevel changes the global level at once. It is safe at any time.
func SetLevel(n int) error {
	if n < 0 || n > MaxLevel {
		return fmt.Errorf("verbosity %d: use 0 to %d", n, MaxLevel)
	}
	level.Store(int32(n))
	return nil
}

// Level returns the global level.
func Level() int { return int(level.Load()) }

// ParseVModule reads a list such as "engine=5,driver=3,http*=7".
func parseVModule(s string) ([]rule, error) {
	if strings.TrimSpace(s) == "" {
		return nil, nil
	}
	var out []rule
	for _, part := range strings.Split(s, ",") {
		name, val, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok || name == "" {
			return nil, fmt.Errorf("vmodule %q: want component=level", part)
		}
		n, err := strconv.Atoi(val)
		if err != nil || n < 0 || n > MaxLevel {
			return nil, fmt.Errorf("vmodule %q: level must be 0 to %d", part, MaxLevel)
		}
		if _, err := path.Match(name, ""); err != nil {
			return nil, fmt.Errorf("vmodule %q: %w", part, err)
		}
		out = append(out, rule{name, int32(n)})
	}
	return out, nil
}

// SetVModule sets per-component levels. An empty string clears them.
func SetVModule(s string) error {
	parsed, err := parseVModule(s)
	if err != nil {
		return err
	}
	if parsed == nil {
		rules.Store(nil)
	} else {
		rules.Store(&parsed)
	}
	vmoduleText.Store(&s)
	return nil
}

// VModule returns the current per-component levels.
func VModule() string {
	if p := vmoduleText.Load(); p != nil {
		return *p
	}
	return ""
}

// SetFor changes levels until d has passed, then restores the previous ones, so that a
// debugging session cannot leave a server at -v=9 for good. d of 0 makes it permanent.
func SetFor(n int, vmodule string, d time.Duration) error {
	if n < 0 || n > MaxLevel {
		return fmt.Errorf("verbosity %d: use 0 to %d", n, MaxLevel)
	}
	if _, err := parseVModule(vmodule); err != nil {
		return err
	}
	tempMu.Lock()
	defer tempMu.Unlock()
	if tempTimer != nil {
		tempTimer.Stop()
		tempTimer = nil
	} else {
		// A temporary change on top of another keeps the original to return to.
		base.level, base.vmodule = level.Load(), VModule()
	}
	tempUntil = time.Time{}
	_ = SetLevel(n)
	_ = SetVModule(vmodule)
	if d > 0 {
		tempUntil = time.Now().Add(d)
		tempTimer = time.AfterFunc(d, restore)
	} else {
		base.level, base.vmodule = int32(n), vmodule
	}
	return nil
}

func restore() {
	tempMu.Lock()
	defer tempMu.Unlock()
	tempTimer = nil
	tempUntil = time.Time{}
	_ = SetLevel(int(base.level))
	_ = SetVModule(base.vmodule)
}

// RevertsAt says when a temporary change ends; zero means it is permanent.
func RevertsAt() time.Time {
	tempMu.Lock()
	defer tempMu.Unlock()
	return tempUntil
}

// ComponentLevel is the level in force for one component.
func ComponentLevel(component string) int {
	if r := rules.Load(); r != nil {
		for _, x := range *r {
			if ok, _ := path.Match(x.pattern, component); ok {
				return int(x.level)
			}
		}
	}
	return int(level.Load())
}

// Logger logs for one component.
type Logger struct{ component string }

// For returns the logger for a component.
func For(component string) Logger { return Logger{component} }

// Verbose is the result of a level check.
type Verbose struct {
	component string
	level     int
	on        bool
}

// V checks a level. It allocates nothing, so a disabled call is nearly free. Build costly
// arguments only inside: if v := log.V(4); v.Enabled() { v.Info(...) }.
func (l Logger) V(n int) Verbose {
	if r := rules.Load(); r == nil {
		return Verbose{l.component, n, int32(n) <= level.Load()}
	}
	return Verbose{l.component, n, n <= ComponentLevel(l.component)}
}

// Enabled reports whether this level is on.
func (v Verbose) Enabled() bool { return v.on }

// Info writes one line if this level is on.
func (v Verbose) Info(msg string, kv ...any) {
	if !v.on {
		return
	}
	write(slog.LevelInfo, v.component, v.level, msg, kv)
}

// Warn writes one warning if this level is on.
func (v Verbose) Warn(msg string, kv ...any) {
	if !v.on {
		return
	}
	write(slog.LevelWarn, v.component, v.level, msg, kv)
}

// Error always writes, at any verbosity.
func (l Logger) Error(msg string, kv ...any) {
	write(slog.LevelError, l.component, 0, msg, kv)
}

func write(lv slog.Level, component string, v int, msg string, kv []any) {
	attrs := make([]any, 0, len(kv)+4)
	attrs = append(attrs, "v", v, "component", component)
	attrs = append(attrs, kv...)
	handler.Load().Log(context.Background(), lv, msg, attrs...)
}

// Slog returns a standard logger for a component, for code that takes one.
func Slog(component string) *slog.Logger {
	return handler.Load().With("component", component)
}

var (
	// Agent tokens are base64url JSON, then a dot, then a base64url signature.
	tokenShape = regexp.MustCompile(`eyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{16,}`)
	bearer     = regexp.MustCompile(`(?i)\b(bearer|basic)\s+[A-Za-z0-9._~+/=-]+`)
	secretName = regexp.MustCompile(`(?i)(token|authorization|password|passwd|secret|api[_-]?key|cookie|credential|private[_-]?key)`)
)

// Redacted replaces every secret value.
const Redacted = "[redacted]"

// IsSecretName reports whether a key or header name holds a credential.
func IsSecretName(name string) bool {
	// A count of tokens used is not a credential.
	switch strings.ToLower(name) {
	case "tokens", "input_tokens", "output_tokens", "total_tokens":
		return false
	}
	return secretName.MatchString(name)
}

// Scrub removes anything shaped like a credential from text.
func Scrub(s string) string {
	s = bearer.ReplaceAllString(s, "$1 "+Redacted)
	return tokenShape.ReplaceAllString(s, Redacted)
}

// scrubAttr runs on every attribute of every line, so a credential is removed whichever
// call site logged it, and under whichever name.
func scrubAttr(_ []string, a slog.Attr) slog.Attr {
	if a.Value.Kind() == slog.KindGroup {
		return a
	}
	if a.Key == slog.TimeKey || a.Key == slog.LevelKey {
		return a
	}
	if IsSecretName(a.Key) {
		a.Value = slog.StringValue(Redacted)
		return a
	}
	switch a.Value.Kind() {
	case slog.KindString:
		a.Value = slog.StringValue(Scrub(a.Value.String()))
	case slog.KindAny:
		// Errors and other values reach the log as text, so scrub that text too.
		a.Value = slog.StringValue(Scrub(fmt.Sprint(a.Value.Any())))
	}
	return a
}
