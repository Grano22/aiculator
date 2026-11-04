// Package tui draws the live terminal dashboard. Render is a pure function from a
// snapshot to text, so what it draws can be tested exactly, with no terminal involved.
package tui

import (
	"fmt"
	"slices"
	"strings"
	"unicode"

	"github.com/Grano22/aiculator/pkg/observe"
)

// View selects a screen.
type View int

// The screens.
const (
	Overview View = iota
	Tasks
	Agents
	Flow
	Usage
)

var viewNames = []string{"1 overview", "2 tasks", "3 agents", "4 flow", "5 usage"}

// Options controls one frame.
type Options struct {
	View          View
	Width, Height int
	Scroll        int
	Color         bool
	Paused        bool
	Stale         string // Set when the server stopped answering.
	Plain         bool   // A one-off print: no padding to the screen, no key help.
}

const (
	red    = "31"
	green  = "32"
	yellow = "33"
	cyan   = "36"
	dim    = "2"
	bold   = "1"
)

type frame struct {
	o     Options
	lines []string
}

func (f *frame) paint(code, s string) string {
	if !f.o.Color || s == "" {
		return s
	}
	return "\x1b[" + code + "m" + s + "\x1b[0m"
}

func (f *frame) add(s string) { f.lines = append(f.lines, s) }

// Clean removes everything that could control the terminal. Task titles and agent names
// come from agents, so an escape sequence in one must not reach the screen as anything
// but harmless text.
func Clean(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return ' '
		}
		return r
	}, s)
}

// cut fits text to a width in runes, marking anything it removes.
func cut(s string, w int) string {
	r := []rune(s)
	if w <= 0 {
		return ""
	}
	if len(r) <= w {
		return s
	}
	if w == 1 {
		return "…"
	}
	return string(r[:w-1]) + "…"
}

// col pads or cuts to an exact width.
func col(s string, w int) string {
	s = cut(Clean(s), w)
	if n := w - len([]rune(s)); n > 0 {
		s += strings.Repeat(" ", n)
	}
	return s
}

func right(s string, w int) string {
	s = cut(s, w)
	if n := w - len([]rune(s)); n > 0 {
		s = strings.Repeat(" ", n) + s
	}
	return s
}

// Micros formats a duration in microseconds.
func Micros(us int64) string {
	switch {
	case us < 1000:
		return fmt.Sprintf("%dµs", us)
	case us < 1_000_000:
		return fmt.Sprintf("%.1fms", float64(us)/1000)
	case us < 60_000_000:
		return fmt.Sprintf("%.1fs", float64(us)/1e6)
	}
	return Millis(us / 1000)
}

// Millis formats a longer time, down to whole seconds.
func Millis(ms int64) string {
	neg := ""
	if ms < 0 {
		neg, ms = "-", -ms
	}
	sec := ms / 1000
	if neg != "" {
		sec = max(sec, 1) // Never show a past deadline as "-0s".
	}
	switch {
	case sec < 60:
		return fmt.Sprintf("%s%ds", neg, sec)
	case sec < 3600:
		return fmt.Sprintf("%s%dm%02ds", neg, sec/60, sec%60)
	case sec < 86400:
		return fmt.Sprintf("%s%dh%02dm", neg, sec/3600, sec/60%60)
	}
	return fmt.Sprintf("%s%dd%02dh", neg, sec/86400, sec/3600%24)
}

// Count shortens a large number.
func Count(n int64) string {
	switch {
	case n < 10_000:
		return fmt.Sprint(n)
	case n < 1_000_000:
		return fmt.Sprintf("%.1fk", float64(n)/1e3)
	case n < 1_000_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1e6)
	}
	return fmt.Sprintf("%.1fG", float64(n)/1e9)
}

// Money formats micro-dollars.
func Money(micro int64) string { return fmt.Sprintf("$%.2f", float64(micro)/1e6) }

const sparks = "▁▂▃▄▅▆▇█"

// Spark draws a history as one line, scaled to its own peak.
func Spark(values []int64, width int) string {
	if width <= 0 {
		return ""
	}
	if len(values) > width {
		values = values[len(values)-width:]
	}
	var peak int64
	for _, v := range values {
		peak = max(peak, v)
	}
	blocks := []rune(sparks)
	var b strings.Builder
	for _, v := range values {
		if v <= 0 || peak == 0 {
			b.WriteRune(' ')
			continue
		}
		b.WriteRune(blocks[min(int(v*int64(len(blocks))/(peak+1)), len(blocks)-1)])
	}
	return b.String()
}

