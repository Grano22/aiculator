package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"go.yaml.in/yaml/v3"

	"github.com/Grano22/aiculator/internal/parallel"
)

func parallelCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "parallel",
		Short: "Work out what subagents can do at the same time in one working tree",
		Long: `Worktrees keep tasks apart. This is the level below: one task split among subagents in one
checkout. Give it the jobs and what each will change, and it says what can safely run at once.
Jobs share a wave only if their claims cannot overlap, and a job that claims nothing runs alone.

This needs no server, and it does not touch the swarm's state.`,
	}
	var (
		file, level, output string
		maxJobs             int
		asJSON              bool
	)
	plan := &cobra.Command{
		Use:   "plan",
		Short: "Plan jobs into waves that cannot conflict",
		Long: `Reads jobs as YAML or JSON. A claim is a file or a directory ending in /. With symbol
granularity it can also be file#symbol or file:10-40. Reads are what a job relies on staying the same while it works.

Granularity is how finely claims are compared. The default, auto, uses the coarser of dir and
file that loses no parallelism, and it treats file#symbol as the whole file. To let two jobs share
one file, ask for symbol. Their claims are then checked against the file, but nothing can stop a
subagent straying, and two in one file can overwrite each other.`,
		Example: `  aiculator parallel plan --file jobs.yaml -o plan.json
  cat jobs.yaml | aiculator parallel plan --file - --max 3

  # jobs.yaml
  jobs:
    - id: api
      touches: [internal/api/users.go]
    - id: docs
      touches: [docs/users.md]
      reads: [internal/api/users.go]
    - id: format
      exclusive: true
      deps: [api, docs]`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if file == "" {
				return errors.New("give the jobs with --file jobs.yaml, or --file - for standard input")
			}
			var data []byte
			var err error
			if file == "-" {
				data, err = io.ReadAll(os.Stdin)
			} else {
				data, err = os.ReadFile(file)
			}
			if err != nil {
				return err
			}
			var in parallel.Input
			dec := yaml.NewDecoder(bytes.NewReader(data))
			dec.KnownFields(true)
			if err := dec.Decode(&in); err != nil {
				return fmt.Errorf("reading the jobs: %w", err)
			}
			if cmd.Flags().Changed("granularity") || in.Granularity == "" {
				in.Granularity = parallel.Granularity(level)
			}
			if cmd.Flags().Changed("max") || in.Max == 0 {
				in.Max = maxJobs
			}
			if in.Granularity == parallel.Symbol {
				if err := parallel.CheckClaims(projectRoot(), in.Jobs); err != nil {
					return err
				}
			}
			p, err := parallel.Schedule(in)
			if err != nil {
				return err
			}
			b, err := json.MarshalIndent(p, "", "  ")
			if err != nil {
				return err
			}
			if output != "" {
				if err := writeFileAtomic(output, append(b, '\n')); err != nil {
					return err
				}
			}
			if asJSON {
				fmt.Println(string(b))
				return nil
			}
			printPlanWaves(p, output)
			return nil
		},
	}
	pf := plan.Flags()
	pf.StringVar(&file, "file", "", "the jobs, as YAML or JSON; - for standard input")
	pf.StringVar(&level, "granularity", "auto", "dir, file, symbol, repo or auto")
	pf.IntVar(&maxJobs, "max", 4, "most subagents at once")
	pf.StringVarP(&output, "output", "o", "", "save the plan, for verify")
	pf.BoolVar(&asJSON, "json", false, "print the plan as JSON")

	snapshot := &cobra.Command{
		Use:   "snapshot",
		Short: "Record the working tree before a wave, without staging or committing anything",
		RunE: func(*cobra.Command, []string) error {
			tree, err := parallel.Snapshot(projectRoot())
			if err != nil {
				return err
			}
			fmt.Println(tree)
			return nil
		},
	}
	var (
		planFile, since string
		wave            int
	)
	verify := &cobra.Command{
		Use:   "verify",
		Short: "Check that a wave changed only what its jobs claimed; exits 1 if not",
		RunE: func(cmd *cobra.Command, _ []string) error {
			b, err := os.ReadFile(planFile)
			if err != nil {
				return err
			}
			var p parallel.Plan
			if err := json.Unmarshal(b, &p); err != nil {
				return fmt.Errorf("reading the plan: %w", err)
			}
			r, err := parallel.Verify(projectRoot(), &p, wave, since)
			if err != nil {
				return err
			}
			if asJSON {
				if err := printJSON(r); err != nil {
					return err
				}
			} else {
				fmt.Printf("%d files changed in wave %d.\n", len(r.Changed), wave)
				for _, f := range r.Unclaimed {
					fmt.Printf("  NOT CLAIMED: %s\n", f)
				}
				if len(r.Idle) > 0 {
					fmt.Printf("  Changed nothing, so check that these did their work: %s\n", strings.Join(r.Idle, ", "))
				}
				fmt.Println(r.Note)
			}
			if !r.OK {
				return fmt.Errorf("wave %d changed %d files that no job claimed", wave, len(r.Unclaimed))
			}
			return nil
		},
	}
	vf := verify.Flags()
	vf.StringVar(&planFile, "plan", "", "the plan saved by parallel plan -o")
	vf.IntVar(&wave, "wave", 1, "which wave just finished")
	vf.StringVar(&since, "since", "", "what parallel snapshot printed before that wave")
	vf.BoolVar(&asJSON, "json", false, "print as JSON")
	_ = verify.MarkFlagRequired("plan")
	_ = verify.MarkFlagRequired("since")
	cmd.AddCommand(plan, snapshot, verify)
	return cmd
}

func printPlanWaves(p *parallel.Plan, saved string) {
	jobs := 0
	for _, w := range p.Waves {
		jobs += len(w.Jobs)
	}
	if p.Verdict == "parallel" {
		fmt.Printf("Parallel: %d jobs in %d waves, compared at %s level.\n", jobs, len(p.Waves), p.Granularity)
	} else {
		fmt.Printf("Sequential: these %d jobs cannot safely run together, even at %s level.\n", jobs, p.Granularity)
	}
	for _, w := range p.Waves {
		fmt.Printf("  Wave %d: %s\n", w.N, strings.Join(w.Jobs, ", "))
	}
	if !p.Optimal {
		fmt.Println("This schedule is good, but with this many jobs it is not proven to be the shortest.")
	}
	if len(p.Alone) > 0 {
		fmt.Printf("Running alone, because they claim nothing or asked to: %s\n", strings.Join(p.Alone, ", "))
	}
	for _, c := range p.Conflicts {
		if strings.HasSuffix(c.Why, "must run alone") {
			continue // Already said above, and it would be a line for every other job.
		}
		fmt.Printf("  %s and %s: %s\n", c.A, c.B, c.Why)
	}
	if saved != "" {
		fmt.Printf("Saved to %s.\n", saved)
	}
}
