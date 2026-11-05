package core

import (
	"fmt"
	"os"
	"regexp"
	"slices"
	"testing"
	"time"
)

// TestSpecMatchesCode ties the TLA+ model to the implementation. It enumerates
// every reachable combination of state, command and actor through Apply, and
// compares the transitions that succeed with the table in spec/tla/Swarm.tla.
// Without this, the proofs would be about a model that could silently drift away
// from the code.
func TestSpecMatchesCode(t *testing.T) {
	spec := specTransitions(t)

	kinds := []CmdKind{CmdPublish, CmdGrant, CmdHeartbeat, CmdProgress, CmdSubmit, CmdChecks, CmdVerdict, CmdMerged, CmdFail, CmdRelease, CmdExpire, CmdRecover, CmdUnpark, CmdCancel, CmdAmend}
	actors := []string{"owner", "subm", "other", "system:x"}
	code := map[string]bool{}
	for _, st := range AllStates {
		for _, owner := range []string{"", "owner"} {
			for _, flags := range []int{0, 1, 2, 3, 4, 5, 6, 7} {
				for _, failures := range []int{0, 2} {
					for _, tier := range []int{1, 3} {
						x := Task{
							ID: "t", Tier: tier, State: st, Owner: owner, LeaseEpoch: 1, Failures: failures,
							Submitter: "subm", ReadyAt: t0, LeaseDeadline: t0, Verifier: "system:v",
							RequireReview: flags&1 != 0, ChecksRun: flags&2 != 0, ChecksPassed: flags&2 != 0,
						}
						if flags&4 != 0 {
							x.Checks = []string{"c"}
						}
						if Validate(x) != nil {
							continue // Not a state the system can be in.
						}
						for _, k := range kinds {
							for _, a := range actors {
								for _, pass := range []bool{true, false} {
									c := Cmd{Kind: k, Actor: a, Lease: 1, Pass: pass, AgentTier: 3, LeaseTTL: time.Minute, ProgressTTL: time.Minute, Amend: &Amendment{}}
									n, err := Apply(x, c, t0, DefaultPolicy())
									if err == nil {
										code[fmt.Sprintf("%s %s %s", st, k, n.State)] = true
									}
								}
							}
						}
					}
				}
			}
		}
	}

	var missing, extra []string
	for k := range code {
		if !spec[k] {
			missing = append(missing, k)
		}
	}
	for k := range spec {
		if !code[k] {
			extra = append(extra, k)
		}
	}
	slices.Sort(missing)
	slices.Sort(extra)
	if len(missing) > 0 {
		t.Errorf("the code allows transitions the spec does not model:\n%v", missing)
	}
	if len(extra) > 0 {
		t.Errorf("the spec allows transitions the code does not:\n%v", extra)
	}
	t.Logf("%d transitions agree", len(code))
}

// specTransitions reads the transition table from spec/tla/Swarm.tla.
func specTransitions(t *testing.T) map[string]bool {
	t.Helper()
	src, err := os.ReadFile("../../spec/tla/Swarm.tla")
	if err != nil {
		t.Fatal(err)
	}
	spec := map[string]bool{}
	for _, m := range regexp.MustCompile(`<<"(\w+)", "(\w+)", "(\w+)">>`).FindAllStringSubmatch(string(src), -1) {
		spec[m[1]+" "+m[2]+" "+m[3]] = true
	}
	if len(spec) < 30 {
		t.Fatalf("parsed only %d transitions from the spec", len(spec))
	}
	return spec
}
