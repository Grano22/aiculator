package realm

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Grano22/aiculator/pkg/observe"
)

func setup(t *testing.T) string {
	t.Helper()
	t.Setenv("AICULATOR_REALMS", filepath.Join(t.TempDir(), "realms"))
	return t.TempDir()
}

func project(t *testing.T, base, name string) (string, string) {
	t.Helper()
	p := filepath.Join(base, name)
	d := filepath.Join(p, ".swarm")
	if err := os.MkdirAll(d, 0o700); err != nil {
		t.Fatal(err)
	}
	return p, d
}

func TestRegisterKeepsANameAndAvoidsClashes(t *testing.T) {
	base := setup(t)
	p, d := project(t, base, "Shop API")
	r, err := Register(p, d)
	if err != nil || r.Name != "shop-api" {
		t.Fatalf("%+v %v", r, err)
	}
	if err := Rename("shop-api", "shop"); err != nil {
		t.Fatal(err)
	}
	if again, _ := Register(p, d); again.Name != "shop" {
		t.Errorf("a restart should keep the chosen name: %+v", again)
	}
	p2, d2 := project(t, filepath.Join(base, "other"), "shop")
	r2, err := Register(p2, d2)
	if err != nil || r2.Name == "shop" || !strings.HasPrefix(r2.Name, "shop-") {
		t.Fatalf("two realms got one name: %+v %v", r2, err)
	}
	if err := Rename("shop", r2.Name); err == nil {
		t.Error("a rename onto a taken name was accepted")
	}
	if err := Rename("shop", "Bad Name"); err == nil {
		t.Error("a bad name was accepted")
	}
}

func TestDirectoryHoldsNoSecrets(t *testing.T) {
	base := setup(t)
	p, d := project(t, base, "a")
	if err := os.WriteFile(filepath.Join(d, "admin.token"), []byte("sekret-admin-token"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Register(p, d); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(Dir())
	for _, e := range entries {
		b, _ := os.ReadFile(filepath.Join(Dir(), e.Name()))
		if strings.Contains(string(b), "sekret") {
			t.Fatalf("a token reached the directory: %s", b)
		}
		if fi, _ := e.Info(); fi.Mode().Perm() != 0o600 {
			t.Errorf("mode %v", fi.Mode().Perm())
		}
	}
}

func TestConcurrentRegistrationLosesNothing(t *testing.T) {
	base := setup(t)
	var wg sync.WaitGroup
	for i := range 24 {
		wg.Go(func() {
			p, d := project(t, base, "p"+string(rune('a'+i)))
			if _, err := Register(p, d); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	all, err := List()
	if err != nil || len(all) != 24 {
		t.Fatalf("%d realms, %v", len(all), err)
	}
}

func TestDamagedEntryIsSkipped(t *testing.T) {
	base := setup(t)
	p, d := project(t, base, "good")
	if _, err := Register(p, d); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(Dir(), "bad.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	all, err := List()
	if err != nil || len(all) != 1 {
		t.Fatalf("%+v %v", all, err)
	}
}

func TestForgetLeavesTheProjectAlone(t *testing.T) {
	base := setup(t)
	p, d := project(t, base, "a")
	if _, err := Register(p, d); err != nil {
		t.Fatal(err)
	}
	if err := Forget("a"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(d); err != nil {
		t.Error("forgetting removed the project's data")
	}
	if _, err := Get("a"); !errors.Is(err, ErrNotFound) {
		t.Errorf("%v", err)
	}
}

func serve(t *testing.T, d, token string, delay time.Duration, states map[string]int) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(delay)
		if r.Header.Get("Authorization") != "Bearer "+token {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(observe.Snapshot{States: states})
	}))
	t.Cleanup(srv.Close)
	if err := os.WriteFile(filepath.Join(d, "addr"), []byte(srv.URL), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(d, "admin.token"), []byte(token), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestGatherIsNotHeldUpByABadRealm(t *testing.T) {
	base := setup(t)
	reg := func(name string) string {
		p, d := project(t, base, name)
		if _, err := Register(p, d); err != nil {
			t.Fatal(err)
		}
		return d
	}
	serve(t, reg("up"), "t1", 0, map[string]int{"ready": 3})
	serve(t, reg("stuck"), "t2", 10*time.Second, nil)
	reg("down")
	gone := reg("gone")
	if err := os.RemoveAll(filepath.Dir(gone)); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	views, err := Gather(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if took := time.Since(start); took > 2*perRealm {
		t.Errorf("a stuck realm held the view for %v", took)
	}
	got := map[string]View{}
	for _, v := range views {
		got[v.Realm.Name] = v
	}
	if !got["up"].Live || got["up"].Snapshot.States["ready"] != 3 {
		t.Errorf("up: %+v", got["up"])
	}
	if got["stuck"].Live || got["down"].Live || got["down"].Error != "not running" {
		t.Errorf("%+v %+v", got["stuck"], got["down"])
	}
	if !got["gone"].Missing {
		t.Errorf("%+v", got["gone"])
	}
}

// After a crash, an address file can point at a port another project's server now uses.
func TestStaleAddressOfAnotherProjectIsRefused(t *testing.T) {
	base := setup(t)
	p, d := project(t, base, "a")
	if _, err := Register(p, d); err != nil {
		t.Fatal(err)
	}
	serve(t, d, "real", 0, nil)
	if err := os.WriteFile(filepath.Join(d, "admin.token"), []byte("old-token"), 0o600); err != nil {
		t.Fatal(err)
	}
	views, _ := Gather(context.Background(), nil)
	if views[0].Live {
		t.Fatal("it showed another project's server as its own")
	}
}
