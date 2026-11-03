package main

import (
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/Grano22/aiculator/internal/core"
)

// parseAging reads steps such as 1h=5 or 30m=10, and sorts them by time.
func parseAging(items []string) ([]core.AgingStep, error) {
	out := make([]core.AgingStep, 0, len(items))
	for _, it := range items {
		after, boost, ok := strings.Cut(it, "=")
		if !ok {
			return nil, fmt.Errorf("aging step %q: use a form such as 1h=5", it)
		}
		d, err := time.ParseDuration(after)
		if err != nil || d < time.Second {
			return nil, fmt.Errorf("aging step %q: bad wait", it)
		}
		b, err := strconv.Atoi(boost)
		if err != nil {
			return nil, fmt.Errorf("aging step %q: bad boost", it)
		}
		out = append(out, core.AgingStep{AfterSec: int64(d / time.Second), Boost: b})
	}
	slices.SortFunc(out, func(a, b core.AgingStep) int { return int(a.AfterSec - b.AfterSec) })
	return out, nil
}

// parseLabels reads k=v pairs. An empty value removes the label when amending.
func parseLabels(items []string) (map[string]string, error) {
	if len(items) == 0 {
		return nil, nil
	}
	out := map[string]string{}
	for _, it := range items {
		k, v, ok := strings.Cut(it, "=")
		if !ok || k == "" {
			return nil, fmt.Errorf("label %q: use name=value", it)
		}
		out[k] = v
	}
	return out, nil
}

func amendCmd() *cobra.Command {
	var (
		title, spec   string
		priority      int
		labels, aging []string
	)
	cmd := &cobra.Command{
		Use:   "amend ID",
		Short: "Change a task's title, spec, priority, labels or aging, without touching its progress",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var a core.Amendment
			f := cmd.Flags()
			if f.Changed("title") {
				a.Title = &title
			}
			if f.Changed("spec") {
				a.Spec = &spec
			}
			if f.Changed("priority") {
				a.Priority = &priority
			}
			var err error
			if a.Labels, err = parseLabels(labels); err != nil {
				return err
			}
			if f.Changed("aging") {
				steps, err := parseAging(aging)
				if err != nil {
					return err
				}
				a.Aging = &steps
			}
			c, err := adminClient()
			if err != nil {
				return err
			}
			var t core.Task
			if err := c.Post(cmd.Context(), "/v1/tasks/"+args[0]+"/amend", a, &t); err != nil {
				return err
			}
			fmt.Printf("%s: %s, priority %d", t.ID, t.Title, t.Priority)
			if keys := slices.Sorted(maps.Keys(t.Labels)); len(keys) > 0 {
				fmt.Printf(", labels %s", strings.Join(keys, " "))
			}
			fmt.Println()
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&title, "title", "", "new title")
	f.StringVar(&spec, "spec", "", "new instructions")
	f.IntVar(&priority, "priority", 0, "new priority")
	f.StringArrayVar(&labels, "label", nil, "label as name=value; name= removes it")
	f.StringSliceVar(&aging, "aging", nil, "replace the aging steps, as wait=boost, such as 1h=5")
	return cmd
}

func priorityCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "priority ID",
		Short: "Show how a task's priority is worked out right now",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := adminClient()
			if err != nil {
				return err
			}
			var v any
			if err := c.Get(cmd.Context(), "/v1/tasks/"+args[0]+"/priority", &v); err != nil {
				return err
			}
			return printJSON(v)
		},
	}
}
