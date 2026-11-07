package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Grano22/aiculator/internal/harness"
	"github.com/Grano22/aiculator/internal/prompt"
)

// gitState says what a directory needs before the swarm can work in it.
func gitState(dir string) (needsInit, needsCommit bool) {
	run := func(args ...string) error {
		c := exec.Command("git", args...)
		c.Dir = dir
		return c.Run()
	}
	if run("rev-parse", "--git-dir") != nil {
		return true, true
	}
	return false, run("rev-parse", "--verify", "HEAD") != nil
}

// prepareGit makes the first commit the swarm's worktrees need. It adds no files.
func prepareGit(dir string, needsInit bool) error {
	run := func(args ...string) error {
		c := exec.Command("git", args...)
		c.Dir = dir
		out, err := c.CombinedOutput()
		if err != nil {
			return fmt.Errorf("git %s: %w: %s", args[0], err, strings.TrimSpace(string(out)))
		}
		return nil
	}
	if needsInit {
		if err := run("init", "-q"); err != nil {
			return err
		}
	}
	return run("commit", "-q", "--allow-empty", "-m", "Initial commit")
}

// routeDetail says what a harness can use, for the list of choices.
func routeDetail(h harness.Harness) string {
	if h.MCP == harness.MCPNone {
		return "skills and shell"
	}
	return "skills, shell and MCP"
}

