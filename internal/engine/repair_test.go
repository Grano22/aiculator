package engine

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Grano22/aiculator/internal/core"
)

// A record damaged on disk, by hand or by an older version, must not stop the swarm, and must
// not hand out work in an impossible state. It is repaired, and the original is kept.
func TestDamagedRecordsAreRepairedAtStartup(t *testing.T) {
	dir := t.TempDir()
	st := open(t, dir)
	e, err := New(bg, st, Config{RestartGrace: time.Nanosecond})
	if err != nil {
		t.Fatal(err)
	}
	add(t, e, TaskSpec{ID: "fine", Title: "x"})
	add(t, e, TaskSpec{ID: "half-claimed", Title: "x"})
	add(t, e, TaskSpec{ID: "fake-done", Title: "x"})
	e.Close()
	broken, _, _ := st.GetTask("half-claimed")
	broken.State, broken.Owner = core.Claimed, "ghost" // Claimed, with no lease.
	if err := st.RawPutForTest(broken); err != nil {
		t.Fatal(err)
	}
	done, _, _ := st.GetTask("fake-done")
	done.State = core.Done // Done, but nobody verified it.
	if err := st.RawPutForTest(done); err != nil {
		t.Fatal(err)
	}
	if err := st.RawBytesForTest("garbled", []byte(`{"id": "garbled", "state": `)); err != nil {
		t.Fatal(err)
	}
	st.Close()

	st2 := open(t, dir)
	e2, err := New(bg, st2, Config{RestartGrace: time.Nanosecond})
	if err != nil {
		t.Fatalf("one bad record stopped the swarm: %v", err)
	}
	defer func() { e2.Close(); st2.Close() }()
	got := map[string]core.Task{}
	ts, _ := e2.List(bg)
	for _, x := range ts {
		got[x.ID] = x
		if err := core.Validate(x); err != nil {
			t.Errorf("still invalid: %v", err)
		}
	}
	if got["half-claimed"].State != core.Ready || got["half-claimed"].Owner != "" {
		t.Errorf("half-claimed: %+v", got["half-claimed"])
	}
	if got["fake-done"].State != core.Parked {
		t.Errorf("an unverified done must go to a person: %+v", got["fake-done"])
	}
	if got["garbled"].State != core.Parked || got["garbled"].Reason != "unreadable_record" {
		t.Errorf("garbled: %+v", got["garbled"])
	}
	if got["fine"].State != core.Ready {
		t.Errorf("a good record changed: %+v", got["fine"])
	}
	repairs, _ := e2.Repairs(bg)
	if len(repairs) != 3 {
		t.Errorf("%d repairs: %+v", len(repairs), repairs)
	}
	kept, err := st2.Quarantined(bg)
	if err != nil || len(kept) != 3 {
		t.Fatalf("%d kept, %v", len(kept), err)
	}
	all := ""
	for _, k := range kept {
		var q map[string]any
		if json.Unmarshal(k, &q) != nil {
			t.Fatal("unreadable quarantine entry")
		}
		all += q["raw"].(string)
	}
	if !strings.Contains(all, `"state": `) || !strings.Contains(all, "ghost") {
		t.Errorf("the originals were not kept exactly: %s", all)
	}
	// The repaired task works as any other.
	if r, err := e2.Wait(bg, a1, 0); err != nil || r.Task == nil {
		t.Fatalf("%+v %v", r, err)
	}
	// A second start finds nothing more to do.
	e2.Close()
	st2.Close()
	st3 := open(t, dir)
	e3, err := New(bg, st3, Config{RestartGrace: time.Nanosecond})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { e3.Close(); st3.Close() }()
	if again, _ := e3.Repairs(bg); len(again) != 0 {
		t.Errorf("repaired again: %+v", again)
	}
}
