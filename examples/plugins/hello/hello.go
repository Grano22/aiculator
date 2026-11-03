// Package hello is the smallest useful plugin: a command, a route and an event handler.
// Build it in with: go build -tags example_hello ./cmd/aiculator
package hello

import (
	"fmt"
	"net/http"
	"sync/atomic"

	"github.com/spf13/cobra"

	"github.com/Grano22/aiculator/pkg/plugin"
)

// Plugin counts finished tasks.
type Plugin struct{}

func init() { plugin.Register(Plugin{}) }

// Name implements plugin.Plugin.
func (Plugin) Name() string { return "hello" }

// Description implements plugin.Plugin.
func (Plugin) Description() string { return "Example plugin that counts finished tasks" }

// Setup implements plugin.Plugin.
func (Plugin) Setup(h *plugin.Host) error {
	var done atomic.Int64
	h.OnEvent(func(e plugin.Event) {
		if e.State == "done" {
			done.Add(1)
		}
	})
	h.HandleViewer("count", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, "%d\n", done.Load())
	}))
	return nil
}

// Commands implements plugin.Commander.
func (Plugin) Commands(cli *plugin.CLI) []*cobra.Command {
	return []*cobra.Command{{
		Use:   "hello",
		Short: "Say how many tasks are done",
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, err := cli.Snapshot(cmd.Context())
			if err != nil {
				return err
			}
			fmt.Printf("Hello. %d tasks are done.\n", s.States["done"])
			return nil
		},
	}}
}