// Render draws one frame, at most Height lines of at most Width columns.
func Render(s observe.Snapshot, o Options) string {
	if o.Width <= 0 {
		o.Width = 100
	}
	if o.Height <= 0 {
		o.Height = 40
	}
	f := &frame{o: o}
	f.header(s)
	switch o.View {
	case Tasks:
		f.tasks(s, o.Height-len(f.lines)-1, o.Scroll)
	case Agents:
		f.agents(s, o.Height-len(f.lines)-1, o.Scroll)
	case Flow:
		f.flow(s)
	case Usage:
		f.usage(s)
	default:
		f.overview(s)
	}
	if o.Plain {
		return strings.TrimRight(strings.Join(f.lines, "\n"), "\n")
	}
	body := f.lines
	if len(body) > o.Height-1 {
		body = body[:o.Height-1]
	}
	for len(body) < o.Height-1 {
		body = append(body, "")
	}
	help := "q quit  tab next  p pause  ↑↓ scroll"
	return strings.Join(append(body, f.paint(dim, cut(help, o.Width))), "\n")
}

func (f *frame) header(s observe.Snapshot) {
	state := f.paint(green, "READY")
	if !s.Ready {
		state = f.paint(red, "NOT READY")
	}
	parts := []string{f.paint(bold, "aiculator"), Clean(s.Version), "up " + Millis(s.UptimeMs), state}
	if s.Realm != "" {
		parts = append(parts, f.paint(cyan, "realm: "+Clean(s.Realm)))
	}
	switch {
	case s.Planning.Active:
		parts = append(parts, f.paint(cyan, "planning: "+Clean(s.Planning.Phase)))
	case s.Planning.Lost:
		parts = append(parts, f.paint(red, "planner lost"))
	}
	if f.o.Paused {
		parts = append(parts, f.paint(yellow, "PAUSED"))
	}
	if f.o.Stale != "" {
		parts = append(parts, f.paint(red, "STALE: "+cut(Clean(f.o.Stale), 40)))
	}
	f.add(strings.Join(parts, "  "))
	var tabs []string
	for i, n := range viewNames {
		if View(i) == f.o.View {
			tabs = append(tabs, f.paint(bold, "["+n+"]"))
		} else {
			tabs = append(tabs, f.paint(dim, " "+n+" "))
		}
	}
	f.add(strings.Join(tabs, " "))
	f.add("")
}

func (f *frame) section(title string) { f.add(f.paint(bold, title)) }

var pipeline = []string{"draft", "ready", "claimed", "running", "review", "merging", "done"}

func (f *frame) stateLine(s observe.Snapshot) {
	parts := make([]string, 0, len(pipeline))
	for _, st := range pipeline {
		parts = append(parts, fmt.Sprintf("%s %d", st, s.States[st]))
	}
	f.add(strings.Join(parts, " → "))
	line := fmt.Sprintf("failed %d  parked %d  canceled %d  agents waiting %d", s.States["failed"], s.States["parked"], s.States["canceled"], s.Waiting)
	if s.OldestMs > 0 {
		line += "  oldest ready " + Millis(s.OldestMs)
	}
	if s.States["parked"] > 0 || s.States["failed"] > 0 {
		f.add(f.paint(yellow, line))
	} else {
		f.add(line)
	}
}

func (f *frame) overview(s observe.Snapshot) {
	f.section("Flow")
	f.stateLine(s)
	f.add("")
	f.throughput(s)
	f.add("")
	f.latency(s)
	f.add("")
	f.probes(s)
	f.add("")
	f.usageLine(s)
	f.add("")
	left := f.o.Height - len(f.lines) - 2
	if left > 2 {
		f.tasks(s, left, 0)
	}
}

