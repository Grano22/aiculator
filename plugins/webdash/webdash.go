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
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"strings"
	"syscall"
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
		noOpen, all, control bool
		ttl                  time.Duration
	)
	cmd := &cobra.Command{
		Use:   "web",
		Short: "Open the web dashboard",
		Long: `Opens the live dashboard in your browser, using a read-only token that expires.
With --control, the page can also stop or kill agents and clear gone sessions.

The dashboard is compiled into this binary; go build -tags noweb leaves it out.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if all {
				return serveFleet(cmd.Context(), cli, ttl, noOpen)
			}
			mint := cli.ViewerToken
			if control {
				if cli.ControlToken == nil {
					return fmt.Errorf("this build cannot issue control links")
				}
				mint = cli.ControlToken
			}
			url, token, err := mint(cmd.Context(), ttl)
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
	cmd.Flags().BoolVar(&all, "all", false, "show every realm on this machine, for as long as this command runs")
	cmd.Flags().BoolVar(&noOpen, "no-open", false, "print the link and do not open a browser")
	cmd.Flags().BoolVar(&control, "control", false, "a link whose page can also stop and kill agents and clear gone sessions")
	cmd.Flags().DurationVar(&ttl, "ttl", 12*time.Hour, "how long the link works")
	return []*cobra.Command{cmd}
}

// securityHeaders are the same for every page: nothing loads from elsewhere, nothing runs
// inline, and no site can frame it.
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hdr := w.Header()
		hdr.Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; img-src 'self'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'")
		hdr.Set("X-Content-Type-Options", "nosniff")
		hdr.Set("X-Frame-Options", "DENY")
		hdr.Set("Referrer-Policy", "no-referrer")
		hdr.Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

// FleetHandler serves the page across realms. Reading the realms takes this user's own
// tokens, so it runs in this command and not in any project's server: otherwise one project's
// server would have to hold the keys to all the others.
//
// analytics, when set, answers /api/fleet/analytics?realm=NAME for one realm's flow charts.
// The realm and filters go on to that realm's own server, which checks them.
func FleetHandler(session string, fetch func(ctx context.Context) (any, error), analytics func(ctx context.Context, realm, query string) (any, error)) (http.Handler, error) {
	sub, err := fs.Sub(assets, "static")
	if err != nil {
		return nil, err
	}
	mux := http.NewServeMux()
	mux.Handle("GET /", http.FileServerFS(sub))
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		http.ServeFileFS(w, r, sub, "fleet.html")
	})
	authorized := func(w http.ResponseWriter, r *http.Request) bool {
		got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if subtle.ConstantTimeCompare([]byte(got), []byte(session)) != 1 {
			http.Error(w, "this page needs the link printed by aiculator web --all", http.StatusUnauthorized)
			return false
		}
		return true
	}
	mux.HandleFunc("GET /api/fleet/analytics", func(w http.ResponseWriter, r *http.Request) {
		if !authorized(w, r) {
			return
		}
		if analytics == nil {
			http.Error(w, "analytics are not available here", http.StatusNotFound)
			return
		}
		q := r.URL.Query()
		name := q.Get("realm")
		q.Del("realm")
		// Only the known filters go on, so nothing else in this URL reaches a server.
		pass := url.Values{}
		for _, k := range []string{"window", "tier", "label", "points"} {
			if v := q.Get(k); v != "" {
				pass.Set(k, v)
			}
		}
		v, err := analytics(r.Context(), name, pass.Encode())
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(v)
	})
	mux.HandleFunc("GET /api/fleet", func(w http.ResponseWriter, r *http.Request) {
		if !authorized(w, r) {
			return
		}
		v, err := fetch(r.Context())
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(v)
	})
	return securityHeaders(mux), nil
}

func serveFleet(ctx context.Context, cli *plugin.CLI, ttl time.Duration, noOpen bool) error {
	var raw [24]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return err
	}
	session := hex.EncodeToString(raw[:])
	var analytics func(ctx context.Context, realm, query string) (any, error)
	if cli.Analytics != nil {
		analytics = func(ctx context.Context, realm, query string) (any, error) { return cli.Analytics(ctx, realm, query) }
	}
	h, err := FleetHandler(session, func(ctx context.Context) (any, error) { return cli.Fleet(ctx, ttl) }, analytics)
	if err != nil {
		return err
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	srv := &http.Server{Handler: h, ReadHeaderTimeout: 5 * time.Second}
	link := fmt.Sprintf("http://%s/#token=%s", ln.Addr(), session)
	fmt.Println(link)
	fmt.Println("Press Ctrl-C to stop.")
	if !noOpen {
		if err := open(ctx, link); err != nil {
			fmt.Println("Could not open a browser, so open that link yourself.")
		}
	}
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		_ = srv.Close()
	}()
	if err := srv.Serve(ln); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
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
