package plugin

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Grano22/aiculator/pkg/observe"
)

type fake struct {
	name  string
	setup func(*Host) error
}

func (f fake) Name() string        { return f.name }
func (f fake) Description() string { return "test" }
func (f fake) Setup(h *Host) error { return f.setup(h) }

func start(t *testing.T, ps ...Plugin) (*Manager, *http.ServeMux) {
	t.Helper()
	mux := http.NewServeMux()
	m := Start(ps, Config{
		Router: mux, Version: "v1", Log: slog.New(slog.DiscardHandler),
		Guard: func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") == "" {
					http.Error(w, "no", http.StatusUnauthorized)
					return
				}
				next.ServeHTTP(w, r)
			})
		},
		Snapshot: func(context.Context) (observe.Snapshot, error) { return observe.Snapshot{Version: "v1"}, nil },
	})
	return m, mux
}

func get(mux *http.ServeMux, path string, auth bool) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodGet, path, nil)
	if auth {
		r.Header.Set("Authorization", "x")
	}
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	return w
}

func ok(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "hello") }

func TestRoutesStayUnderThePluginsPrefix(t *testing.T) {
	_, mux := start(t, fake{"a", func(h *Host) error {
		h.Handle("/open", http.HandlerFunc(ok))
		h.HandleViewer("private", http.HandlerFunc(ok))
		return nil
	}})
	if w := get(mux, "/plugins/a/open", false); w.Code != 200 || w.Body.String() != "hello" {
		t.Fatalf("open: %d", w.Code)
	}
	if w := get(mux, "/plugins/a/private", false); w.Code != 401 {
		t.Fatalf("private without a token: %d", w.Code)
	}
	if w := get(mux, "/plugins/a/private", true); w.Code != 200 {
		t.Fatalf("private with a token: %d", w.Code)
	}
	if w := get(mux, "/open", false); w.Code != 404 {
		t.Fatalf("a route escaped the prefix: %d", w.Code)
	}
}

func TestOneBadPluginDoesNotStopTheOthers(t *testing.T) {
	m, mux := start(t,
		fake{"fails", func(*Host) error { return errors.New("boom") }},
		fake{"panics", func(*Host) error { panic("kaboom") }},
		fake{"good", func(h *Host) error { h.Handle("x", http.HandlerFunc(ok)); return nil }},
	)
	st := m.Statuses()
	if st[0].Running || st[0].Error != "boom" || st[1].Running || !strings.Contains(st[1].Error, "kaboom") || !st[2].Running {
		t.Fatalf("statuses: %+v", st)
	}
	if w := get(mux, "/plugins/good/x", false); w.Code != 200 {
		t.Fatalf("the good plugin: %d", w.Code)
	}
}

func TestPanickingHandlerOnlyFailsItsRequest(t *testing.T) {
	_, mux := start(t, fake{"p", func(h *Host) error {
		h.Handle("boom", http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("x") }))
		return nil
	}})
	if w := get(mux, "/plugins/p/boom", false); w.Code != 500 {
		t.Fatalf("code: %d", w.Code)
	}
}

func TestEventsReachPluginsAndAPanicIsContained(t *testing.T) {
	got := make(chan string, 4)
	m, _ := start(t, fake{"a", func(h *Host) error {
		h.OnEvent(func(e Event) {
			if e.TaskID == "bad" {
				panic("x")
			}
			got <- e.TaskID
		})
		return nil
	}})
	defer m.Stop()
	m.Publish(Event{TaskID: "bad"})
	m.Publish(Event{TaskID: "t1"})
	select {
	case id := <-got:
		if id != "t1" {
			t.Fatalf("got %s", id)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("delivery stopped after a panic")
	}
}

func TestSlowPluginNeverBlocksPublish(t *testing.T) {
	release := make(chan struct{})
	m, _ := start(t, fake{"slow", func(h *Host) error {
		h.OnEvent(func(Event) { <-release })
		return nil
	}})
	done := make(chan struct{})
	go func() {
		for range 10000 {
			m.Publish(Event{})
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("a slow plugin blocked the server")
	}
	close(release)
	m.Stop()
}

func TestRegisterRejectsBadNames(t *testing.T) {
	for _, n := range []string{"", "Bad", "a/b", "../x", "a--b"} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("%q was accepted", n)
				}
			}()
			Register(fake{n, nil})
		}()
	}
}
