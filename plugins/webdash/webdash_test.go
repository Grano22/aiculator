package webdash

import (
	"context"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/Grano22/aiculator/pkg/plugin"
)

func serve(t *testing.T) *http.ServeMux {
	t.Helper()
	mux := http.NewServeMux()
	m := plugin.Start([]plugin.Plugin{Plugin{}}, plugin.Config{
		Router: mux, Log: slog.New(slog.DiscardHandler),
		Guard: func(h http.Handler) http.Handler { return h },
	})
	if st := m.Statuses(); !st[0].Running {
		t.Fatalf("status: %+v", st)
	}
	return mux
}

func TestAssetsAreServedWithAStrictPolicy(t *testing.T) {
	mux := serve(t)
	for path, want := range map[string]string{
		"/plugins/webdash/":          "Aiculator dashboard",
		"/plugins/webdash/app.js":    "use strict",
		"/plugins/webdash/style.css": "color-scheme",
	} {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		if w.Code != 200 || !strings.Contains(w.Body.String(), want) {
			t.Errorf("%s: %d", path, w.Code)
		}
		csp := w.Header().Get("Content-Security-Policy")
		if !strings.Contains(csp, "default-src 'none'") || !strings.Contains(csp, "frame-ancestors 'none'") || strings.Contains(csp, "unsafe") {
			t.Errorf("%s: policy: %q", path, csp)
		}
		if w.Header().Get("X-Content-Type-Options") != "nosniff" {
			t.Errorf("%s: no nosniff", path)
		}
	}
}

func read(t *testing.T, name string) string {
	t.Helper()
	b, err := fs.ReadFile(assets, "static/"+name)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// Task titles and agent names come from agents. They must never be parsed as HTML.
func TestScriptNeverParsesDataAsHTML(t *testing.T) {
	js := read(t, "app.js")
	code := regexp.MustCompile(`(?m)^\s*//.*$`).ReplaceAllString(js, "")
	for _, bad := range []string{"innerHTML", "outerHTML", "insertAdjacentHTML", "document.write", "eval(", "new Function", "DOMParser"} {
		if strings.Contains(code, bad) {
			t.Errorf("app.js uses %s", bad)
		}
	}
}

// The strict policy forbids these, and they would also break the page offline.
func TestNothingLoadsFromElsewhereOrRunsInline(t *testing.T) {
	html := read(t, "index.html")
	for _, name := range []string{"index.html", "app.js", "style.css"} {
		text := read(t, name)
		for _, m := range regexp.MustCompile(`https?://[^\s"')]+`).FindAllString(text, -1) {
			if m != "http://www.w3.org/2000/svg" { // A namespace name, never fetched.
				t.Errorf("%s refers to %s", name, m)
			}
		}
	}
	if regexp.MustCompile(`<script(?:\s[^>]*)?>[^<]`).MatchString(html) || regexp.MustCompile(`<style|\son[a-z]+=|\sstyle=`).MatchString(html) {
		t.Error("index.html has inline code or styles, which the policy blocks")
	}
}

func TestEveryElementTheScriptUsesExists(t *testing.T) {
	html, js := read(t, "index.html"), read(t, "app.js")
	for _, m := range regexp.MustCompile(`\$\("([a-z0-9-]+)"\)`).FindAllStringSubmatch(js, -1) {
		if !strings.Contains(html, `id="`+m[1]+`"`) {
			t.Errorf("app.js uses #%s, which index.html does not have", m[1])
		}
	}
}

func TestTheTokenLeavesTheAddressBar(t *testing.T) {
	js := read(t, "app.js")
	if !strings.Contains(js, "history.replaceState") {
		t.Error("the token stays visible in the address bar")
	}
	if strings.Contains(js, "localStorage") {
		t.Error("the token should not outlive the tab")
	}
}

func TestFleetPageNeedsTheSessionToken(t *testing.T) {
	h, err := FleetHandler("right", func(context.Context) (any, error) { return []string{"secret-realm"}, nil })
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(h)
	defer srv.Close()
	get := func(path, token string) (int, string, http.Header) {
		req, _ := http.NewRequest("GET", srv.URL+path, nil)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b), resp.Header
	}
	for _, tok := range []string{"", "wrong"} {
		if code, body, _ := get("/api/fleet", tok); code != 401 || strings.Contains(body, "secret-realm") {
			t.Errorf("token %q: %d %s", tok, code, body)
		}
	}
	if code, body, _ := get("/api/fleet", "right"); code != 200 || !strings.Contains(body, "secret-realm") {
		t.Errorf("%d %s", code, body)
	}
	code, body, hdr := get("/", "")
	if code != 200 || !strings.Contains(body, "fleet.js") {
		t.Errorf("%d %s", code, body)
	}
	if !strings.Contains(hdr.Get("Content-Security-Policy"), "default-src 'none'") {
		t.Errorf("%v", hdr)
	}
	if code, _, _ := get("/fleet.js", ""); code != 200 {
		t.Errorf("fleet.js: %d", code)
	}
}

func TestFleetScriptNeverParsesDataAsHTML(t *testing.T) {
	b, err := assets.ReadFile("static/fleet.js")
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"innerHTML", "outerHTML", "insertAdjacentHTML", "document.write", "eval("} {
		if strings.Contains(string(b), bad) {
			t.Errorf("fleet.js uses %s", bad)
		}
	}
	// A link comes from a realm's own server, and is checked before it can become a link.
	if !strings.Contains(string(b), "safeLink") {
		t.Error("links are not checked")
	}
}
