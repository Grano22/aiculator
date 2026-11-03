package main

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/Grano22/aiculator/internal/harness"
	"github.com/Grano22/aiculator/internal/skillpack"
	"github.com/Grano22/aiculator/internal/tmpl"
)

func templateCmd() *cobra.Command {
	var (
		install                         []string
		renderName, newName, harnessArg string
		forHarness                      string
		set, paths, sources             []string
		list, check, global, copyIt     bool
		perHarness, force               bool
	)
	cmd := &cobra.Command{
		Use:     "template [--install PATTERN...]",
		Aliases: []string{"templates"},
		Short:   "Render reusable instruction templates into skills",
		Long: `Templates live in .agents/templates; .agent/templates and .agent/template also work.
They use Jinja2 and MDX-style components, and render into skills that install exactly as
aiculator skill --install does.

Bundled components: Directive, Anchor, Subagents, Backoff, OutputFormat, Flow, Step, Note, Toc.
Add or replace one by creating components/NAME.md in a template directory.

These produce instructions for a model. A model can still ignore text, so anything that
must hold needs a real check, such as a task's --check command.`,
		Example: `  aiculator template --list
  aiculator template --new my-rules
  aiculator template --render agent-discipline --set retries=5
  aiculator template --install '*'
  aiculator template --install 'agent-*' --global --set timeout=300s
  aiculator template --install x --per-harness       a separate copy per harness
  aiculator template --install x --path ./vendor/skills --harness none
  aiculator template --check                         fail if a rendered skill is stale`,
		Args: cobra.ArbitraryArgs,
		RunE: func(c *cobra.Command, args []string) error {
			root := projectRoot()
			vars := map[string]string{}
			for _, kv := range set {
				k, v, ok := strings.Cut(kv, "=")
				if !ok || k == "" {
					return fmt.Errorf("--set %q: want KEY=VALUE", kv)
				}
				vars[k] = v
			}
			var hs []harness.Harness
			if harnessArg != "none" {
				var err error
				if hs, err = selectHarnesses(harnessArg); err != nil {
					return err
				}
			}
			o := skillpack.Options{Root: root, Global: global, Harnesses: hs, Paths: paths, Copy: copyIt, Force: force}
			s := tmpl.NewSet(o.Base(), sources)
			io := tmpl.InstallOptions{Skill: o, Vars: vars, Version: version, PerHarness: perHarness}
			wd, _ := os.Getwd()
			switch {
			case c.Flags().Changed("install"):
				io.Skill.Patterns = skillpack.NormalizeShellStar(append(install, args...), wd)
				acts, err := s.Install(io)
				printActions(acts)
				return err
			case renderName != "":
				t, err := s.Get(renderName)
				if err != nil {
					return err
				}
				ctx := tmpl.Context{Vars: vars, Version: version}
				if forHarness != "" {
					h, ok := harness.Get(forHarness)
					if !ok {
						return fmt.Errorf("unknown harness %q", forHarness)
					}
					ctx.Harness = &h
				}
				out, err := s.Render(t, ctx)
				if err != nil {
					return err
				}
				fmt.Print(out)
				return nil
			case newName != "":
				path, err := tmpl.New(o.Base(), newName)
				if err != nil {
					return err
				}
				fmt.Printf("Created %s. Edit it, then run: aiculator template --install %s\n", path, newName)
				return nil
			case check:
				io.Skill.Patterns = []string{"*"}
				stale, err := s.Check(io)
				if err != nil {
					return err
				}
				if len(stale) > 0 {
					return fmt.Errorf("out of date, run aiculator template --install: %s", strings.Join(stale, ", "))
				}
				fmt.Println("all rendered skills are up to date")
				return nil
			case list, len(args) == 0:
				all, err := s.List()
				if err != nil {
					return err
				}
				w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
				fmt.Fprintln(w, "TEMPLATE\tSOURCE")
				for _, t := range all {
					fmt.Fprintf(w, "%s\t%s\n", t.Name, t.Source)
				}
				return w.Flush()
			}
			return errors.New("nothing to do: see aiculator template --help")
		},
	}
	f := cmd.Flags()
	f.StringSliceVar(&install, "install", nil, "render and install templates matching these patterns")
	f.StringVar(&renderName, "render", "", "print one rendered template; writes nothing")
	f.StringVar(&forHarness, "for", "", "with --render, render as this harness")
	f.StringVar(&newName, "new", "", "create a starter template")
	f.BoolVar(&list, "list", false, "list templates")
	f.BoolVar(&check, "check", false, "fail if a rendered skill no longer matches its template")
	f.StringArrayVar(&set, "set", nil, "template variable, as KEY=VALUE")
	f.StringVar(&harnessArg, "harness", "installed", "installed, all, none, or comma-separated ids")
	f.StringSliceVar(&paths, "path", nil, "also install to this directory")
	f.StringSliceVar(&sources, "source", nil, "also read templates from this directory")
	f.BoolVar(&global, "global", false, "use ~/.agents and home directories")
	f.BoolVar(&copyIt, "copy", false, "copy instead of linking")
	f.BoolVar(&perHarness, "per-harness", false, "render a separate copy into each harness's own directory")
	f.BoolVar(&force, "force", false, "replace links; your own directories are never replaced")
	return cmd
}
