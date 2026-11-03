package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/Grano22/aiculator/internal/api"
	"github.com/Grano22/aiculator/internal/vlog"
)

var (
	verbosity int
	vmodule   string
	logFormat string
	logFile   string
)

func addLoggingFlags(root *cobra.Command) {
	f := root.PersistentFlags()
	f.IntVarP(&verbosity, "v", "v", 0, "log verbosity, 0 to 9, as in kubectl; see aiculator debug levels. A server defaults to 2")
	f.StringVar(&vmodule, "vmodule", "", "levels per component, such as engine=5,driver=3; wildcards work")
	f.StringVar(&logFormat, "log-format", "text", "log format: text or json")
	f.StringVar(&logFile, "log-file", "", "write logs to this file instead of stderr")
}

// setupLogging applies the flags. A flag beats the environment, which beats the default.
func setupLogging(cmd *cobra.Command) error {
	level := 0
	if cmd.Name() == "serve" {
		level = vlog.LevelSteady
	}
	if env := os.Getenv("AICULATOR_V"); env != "" {
		n, err := strconv.Atoi(env)
		if err != nil {
			return fmt.Errorf("AICULATOR_V=%q: want a number from 0 to %d", env, vlog.MaxLevel)
		}
		level = n
	}
	if cmd.Flags().Changed("v") {
		level = verbosity
	}
	mod := vmodule
	if !cmd.Flags().Changed("vmodule") {
		mod = os.Getenv("AICULATOR_VMODULE")
	}
	verbosity, vmodule = level, mod
	opts := vlog.Options{Level: level, VModule: mod, Format: logFormat}
	if logFile != "" {
		f, err := os.OpenFile(logFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			return err
		}
		opts.Output = f
	}
	return vlog.Setup(opts)
}

// loggingArgs passes the choices on to a server started in the background. The server's
// own default applies unless a level was asked for.
func loggingArgs() []string {
	args := []string{"--log-format", logFormat}
	if verbosity > 0 {
		args = append(args, "--v", strconv.Itoa(verbosity))
	}
	if vmodule != "" {
		args = append(args, "--vmodule", vmodule)
	}
	return args
}

func debugCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "debug",
		Short: "Debugging tools: verbosity levels, and changing them on a running server",
	}
	cmd.AddCommand(&cobra.Command{
		Use:   "levels",
		Short: "Explain what each verbosity level shows",
		RunE: func(*cobra.Command, []string) error {
			for i, d := range vlog.Descriptions {
				fmt.Printf("%d  %s\n", i, d)
			}
			fmt.Println("\nComponents for --vmodule: engine, work, driver, swarm, sandbox, mcp, http, client, store.")
			return nil
		},
	})
	var (
		mod string
		dur time.Duration
	)
	v := &cobra.Command{
		Use:   "verbosity [LEVEL]",
		Short: "Show or change a running server's verbosity, with no restart",
		Long: `Changes take effect at once. Use --for, so that a debugging session cannot leave
the server logging at a high level by accident. The previous level then comes back by itself.`,
		Example: `  aiculator debug verbosity                     show the current level
  aiculator debug verbosity 5 --for 10m         trace for ten minutes
  aiculator debug verbosity --vmodule engine=5 --for 5m
  aiculator debug verbosity 2                   set it for good`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := adminClient()
			if err != nil {
				return fmt.Errorf("%w; start it with: aiculator up", err)
			}
			var st api.VerbosityState
			if len(args) == 0 && !cmd.Flags().Changed("vmodule") {
				if err := c.Get(cmd.Context(), "/v1/debug/verbosity", &st); err != nil {
					return err
				}
				return printVerbosity(st)
			}
			q := api.VerbosityReq{ForSeconds: int64(dur.Seconds())}
			if len(args) == 1 {
				n, err := strconv.Atoi(args[0])
				if err != nil {
					return fmt.Errorf("level %q: want a number from 0 to %d", args[0], vlog.MaxLevel)
				}
				q.Level = &n
			}
			if cmd.Flags().Changed("vmodule") {
				q.VModule = &mod
			}
			if err := c.Do(cmd.Context(), http.MethodPut, "/v1/debug/verbosity", q, &st); err != nil {
				return err
			}
			return printVerbosity(st)
		},
	}
	v.Flags().StringVar(&mod, "vmodule", "", "levels per component, such as engine=5")
	v.Flags().DurationVar(&dur, "for", 0, "go back to the previous level after this long")
	cmd.AddCommand(v)
	return cmd
}

func printVerbosity(st api.VerbosityState) error {
	fmt.Printf("level %d", st.Level)
	if st.VModule != "" {
		fmt.Printf("  vmodule %s", st.VModule)
	}
	if !st.RevertsAt.IsZero() {
		fmt.Printf("  until %s", st.RevertsAt.Local().Format(time.TimeOnly))
	}
	fmt.Println()
	return nil
}

func logsCmd() *cobra.Command {
	var (
		follow    bool
		tail      int
		component string
		grep      string
	)
	cmd := &cobra.Command{
		Use:   "logs",
		Short: "Show the logs of a server started with aiculator up",
		Example: `  aiculator logs --tail 50
  aiculator logs -f --component engine
  aiculator logs -f --grep ship-api-3`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			path := filepath.Join(dataDir, "server.log")
			f, err := os.Open(path)
			if errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("no log at %s; it exists only for a server started with aiculator up", path)
			}
			if err != nil {
				return err
			}
			defer f.Close()
			match := func(line string) bool {
				if component != "" && !strings.Contains(line, "component="+component) && !strings.Contains(line, `"component":"`+component+`"`) {
					return false
				}
				return grep == "" || strings.Contains(line, grep)
			}
			var last []string
			r := bufio.NewReader(f)
			partial := ""
			for {
				chunk, err := r.ReadString('\n')
				partial += chunk
				if err != nil {
					break
				}
				if match(partial) {
					last = append(last, partial)
					if tail > 0 && len(last) > tail {
						last = last[1:]
					}
				}
				partial = ""
			}
			for _, l := range last {
				fmt.Print(l)
			}
			for follow {
				chunk, err := r.ReadString('\n')
				partial += chunk
				if errors.Is(err, io.EOF) {
					select {
					case <-cmd.Context().Done():
						return nil
					case <-time.After(200 * time.Millisecond):
					}
					continue
				}
				if err != nil {
					return err
				}
				if match(partial) {
					fmt.Print(partial)
				}
				partial = ""
			}
			return nil
		},
	}
	f := cmd.Flags()
	f.BoolVarP(&follow, "follow", "f", false, "keep printing new lines")
	f.IntVar(&tail, "tail", 0, "show only the last N matching lines")
	f.StringVar(&component, "component", "", "show only this component")
	f.StringVar(&grep, "grep", "", "show only lines containing this text")
	return cmd
}