func (f *frame) throughput(s observe.Snapshot) {
	t := s.Throughput
	f.section("Throughput")
	f.add(fmt.Sprintf("done %.1f/min   grants %.1f/min   failed %.1f/min   commits %.1f/s   api %.1f/s", t.DonePerMin, t.GrantsPerMin, t.FailedPerMin, t.CommitsPerSec, t.APIPerSec))
	f.add(fmt.Sprintf("since start: %s done, %s grants, %s failures, %s zombies caught", Count(t.TotalDone), Count(t.TotalGrants), Count(t.TotalFailed), Count(t.TotalZombies)))
	w := max(f.o.Width-10, 10)
	f.add("commits " + f.paint(cyan, Spark(t.Commits, w)))
	f.add("done    " + f.paint(green, Spark(t.Done, w)))
}

var latencyRows = []struct{ key, label string }{
	{observe.LatencyQueue, "queue wait"},
	{observe.LatencyCommit, "commit"},
	{observe.LatencyAPI, "api call"},
	{observe.LatencyCycle, "task cycle"},
}

func (f *frame) latency(s observe.Snapshot) {
	f.section("Latency")
	f.add(f.paint(dim, col("", 12)+right("count", 8)+right("p50", 10)+right("p90", 10)+right("p99", 10)+right("max", 10)+right("jitter", 10)))
	for _, r := range latencyRows {
		st, ok := s.Latency[r.key]
		if !ok {
			f.add(col(r.label, 12) + right("-", 8))
			continue
		}
		f.add(col(r.label, 12) + right(Count(st.Count), 8) + right(Micros(st.P50), 10) + right(Micros(st.P90), 10) + right(Micros(st.P99), 10) + right(Micros(st.Max), 10) + right(Micros(st.Jitter), 10))
	}
}

func (f *frame) probes(s observe.Snapshot) {
	f.section("Probes")
	var parts []string
	for _, p := range s.Probes {
		mark := f.paint(green, "ok")
		if !p.OK {
			mark = f.paint(red, "FAIL")
		}
		parts = append(parts, fmt.Sprintf("%s %s %s", Clean(p.Name), mark, Micros(p.LatencyUs)))
	}
	f.add(strings.Join(parts, "   "))
	for _, p := range s.Probes {
		if !p.OK {
			f.add(f.paint(red, "  "+Clean(p.Name)+": "+cut(Clean(p.Detail), max(f.o.Width-len(p.Name)-4, 10))))
		}
	}
}

func (f *frame) usageLine(s observe.Snapshot) {
	u := s.Usage
	f.section("Usage")
	line := fmt.Sprintf("tokens %s   spent %s", Count(u.Tokens), Money(u.Spent))
	if u.Budgeted > 0 {
		line += "   budgeted " + Money(u.Budgeted)
	}
	if u.OverBudget > 0 {
		line += "   " + f.paint(red, fmt.Sprintf("%d over budget", u.OverBudget))
	}
	f.add(line)
}

func opt(ms *int64) string {
	if ms == nil {
		return "-"
	}
	return Millis(*ms)
}

func (f *frame) healthWord(h string) string {
	switch h {
	case observe.HealthOK:
		return f.paint(green, col("ok", 7))
	case observe.HealthAtRisk:
		return f.paint(yellow, col("at risk", 7))
	case observe.HealthExpired:
		return f.paint(red, col("expired", 7))
	}
	return f.paint(dim, col("-", 7))
}

func (f *frame) tasks(s observe.Snapshot, room, scroll int) {
	f.section(fmt.Sprintf("Tasks with work in progress or problems: %d", len(s.Tasks)+s.Truncated))
	title := max(f.o.Width-68, 8)
	f.add(f.paint(dim, col("HEALTH", 8)+col("STATE", 9)+col("T", 2)+col("ID", 16)+col("OWNER", 14)+right("LEASE", 7)+right("PROG", 7)+"  "+col("TITLE", title)))
	if len(s.Tasks) == 0 {
		f.add(f.paint(dim, "none"))
		return
	}
	rows := s.Tasks[min(max(scroll, 0), len(s.Tasks)-1):]
	room -= 2
	for i, t := range rows {
		if i >= room-1 && len(rows) > room {
			f.add(f.paint(dim, fmt.Sprintf("… %d more", len(rows)-i+s.Truncated)))
			return
		}
		f.add(f.healthWord(t.Health) + " " + col(t.State, 9) + col(fmt.Sprint(t.Tier), 2) + col(t.ID, 16) + col(t.Owner, 14) + right(opt(t.LeaseRemainingMs), 7) + right(opt(t.ProgressRemaining), 7) + "  " + col(rowTitle(t), title))
	}
	if s.Truncated > 0 {
		f.add(f.paint(dim, fmt.Sprintf("… %d more not sent by the server", s.Truncated)))
	}
}

