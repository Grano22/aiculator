package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Grano22/aiculator/internal/swarm"
)

// fileChecks look at the data directory itself, so they work while the server is down,
// which is when they matter most.
func fileChecks(fix bool) []swarm.Finding {
	var out []swarm.Finding
	abs, err := filepath.Abs(dataDir)
	if err != nil {
		return out
	}
	if _, err := os.Stat(abs); err != nil {
		return append(out, swarm.Finding{Check: "data", OK: true, Detail: "no data directory here yet: aiculator join"})
	}
	_, running := serverURL()
	// The lock is the truth about whether a server runs here: the kernel drops it with the process.
	unlock, lockErr := lockDataDir(abs)
	held := lockErr != nil
	if unlock != nil {
		unlock()
	}
	out = append(out, swarm.Finding{Check: "server", OK: true, Detail: map[bool]string{true: "running", false: "not running"}[held]})
	if held && !running {
		out = append(out, swarm.Finding{Check: "server", OK: false, Detail: "a server holds the lock but does not answer: it may be starting, stuck, or another version"})
	}
	if pid, ok := readPid(abs); ok && !processAlive(pid) {
		f := swarm.Finding{Check: "pid file", OK: false, Fixable: true, Detail: fmt.Sprintf("names process %d, which is gone", pid)}
		if fix && !held {
			if os.Remove(filepath.Join(abs, "pid")) == nil {
				f.OK, f.Detail = true, f.Detail+"; removed"
			}
		}
		out = append(out, f)
	}
	if _, err := os.Stat(filepath.Join(abs, "addr")); err == nil && !running {
		f := swarm.Finding{Check: "address file", OK: false, Fixable: true, Detail: "points at a server that does not answer, or is another project's"}
		if fix && !held {
			if os.Remove(filepath.Join(abs, "addr")) == nil {
				f.OK, f.Detail = true, f.Detail+"; removed"
			}
		}
		out = append(out, f)
	}
	return out
}

func readPid(dir string) (int, bool) {
	b, err := os.ReadFile(filepath.Join(dir, "pid"))
	if err != nil {
		return 0, false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	return pid, err == nil && pid > 0
}

func doctorCmd() *cobra.Command {
	var fix, asJSON bool
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Check this project's swarm for damage and leftovers, and fix them with --fix",
		Long: `Checks the data directory, even with the server down: a stale pid or address file, or a
server holding the lock without answering. With the server up, it also reports records repaired
at startup, records kept in quarantine, worktrees of finished tasks, and leases long past their
deadline.

Damaged task records are repaired when the server starts, with the originals kept. --fix removes
stale files and leftover worktrees. It never deletes a task or anything kept in quarantine.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			findings := fileChecks(fix)
			var h swarm.Health
			if c, err := adminClient(); err == nil {
				if _, running := serverURL(); running {
					if err := c.Get(cmd.Context(), "/v1/doctor", &h); err != nil {
						return err
					}
					if fix && len(h.Orphans) > 0 {
						var res map[string]any
						if err := c.Post(cmd.Context(), "/v1/doctor/fix", nil, &res); err != nil {
							return err
						}
						for i := range h.Findings {
							if h.Findings[i].Check == "worktrees" {
								h.Findings[i].OK, h.Findings[i].Detail = true, h.Findings[i].Detail+"; removed"
							}
						}
					}
					findings = append(findings, h.Findings...)
				}
			}
			h.Findings = findings
			if asJSON {
				return printJSON(h)
			}
			problems := 0
			for _, f := range findings {
				mark := "ok  "
				if !f.OK {
					mark, problems = "FIX ", problems+1
					if !f.Fixable {
						mark = "LOOK"
					}
				}
				fmt.Printf("%s  %-13s %s\n", mark, f.Check, f.Detail)
			}
			for _, r := range h.Repairs {
				fmt.Printf("      repaired %s: %s\n", r.TaskID, strings.Join(r.Did, "; "))
			}
			if len(h.Quarantined) > 0 {
				fmt.Printf("      %d originals kept in quarantine: aiculator doctor --json shows them\n", len(h.Quarantined))
			}
			if problems > 0 && !fix {
				fmt.Println("Run aiculator doctor --fix to fix what can be fixed.")
				return errors.New("problems found")
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&fix, "fix", false, "fix what can be fixed safely")
	cmd.Flags().BoolVar(&asJSON, "json", false, "print as JSON")
	return cmd
}
