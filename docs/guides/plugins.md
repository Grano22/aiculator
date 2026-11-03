# Plugins

There are three ways to extend Aiculator, depending on how much you need.

| Way | Needs a rebuild? | Can do | Use it for |
|---|---|---|---|
| **Event stream** | No | Read every task change | Notifications, logs, in any language |
| **Program on `PATH`** | No | Add a command | Your own tools |
| **Compiled in** | Yes | Add commands, server routes and event handlers | Features that ship inside the binary |

## List what you have

```console
$ aiculator plugin
NAME     KIND         DESCRIPTION
webdash  compiled in  Web dashboard, embedded in the binary
```

## Event stream

```sh
aiculator events | while read -r line; do
  echo "$line" | jq -r 'select(.state == "done") | .id' | xargs -I{} notify-send "Done: {}"
done
```

## A program on PATH

Any executable named `aiculator-NAME` runs as `aiculator NAME`, the way git does it. It receives `AICULATOR_DATA` and `AICULATOR_VERSION`.

```sh
#!/bin/sh
# Save as aiculator-greet, somewhere on your PATH.
echo "Hello from $AICULATOR_VERSION"
```

Built-in commands always win, so a program cannot replace one. Only `PATH` is searched, so cloning a repository cannot add a command.

## A compiled-in plugin

Plugins compile in because Go's dynamic `plugin` package needs cgo, and this binary is static.

A plugin registers itself and implements up to two interfaces:

```go title="examples/plugins/hello/hello.go"
package hello

import (
	"fmt"
	"net/http"
	"sync/atomic"

	"github.com/spf13/cobra"

	"github.com/Grano22/aiculator/pkg/plugin"
)

type Plugin struct{}

func init() { plugin.Register(Plugin{}) }

func (Plugin) Name() string        { return "hello" }
func (Plugin) Description() string { return "Example plugin that counts finished tasks" }

// Setup runs once in the server.
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

// Commands is optional, and adds CLI commands.
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
```

Include it with a small file in `cmd/aiculator`, in your own fork:

```go title="cmd/aiculator/plugins_hello.go"
//go:build example_hello

package main

import _ "github.com/Grano22/aiculator/examples/plugins/hello"
```

Then build with its tag:

```sh
go build -tags example_hello ./cmd/aiculator
```

Leave out the build tag line if it should always be included. The web dashboard works the opposite way: it is in by default, and `-tags noweb` removes it.

### What a plugin can do

| Method | Purpose |
|---|---|
| `h.Handle(path, handler)` | A route with no token, for what anyone on this machine may read, such as static files |
| `h.HandleViewer(path, handler)` | A route that needs a viewer or admin token |
| `h.OnEvent(fn)` | Be told about every task change |
| `h.Snapshot(ctx)` | Read the live dashboard data |
| `h.Log` | Log with the plugin's name attached |
| `Commands(cli)` | Add CLI commands, and use `cli.Snapshot` and `cli.ViewerToken` |

### What the swarm guarantees

- **A plugin cannot take the server down.** One that fails or panics in setup is disabled alone, and a panic in a handler fails only that request or event.
- **A slow plugin cannot slow the swarm.** Events are dropped for it rather than waited on, so delivery is best effort.
- **Routes stay in their own space,** under `/plugins/NAME/`, so a plugin cannot shadow the API or another plugin.
- **A command cannot replace a built-in.** It is skipped, with a warning.

!!! danger "Compiled-in plugins are fully trusted"
    A plugin is ordinary Go code in your process, and these guarantees are about mistakes, not malice. Read a plugin before you build it in.

`pkg/plugin` and `pkg/observe` are the public API. They may still change before version 1.0.
