package main

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/Grano22/aiculator/internal/api"
	"github.com/Grano22/aiculator/internal/realm"
	"github.com/Grano22/aiculator/internal/tui"
)

type realmIdentity struct{ name, path, instance string }

// registerRealm adds this project to the machine's directory. The swarm works without it,
// so a failure here is only logged.
func registerRealm(log *slog.Logger) realmIdentity {
	abs, err := filepath.Abs(dataDir)
	if err != nil {
		return realmIdentity{}
	}
	id := realmIdentity{path: filepath.Dir(abs), instance: realm.Instance(abs), name: filepath.Base(filepath.Dir(abs))}
	r, err := realm.Register(id.path, abs)
	if err != nil {
		log.Warn("this project was not added to the realm directory", "err", err)
		return id
	}
	id.name = r.Name
	return id
}

// fleet reads every realm for the dashboards.
func fleet(ctx context.Context) ([]tui.FleetRow, error) {
	views, err := realm.Gather(ctx, nil)
	if err != nil {
		return nil, err
	}
	out := make([]tui.FleetRow, len(views))
	for i, v := range views {
		out[i] = tui.FleetRow{Name: v.Realm.Name, Path: v.Realm.Path, Live: v.Live, Missing: v.Missing}
		if v.Snapshot != nil {
			out[i].Snapshot = *v.Snapshot
		}
	}
	return out, nil
}

// fleetWithLinks also makes a read-only link to each running realm's own dashboard.
func fleetWithLinks(ctx context.Context, ttlSec int64) ([]realm.View, error) {
	return realm.Gather(ctx, func(c context.Context, r realm.Realm, v *realm.View) {
		cl, err := realm.Local(r)
		if err != nil {
			return
		}
		var out map[string]string
		q := api.TokenReq{Agent: "viewer-" + randomSuffix(), Viewer: true, TTLSec: ttlSec}
		if cl.Post(c, "/v1/tokens", q, &out) == nil && out["token"] != "" {
			v.Link = cl.URL + "/plugins/webdash/#token=" + out["token"]
		}
	})
}

func realmCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "realm",
		Short: "See and name the projects on this machine that use the swarm",
		Long: `A realm is one project, with its own server and its own store. A project becomes one
when its server starts. Realms never share tasks or tokens, so trouble in one stays there.

Act on one from any folder with --realm NAME, such as: aiculator --realm shop task ls
See them all with: aiculator top --all, or aiculator web --all`,
	}
	ls := &cobra.Command{
		Use:   "ls",
		Short: "List the realms, and which are running",
		RunE: func(cmd *cobra.Command, _ []string) error {
			views, err := realm.Gather(cmd.Context(), nil)
			if err != nil {
				return err
			}
			if asJSON {
				for i := range views {
					views[i].Snapshot = nil
				}
				return printJSON(views)
			}
			if len(views) == 0 {
				fmt.Println("No realms yet. A project becomes one when its server starts: aiculator join")
				return nil
			}
			abs, _ := filepath.Abs(dataDir)
			fmt.Printf("  %-24s %-9s %s\n", "NAME", "STATE", "PATH")
			for _, v := range views {
				state := "stopped"
				switch {
				case v.Missing:
					state = "missing"
				case v.Live:
					state = "running"
				}
				mark := " "
				if v.Realm.DataDir == abs {
					mark = "*"
				}
				fmt.Printf("%s %-24s %-9s %s\n", mark, v.Realm.Name, state, v.Realm.Path)
			}
			return nil
		},
	}
	ls.Flags().BoolVar(&asJSON, "json", false, "print as JSON")
	rename := &cobra.Command{
		Use:   "rename OLD NEW",
		Short: "Give a realm a new name",
		Args:  cobra.ExactArgs(2),
		RunE: func(_ *cobra.Command, args []string) error {
			return realm.Rename(args[0], args[1])
		},
	}
	forget := &cobra.Command{
		Use:   "forget NAME",
		Short: "Take a realm off the list; the project and its data are not touched",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			return realm.Forget(args[0])
		},
	}
	cmd.AddCommand(ls, rename, forget, realmStopCmd())
	return cmd
}