func rowTitle(t observe.TaskLive) string {
	if t.State == "parked" || t.State == "failed" {
		return t.Title + " [" + t.Reason + "]"
	}
	return t.Title
}

func (f *frame) agents(s observe.Snapshot, room, scroll int) {
	f.section(fmt.Sprintf("Agents: %d", len(s.Agents)))
	f.add(f.paint(dim, col("AGENT", 20)+col("T", 2)+col("STATE", 9)+col("TASK", 16)+right("SEEN", 8)+right("TOKENS", 8)+right("COST", 9)+right("DONE", 6)+right("FAIL", 6)))
	if len(s.Agents) == 0 {
		f.add(f.paint(dim, "none yet. Join with: aiculator work join, or see aiculator join"))
		return
	}
	rows := s.Agents[min(max(scroll, 0), len(s.Agents)-1):]
	room -= 2
	for i, a := range rows {
		if i >= room-1 && len(rows) > room {
			f.add(f.paint(dim, fmt.Sprintf("… %d more", len(rows)-i)))
			return
		}
		state := col(a.State, 9)
		switch a.State {
		case "working":
			state = f.paint(green, state)
		case "gone":
			state = f.paint(dim, state)
		}
		f.add(col(a.ID, 20) + col(fmt.Sprint(a.Tier), 2) + state + col(a.Task, 16) + right(Millis(a.SeenAgoMs), 8) + right(Count(a.Tokens), 8) + right(Money(a.Cost), 9) + right(Count(a.Done), 6) + right(Count(a.Failed), 6))
	}
}

func (f *frame) flow(s observe.Snapshot) {
	f.section("Tasks by state")
	f.stateLine(s)
	f.add("")
	f.section("Moves between states, since the server started")
	if len(s.Flow) == 0 {
		f.add(f.paint(dim, "none yet"))
		return
	}
	var peak int64
	for _, e := range s.Flow {
		peak = max(peak, e.Count)
	}
	barMax := max(f.o.Width-34, 5)
	for _, e := range slices.Clone(s.Flow) {
		bar := strings.Repeat("█", max(int(e.Count*int64(barMax)/peak), 1))
		f.add(col(e.From, 9) + "→ " + col(e.To, 9) + right(Count(e.Count), 8) + "  " + f.paint(cyan, bar))
	}
}

func (f *frame) usage(s observe.Snapshot) {
	f.usageLine(s)
	f.add("")
	f.section("By tier")
	f.add(f.paint(dim, col("TIER", 6)+right("TASKS", 8)+right("TOKENS", 10)+right("SPENT", 10)+right("BUDGET", 10)))
	for _, t := range s.Usage.ByTier {
		f.add(col(fmt.Sprint(t.Tier), 6) + right(Count(int64(t.Tasks)), 8) + right(Count(t.Tokens), 10) + right(Money(t.Spent), 10) + right(Money(t.Budget), 10))
	}
	f.add("")
	f.section("Tightest budgets")
	if len(s.Usage.Tightest) == 0 {
		f.add(f.paint(dim, "no budgeted task has spent anything yet"))
	} else {
		barW := 20
		for _, b := range s.Usage.Tightest {
			fill := min(int(b.Spent*int64(barW)/max(b.Budget, 1)), barW)
			bar := strings.Repeat("█", fill) + strings.Repeat("░", barW-fill)
			code := green
			if b.Spent*4 >= b.Budget*3 {
				code = yellow
			}
			if b.Spent >= b.Budget {
				code = red
			}
			f.add(col(b.ID, 16) + f.paint(code, bar) + " " + right(Money(b.Spent), 9) + " of " + Money(b.Budget) + "  " + Clean(b.State))
		}
	}
	f.add("")
	f.section("By agent, since the server started")
	f.add(f.paint(dim, col("AGENT", 24)+right("TOKENS", 10)+right("COST", 10)+right("DONE", 7)+right("FAIL", 7)))
	for _, a := range s.Agents {
		f.add(col(a.ID, 24) + right(Count(a.Tokens), 10) + right(Money(a.Cost), 10) + right(Count(a.Done), 7) + right(Count(a.Failed), 7))
	}
}