func initCmd() *cobra.Command {
	var (
		list, sb                 string
		tier                     int
		yes, dry, planner        bool
		noSkills, noMCP, noStart bool
		allHarnesses             bool
		scoped, withMCP          bool
		noTimeouts               bool
	)
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Set up this machine, step by step: harnesses, skills and MCP config",
		Long: `Asks what you want, then does it. By default this sets up the whole machine: the swarm's
skills go where each harness reads them for every project, so that afterwards aiculator join
only has to start a server in a project. This needs no git repository and starts no server.

With --scoped it sets up this project alone, which also offers to start the server, and to
create the git repository the swarm needs.

Nothing is written until the last question is answered, so backing out with Ctrl-C changes
nothing. Running it again is safe, and keeps what is already there.

For a script, --yes takes every default, and flags answer single questions.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			err := runInit(cmd, initOptions{
				list: list, sb: sb, tier: tier, yes: yes, dry: dry, planner: planner,
				noSkills: noSkills, noMCP: noMCP, noStart: noStart, allHarnesses: allHarnesses,
				scoped: scoped, withMCP: withMCP, noTimeouts: noTimeouts,
			})
			switch {
			case errors.Is(err, prompt.ErrCanceled):
				fmt.Println("Canceled. Nothing was changed.")
				return nil
			case errors.Is(err, prompt.ErrNoInput):
				return errors.New("input ended before all the questions were answered, and nothing was changed: for a script, use --yes")
			}
			return err
		},
	}
	f := cmd.Flags()
	f.BoolVarP(&yes, "yes", "y", false, "take every default and ask nothing")
	f.StringVar(&list, "harness", "", "set these up without asking: installed, all, or comma-separated ids")
	f.BoolVar(&allHarnesses, "all-harnesses", false, "also offer harnesses that were not found on this machine")
	f.BoolVar(&scoped, "scoped", false, "set up this project only, rather than the whole machine")
	f.BoolVar(&noSkills, "no-skills", false, "do not install the skills")
	f.BoolVar(&noMCP, "no-mcp", false, "do not add MCP config")
	f.BoolVar(&withMCP, "mcp", false, "add MCP config without asking")
	f.BoolVar(&noStart, "no-start", false, "do not start the server, with --scoped")
	f.BoolVar(&noTimeouts, "no-timeouts", false, "leave Claude Code's command timeout as it is")
	f.IntVar(&tier, "tier", 1, "tier for MCP sessions: 1 standard, 2 advanced, 3 ultra")
	f.BoolVar(&planner, "planner", false, "also allow these sessions to plan")
	f.BoolVar(&dry, "dry-run", false, "say what would be done, and do nothing")
	f.StringVar(&sb, "sandbox", "auto", "sandbox for the server: auto, seatbelt, bwrap, docker or none")
	return cmd
}

type initOptions struct {
	list, sb                 string
	tier                     int
	yes, dry, planner        bool
	noSkills, noMCP, noStart bool
	allHarnesses             bool
	scoped, withMCP          bool
	noTimeouts               bool
}

// runInit collects every answer first, and acts only afterwards. That is the promise that
// backing out at any question changes nothing.
func runInit(cmd *cobra.Command, o initOptions) error {
	p := prompt.New(os.Stdin, os.Stdout)
	root := projectRoot()
	ask := func(question string, def bool) (bool, error) {
		if o.yes {
			return def, nil
		}
		return p.Confirm(question, def)
	}
	if !o.yes {
		if o.scoped {
			p.Say("This sets up %s for the swarm. Nothing is written until you have answered everything.\n", root)
		} else {
			p.Say("This sets up the swarm for every project on this machine. For this project alone, use --scoped.")
			p.Say("Nothing is written until you have answered everything.\n")
		}
	}

	// Only a project needs git, because its worktrees need a commit to start from.
	var needsInit, needsCommit, makeGit bool
	if o.scoped {
		needsInit, needsCommit = gitState(root)
	}
	if needsCommit {
		question := "This folder has no commits yet, and the swarm needs one. Make an empty first commit?"
		if needsInit {
			question = "This folder is not a git repository, and the swarm needs one. Create it, with an empty first commit?"
		}
		// --yes must not turn some folder into a repository without anyone having asked.
		if !o.yes {
			var err error
			if makeGit, err = p.Confirm(question, true); err != nil {
				return err
			}
		}
		if !makeGit {
			if !o.noStart {
				p.Say("Without a commit the server cannot start, so that step will be skipped.")
			}
			o.noStart = true
		}
	}

	hs, err := chooseHarnesses(p, o)
	if err != nil {
		return err
	}
	if len(hs) == 0 {
		if !o.dry {
			p.Say("No harness was chosen, so there is nothing to set up. To see what is supported: aiculator harness")
		}
		return nil
	}

	skills := !o.noSkills
	if skills {
		if skills, err = ask("Install the swarm's skills for these harnesses?", true); err != nil {
			return err
		}
	}
	anyMCP := false
	for _, h := range hs {
		anyMCP = anyMCP || h.MCP != harness.MCPNone
	}
	mcp := !o.noMCP && anyMCP
	if mcp && !o.withMCP {
		// The tools of every MCP server take up room in a session's context. In one project
		// that is the point. In every session on the machine it is a cost for little gain,
		// since aiculator join adds them where they are wanted, and so the default differs.
		question, def := "Also add MCP config, so new sessions get the swarm's tools directly?", true
		if !o.scoped {
			question, def = "Also add MCP config for every project? Its tools load in every session; aiculator join can add them per project instead.", false
		}
		if mcp, err = ask(question, def); err != nil {
			return err
		}
	}
	tier := o.tier
	if mcp && !o.yes && !cmd.Flags().Changed("tier") {
		i, err := p.Select("Which tier are these sessions?", []prompt.Option{
			{Label: "1 standard", Detail: "routine tasks; the usual choice"},
			{Label: "2 advanced", Detail: "also takes harder tasks"},
			{Label: "3 ultra", Detail: "takes the hardest tasks"},
		}, 0)
		if err != nil {
			return err
		}
		tier = i + 1
	}
	// Claude Code stops a command after two minutes unless told otherwise, which caps how
	// long aiculator work next can wait for a task on the model's behalf.
	timeouts := false
	if slices.ContainsFunc(hs, func(h harness.Harness) bool { return h.ID == "claude" }) && !o.noTimeouts {
		if timeouts, err = ask(fmt.Sprintf("Let Claude Code run a command for up to %v, so aiculator work next can wait for tasks without spending turns? Sets BASH_MAX_TIMEOUT_MS in %s.",
			commandTimeout, claudeSettingsFile(!o.scoped, root)), true); err != nil {
			return err
		}
	}
	start := o.scoped && !o.noStart
	if start {
		if start, err = ask("Start the server now?", true); err != nil {
			return err
		}
	}

	plan := setupPlan{harnesses: hs, skills: skills, mcp: mcp, start: start, tier: tier, planner: o.planner, sandbox: o.sb, global: !o.scoped, timeouts: timeouts}
	if o.dry {
		printPlan(plan, needsInit && makeGit, needsCommit && makeGit)
		return nil
	}
	if !skills && !mcp && !start && !makeGit && !timeouts {
		p.Say("Nothing to do.")
		return nil
	}
	if makeGit {
		if err := prepareGit(root, needsInit); err != nil {
			return fmt.Errorf("%w\nIf git does not know who you are, set user.name and user.email, and run this again", err)
		}
	}
	fmt.Println()
	if err := runSetup(plan); err != nil {
		return err
	}
	if needsCommit && !makeGit {
		fmt.Println("The swarm needs a git repository with at least one commit, so make one before aiculator up.")
	}
	return nil
}

func chooseHarnesses(p *prompt.Prompter, o initOptions) ([]harness.Harness, error) {
	if o.list != "" {
		return selectHarnesses(o.list)
	}
	found := harness.Detected()
	if o.yes {
		if len(found) == 0 {
			return nil, errors.New("no known harness found on PATH; name one with --harness, or see aiculator harness")
		}
		return found, nil
	}
	choices := found
	if o.allHarnesses || len(found) == 0 {
		if len(found) == 0 {
			p.Say("No known harness was found on PATH, so here are all of them.")
		}
		choices = harness.All()
	}
	options := make([]prompt.Option, len(choices))
	for i, h := range choices {
		detail := routeDetail(h)
		installed := h.Installed()
		if !installed {
			detail += ", not found here"
		}
		options[i] = prompt.Option{Label: h.ID, Detail: detail, Selected: installed}
	}
	picked, err := p.MultiSelect("Which harnesses should use the swarm?", options)
	if err != nil {
		return nil, err
	}
	out := make([]harness.Harness, 0, len(picked))
	for _, i := range picked {
		out = append(out, choices[i])
	}
	return out, nil
}

func printPlan(s setupPlan, gitInit, gitCommit bool) {
	fmt.Println("This would:")
	if gitInit {
		fmt.Println("- Create a git repository")
	}
	if gitCommit {
		fmt.Println("- Make an empty first commit")
	}
	ids := make([]string, 0, len(s.harnesses))
	for _, h := range s.harnesses {
		ids = append(ids, h.ID)
	}
	if s.skills {
		where := ".agents/skills"
		if s.global {
			where = "~/.agents/skills, for every project"
		}
		fmt.Printf("- Install the swarm's skills in %s, and link them for: %s\n", where, strings.Join(ids, ", "))
	}
	if s.mcp {
		for _, h := range s.harnesses {
			file := h.MCPFile
			if s.global {
				file = h.GlobalMCPFile
			}
			if h.MCP != harness.MCPNone && file != "" {
				fmt.Printf("- Add MCP config for %s to %s, at tier %d\n", h.ID, file, s.tier)
			}
		}
	}
	if s.timeouts {
		fmt.Printf("- Let Claude Code run a command for up to %v, in %s\n", commandTimeout, claudeSettingsFile(s.global, projectRoot()))
	}
	if s.start {
		fmt.Println("- Start the server")
	}
	fmt.Println("Nothing was changed.")
}
