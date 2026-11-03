package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Grano22/aiculator/internal/harness"
)

// target is one config file to change, with a label for messages.
type target struct {
	label string
	h     harness.Harness
	cli   []string
}

// resolveTargets accepts auto, all, harness ids, a path to a harness executable, or
// a path to a config file. An existing executable or a known basename means a
// harness; any other path means a config file.
func resolveTargets(arg, format string, global bool) ([]target, error) {
	var out []target
	addHarness := func(h harness.Harness) {
		t := target{label: h.ID, h: h, cli: h.CLIAdd}
		if global {
			t.h.MCPFile = harness.ExpandHome(h.GlobalMCPFile)
			t.cli = nil // The harness's own command would write to its project scope.
			if h.GlobalMCPFile == "" {
				t.h.MCP = harness.MCPNone
				t.h.MCPFile = ""
			}
		}
		out = append(out, t)
	}
	switch arg {
	case "", "auto", "installed", "detected":
		for _, h := range harness.Detected() {
			addHarness(h)
		}
		if len(out) == 0 {
			return nil, errors.New("no known harness found on PATH; name one with --harness")
		}
		return out, nil
	case "all":
		for _, h := range harness.All() {
			addHarness(h)
		}
		return out, nil
	}
	for _, item := range strings.Split(arg, ",") {
		item = strings.TrimSpace(item)
		if h, ok := harness.Get(item); ok {
			addHarness(h)
			continue
		}
		if !strings.ContainsAny(item, `/\`) && !strings.Contains(item, ".") {
			return nil, fmt.Errorf("unknown harness %q: see aiculator harness, or give a path", item)
		}
		if h, ok := harnessForBinary(item); ok {
			addHarness(h)
			continue
		}
		abs, err := filepath.Abs(harness.ExpandHome(item))
		if err != nil {
			return nil, err
		}
		f := harness.MCPFormat(format)
		if f == "" {
			f = harness.FormatForPath(abs)
		}
		out = append(out, target{label: abs, h: harness.At(f, abs)})
	}
	return out, nil
}

// harnessForBinary recognizes a path to a harness's executable by its file name.
func harnessForBinary(path string) (harness.Harness, bool) {
	fi, err := os.Stat(harness.ExpandHome(path))
	if err != nil || fi.IsDir() || fi.Mode().Perm()&0o111 == 0 {
		return harness.Harness{}, false
	}
	base := strings.TrimSuffix(filepath.Base(path), ".exe")
	for _, h := range harness.All() {
		for _, b := range h.Bins {
			if b == base {
				return h, true
			}
		}
	}
	return harness.Harness{}, false
}

type mcpFlags struct {
	harnessArg, format, name string
	tier                     int
	planner, global, dry     bool
	env                      []string
}

func (f *mcpFlags) register(cmd *cobra.Command) {
	fl := cmd.Flags()
	fl.StringVar(&f.harnessArg, "harness", "auto", "auto, all, ids, a harness executable, or a config file path")
	fl.StringVar(&f.format, "format", "", "for a config path: mcpServers-json, opencode-json or codex-toml; inferred if empty")
	fl.StringVar(&f.name, "name", harness.ServerName, "server name")
	fl.IntVar(&f.tier, "tier", 1, "tier for the swarm's own server")
	fl.BoolVar(&f.planner, "planner", false, "allow the swarm's own server to plan")
	fl.BoolVar(&f.global, "global", false, "use the harness's user-level config")
	fl.StringArrayVar(&f.env, "env", nil, "environment variable for a custom server, as KEY=VALUE; stored in plain text")
}

// entry builds the server to register: the words after -- for a custom server,
// otherwise the swarm's bridge, which needs no secret in the file.
func (f *mcpFlags) entry(args []string, h harness.Harness) (harness.Entry, error) {
	env := map[string]string{}
	for _, kv := range f.env {
		k, v, ok := strings.Cut(kv, "=")
		if !ok || k == "" {
			return harness.Entry{}, fmt.Errorf("--env %q: want KEY=VALUE", kv)
		}
		env[k] = v
	}
	if len(args) > 0 {
		if f.name == harness.ServerName {
			return harness.Entry{}, errors.New("a custom server needs its own --name")
		}
		return harness.Entry{Name: f.name, Command: args[0], Args: args[1:], Env: env}, nil
	}
	if f.name != harness.ServerName {
		return harness.Entry{}, errors.New("--name is for a custom server: give its command after --")
	}
	bridge := []string{"mcp", "--auto", "--tier", strconv.Itoa(f.tier)}
	if f.planner {
		bridge = append(bridge, "--planner")
	}
	if h.ID != "" && h.ID != "custom" {
		bridge = append(bridge, "--harness", h.ID)
	}
	return harness.Entry{Command: selfCommand(), Args: bridge, Env: env}, nil
}

func mcpAddCmd() *cobra.Command {
	var f mcpFlags
	cmd := &cobra.Command{
		Use:   "add [--harness TARGET] [--name NAME -- COMMAND ARGS...]",
		Short: "Add the swarm's MCP server, or any server, to a harness's config",
		Example: `  aiculator mcp add                               every installed harness
  aiculator mcp add --harness opencode --tier 2
  aiculator mcp add --harness claude,cursor --global
  aiculator mcp add --harness ~/.local/bin/opencode   found from the executable
  aiculator mcp add --harness ./custom/mcp.json       a config file; format inferred
  aiculator mcp add --name docs --harness claude -- npx -y docs-server`,
		RunE: func(_ *cobra.Command, args []string) error {
			targets, err := resolveTargets(f.harnessArg, f.format, f.global)
			if err != nil {
				return err
			}
			root := projectRoot()
			backups := filepath.Join(dataDir, "backups")
			if f.dry {
				backups = ""
			}
			for _, t := range targets {
				if t.h.MCP == harness.MCPNone {
					msg := "no config file that aiculator can write"
					if t.h.Source != "" && f.name == harness.ServerName {
						msg += "; it can join through the shell: aiculator work join"
					}
					fmt.Printf("%-10s skipped: %s\n", t.label, msg)
					continue
				}
				e, err := f.entry(args, t.h)
				if err != nil {
					return err
				}
				if f.dry {
					fmt.Printf("# %s: would write to %s\n%s\n", t.label, t.h.MCPFile, t.h.Snippet(e))
					continue
				}
				r, err := t.h.WriteMCP(root, e, backups)
				if err != nil {
					fmt.Printf("%-10s failed: %v\n", t.label, err)
					continue
				}
				state := "already set"
				if r.Changed {
					state = "written"
				}
				fmt.Printf("%-10s %s: %s\n", t.label, state, r.Path)
			}
			return nil
		},
	}
	f.register(cmd)
	cmd.Flags().BoolVar(&f.dry, "dry-run", false, "show what would be written and change nothing")
	return cmd
}

func mcpPrintCmd() *cobra.Command {
	var f mcpFlags
	cmd := &cobra.Command{
		Use:   "print [--harness TARGET] [--name NAME -- COMMAND ARGS...]",
		Short: "Print the config piece to add a server by hand; changes nothing",
		Example: `  aiculator mcp print --harness opencode
  aiculator mcp print --harness codex --planner
  aiculator mcp print --harness ./custom.toml
  aiculator mcp print --name docs --harness claude -- npx -y docs-server`,
		RunE: func(_ *cobra.Command, args []string) error {
			targets, err := resolveTargets(f.harnessArg, f.format, f.global)
			if err != nil {
				return err
			}
			for i, t := range targets {
				if i > 0 {
					fmt.Println()
				}
				h := t.h
				if h.MCP == harness.MCPNone {
					// No known file, but this shape is what most harnesses accept.
					fmt.Printf("# %s: no confirmed config file. This is the common shape; check its docs.\n", t.label)
					h.MCP = harness.MCPServersJSON
				} else {
					fmt.Printf("# %s: add to %s\n", t.label, h.MCPFile)
				}
				e, err := f.entry(args, t.h)
				if err != nil {
					return err
				}
				fmt.Print(h.Snippet(e))
				if len(t.cli) > 0 {
					fmt.Printf("# Or with its own command:\n#   %s\n", shellJoin(append(append(append([]string{}, t.cli...), f.name, "--"), append([]string{e.Command}, e.Args...)...)))
				}
			}
			return nil
		},
	}
	f.register(cmd)
	return cmd
}

func mcpRemoveCmd() *cobra.Command {
	var f mcpFlags
	cmd := &cobra.Command{
		Use:   "remove [--harness TARGET] [--name NAME]",
		Short: "Remove a server from a harness's config, and nothing else",
		RunE: func(*cobra.Command, []string) error {
			targets, err := resolveTargets(f.harnessArg, f.format, f.global)
			if err != nil {
				return err
			}
			root := projectRoot()
			for _, t := range targets {
				r, err := t.h.RemoveNamed(root, f.name, filepath.Join(dataDir, "backups"))
				if err != nil {
					fmt.Printf("%-10s failed: %v\n", t.label, err)
					continue
				}
				if r.Changed {
					fmt.Printf("%-10s removed from %s\n", t.label, r.Path)
				}
			}
			return nil
		},
	}
	f.register(cmd)
	return cmd
}

func shellJoin(words []string) string {
	out := make([]string, len(words))
	for i, w := range words {
		if w == "" || strings.ContainsAny(w, " \t'\"$`\\*?") {
			out[i] = "'" + strings.ReplaceAll(w, "'", `'\''`) + "'"
		} else {
			out[i] = w
		}
	}
	return strings.Join(out, " ")
}
