package webdash

import (
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
