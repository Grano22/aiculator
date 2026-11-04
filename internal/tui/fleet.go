package tui

import (
	"context"
	"fmt"
	"strings"

	"github.com/Grano22/aiculator/pkg/observe"
)

// FleetRow is one realm in the view across projects.
type FleetRow struct {
	Name, Path string
	Live       bool
	Missing    bool
	Snapshot   observe.Snapshot
}

// FleetFetch reads every realm.
type FleetFetch func(ctx context.Context) ([]FleetRow, error)

var fleetViews = []string{"1 realms", "2 tasks", "3 agents"}

// RenderFleet draws one frame across all realms. It only reads, and every value shown
// comes from that realm's own server.
func RenderFleet(rows []FleetRow, o Options) string {
	if o.Width <= 0 {
		o.Width = 100
	}
	if o.Height <= 0 {
		o.Height = 40
	}
	f := &frame{o: o}
	live := 0
	for _, r := range rows {
		if r.Live {
			live++
		}
	}
	parts := []string{f.paint(bold, "aiculator"), fmt.Sprintf("%d realms, %d running", len(rows), live)}
	if o.Paused {
		parts = append(parts, f.paint(yellow, "PAUSED"))
	}
	if o.Stale != "" {
		parts = append(parts, f.paint(red, "STALE: "+cut(Clean(o.Stale), 40)))
	}
	f.add(strings.Join(parts, "  "))
	view := int(o.View) % len(fleetViews)
	var tabs []string
	for i, n := range fleetViews {
		if i == view {
			tabs = append(tabs, f.paint(bold, "["+n+"]"))
		} else {
			tabs = append(tabs, f.paint(dim, " "+n+" "))
		}
	}
	f.add(strings.Join(tabs, " "))
	f.add("")
	switch view {
	case 1:
		f.fleetTasks(rows, o.Height-len(f.lines)-1, o.Scroll)
	case 2:
		f.fleetAgents(rows, o.Height-len(f.lines)-1, o.Scroll)
	default:
		f.fleetRealms(rows)
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
	return strings.Join(append(body, f.paint(dim, cut("q quit  tab next  p pause  ↑↓ scroll", o.Width))), "\n")
}

func (f *frame) fleetRealms(rows []FleetRow) {
	if len(rows) == 0 {
		f.add(f.paint(dim, "No realms yet. A project becomes one when its server starts: aiculator join"))
		return
	}
	f.add(f.paint(dim, col("REALM", 22)+col("STATE", 10)+right("READY", 6)+right("WORK", 6)+right("REVIEW", 7)+right("DONE", 6)+right("PARKED", 7)+right("AGENTS", 7)+right("WAIT", 7)+"  PATH"))
	for _, r := range rows {
		state := f.paint(dim, col("stopped", 10))
		if r.Missing {
			state = f.paint(yellow, col("missing", 10))
		}
		if !r.Live {
			f.add(col(r.Name, 22) + state + f.paint(dim, right("-", 6)+right("-", 6)+right("-", 7)+right("-", 6)+right("-", 7)+right("-", 7)+right("-", 7)+"  "+Clean(r.Path)))
			continue
		}
		s := r.Snapshot
		state = f.paint(green, col("running", 10))
		if !s.Ready {
			state = f.paint(red, col("unwell", 10))
		}
		working := 0
		for _, a := range s.Agents {
			if a.State == "working" {
				working++
			}
		}
		active := s.States["claimed"] + s.States["running"]
		parked := fmt.Sprint(s.States["parked"])
		if s.States["parked"] > 0 {
			parked = f.paint(yellow, right(parked, 7))
		} else {
			parked = right(parked, 7)
		}
		f.add(col(r.Name, 22) + state + right(fmt.Sprint(s.States["ready"]), 6) + right(fmt.Sprint(active), 6) +
			right(fmt.Sprint(s.States["review"]+s.States["merging"]), 7) + right(fmt.Sprint(s.States["done"]), 6) + parked +
			right(fmt.Sprintf("%d/%d", working, len(s.Agents)), 7) + right(Millis(s.OldestMs), 7) + "  " + f.paint(dim, Clean(r.Path)))
	}
}

func (f *frame) fleetTasks(rows []FleetRow, room, scroll int) {
	type line struct {
		realm string
		t     observe.TaskLive
	}
	var all []line
	hidden := 0
	for _, r := range rows {
		hidden += r.Snapshot.Truncated
		for _, t := range r.Snapshot.Tasks {
			all = append(all, line{r.Name, t})
		}
	}
	// Each server lists what is in progress or needs attention. The first view counts the rest.
	title := fmt.Sprintf("Tasks in progress or needing attention: %d", len(all))
	if hidden > 0 {
		title += fmt.Sprintf(" (%d more not listed)", hidden)
	}
	f.section(title)
	f.add(f.paint(dim, col("REALM", 18)+col("TASK", 22)+col("STATE", 9)+col("T", 2)+col("OWNER", 18)+"TITLE"))
	if len(all) == 0 {
		f.add(f.paint(dim, "none"))
		return
	}
	shown := all[min(max(scroll, 0), len(all)-1):]
	room -= 2
	for i, l := range shown {
		if i >= room-1 && len(shown) > room {
			f.add(f.paint(dim, fmt.Sprintf("… %d more", len(shown)-i)))
			return
		}
		f.add(f.paint(cyan, col(l.realm, 18)) + col(l.t.ID, 22) + col(l.t.State, 9) + col(fmt.Sprint(l.t.Tier), 2) + col(l.t.Owner, 18) + Clean(l.t.Title))
	}
}

func (f *frame) fleetAgents(rows []FleetRow, room, scroll int) {
	type line struct {
		realm string
		a     observe.AgentLive
	}
	var all []line
	for _, r := range rows {
		for _, a := range r.Snapshot.Agents {
			all = append(all, line{r.Name, a})
		}
	}
	f.section(fmt.Sprintf("Agents: %d", len(all)))
	f.add(f.paint(dim, col("REALM", 18)+col("AGENT", 22)+col("T", 2)+col("STATE", 9)+col("TASK", 22)+right("SEEN", 8)))
	if len(all) == 0 {
		f.add(f.paint(dim, "none yet"))
		return
	}
	shown := all[min(max(scroll, 0), len(all)-1):]
	room -= 2
	for i, l := range shown {
		if i >= room-1 && len(shown) > room {
			f.add(f.paint(dim, fmt.Sprintf("… %d more", len(shown)-i)))
			return
		}
		f.add(f.paint(cyan, col(l.realm, 18)) + col(l.a.ID, 22) + col(fmt.Sprint(l.a.Tier), 2) + col(l.a.State, 9) + col(l.a.Task, 22) + right(Millis(l.a.SeenAgoMs), 8))
	}
}
