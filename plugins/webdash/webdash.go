// Package webdash is the web dashboard, embedded in the binary. Leave it out with
// go build -tags noweb.
//
// The page and its scripts hold no data, so they are served openly. All data comes from
// the API and needs a read-only token, which reaches the page in the URL fragment.
// Browsers never send a fragment to a server, and another website cannot read it, so
// neither can reach your swarm's data.
package webdash

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"net/http"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/Grano22/aiculator/pkg/plugin"
)

//go:embed static
var assets embed.FS

// Plugin is the web dashboard.
type Plugin struct{}

func init() { plugin.Register(Plugin{}) }

// Name implements plugin.Plugin.
func (Plugin) Name() string { return "webdash" }

// Description implements plugin.Plugin.
func (Plugin) Description() string { return "Web dashboard, embedded in the binary" }

// Setup implements plugin.Plugin.
func (Plugin) Setup(h *plugin.Host) error {
	sub, err := fs.Sub(assets, "static")
	if err != nil {
		return err
	}
	files := http.FileServerFS(sub)
	h.Handle("", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hdr := w.Header()
		// Nothing loads from elsewhere, nothing runs inline, and no site can frame it.
		hdr.Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; img-src 'self'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'")
		hdr.Set("X-Content-Type-Options", "nosniff")
		hdr.Set("X-Frame-Options", "DENY")
		hdr.Set("Referrer-Policy", "no-referrer")
		hdr.Set("Cache-Control", "no-cache")
		http.StripPrefix(h.Prefix(), files).ServeHTTP(w, r)
	}))
	return nil
}

// Commands implements plugin.Commander.
func (Plugin) Commands(cli *plugin.CLI) []*cobra.Command {
	var (
		noOpen bool
		ttl    time.Duration
	)
	cmd := &cobra.Command{
		Use:   "web",
		Short: "Open the web dashboard",
		Long: `Opens the live dashboard in your browser, using a read-only token that expires.

The dashboard is compiled into this binary; go build -tags noweb leaves it out.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			url, token, err := cli.ViewerToken(cmd.Context(), ttl)
			if err != nil {
				return err
			}
			link := fmt.Sprintf("%s/plugins/webdash/#token=%s", strings.TrimRight(url, "/"), token)
			fmt.Println(link)
			if !noOpen {
				if err := open(cmd.Context(), link); err != nil {
					fmt.Println("Could not open a browser, so open that link yourself.")
				}
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&noOpen, "no-open", false, "print the link and do not open a browser")
	cmd.Flags().DurationVar(&ttl, "ttl", 12*time.Hour, "how long the link works")
	return []*cobra.Command{cmd}
}

func open(ctx context.Context, url string) error {
	var c *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		c = exec.CommandContext(ctx, "open", url)
	case "windows":
		c = exec.CommandContext(ctx, "rundll32", "url.dll,FileProtocolHandler", url)
	default:
		c = exec.CommandContext(ctx, "xdg-open", url)
	}
	return c.Start()
}
