package meta

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"
)

var now = time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)

func TestApplyMergesAndRemoves(t *testing.T) {
	m, err := Meta{}.Apply(Patch{Props: map[string]*Prop{"a": {Value: 1}, "b": {Value: "x"}}}, now)
	if err != nil {
		t.Fatal(err)
	}
	n, err := m.Apply(Patch{Props: map[string]*Prop{"a": nil, "c": {Blob: &BlobRef{SHA256: strings.Repeat("a", 64)}}}}, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := n.Props["a"]; ok || n.Props["b"].Value != "x" || n.Props["c"].Blob == nil {
		t.Fatalf("%+v", n)
	}
	if _, ok := m.Props["a"]; !ok {
		t.Fatal("Apply changed its input")
	}
}

func TestApplyRefusesBadProps(t *testing.T) {
	big := strings.Repeat("x", MaxBytes+1)
	for name, p := range map[string]*Prop{
		"empty":     {},
		"two kinds": {Value: 1, Blob: &BlobRef{SHA256: strings.Repeat("a", 64)}},
		"bad hash":  {Blob: &BlobRef{SHA256: "../../etc/passwd"}},
		"file url":  {Resource: &Resource{Kind: "http", URL: "file:///etc/passwd"}},
		"no host":   {Resource: &Resource{Kind: "http", URL: "https://"}},
		"no kind":   {Resource: &Resource{URL: "https://a.example/x"}},
		"too big":   {Value: big},
	} {
		if _, err := (Meta{}).Apply(Patch{Props: map[string]*Prop{"p": p}}, now); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: got %v", name, err)
		}
	}
	if _, err := (Meta{}).Apply(Patch{Props: map[string]*Prop{"bad name!": {Value: 1}}}, now); !errors.Is(err, ErrInvalid) {
		t.Errorf("bad name: %v", err)
	}
}

func TestPublicAddr(t *testing.T) {
	for addr, want := range map[string]bool{
		"8.8.8.8": true, "2606:4700::1": true,
		"127.0.0.1": false, "10.0.0.1": false, "172.16.0.1": false, "192.168.1.1": false,
		"169.254.169.254": false, "100.64.0.1": false, "::1": false, "fe80::1": false, "fd00::1": false,
		"0.0.0.0": false, "224.0.0.1": false, "::ffff:127.0.0.1": false, "::ffff:169.254.169.254": false,
	} {
		if got := publicAddr(netip.MustParseAddr(addr)); got != want {
			t.Errorf("%s: got %v", addr, got)
		}
	}
}

// The server must not be usable to read its own network through a link in a ticket.
func TestFetcherRefusesPrivateAddresses(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("secret")) }))
	defer srv.Close()
	_, _, err := NewFetcher(false).Download(context.Background(), Resource{Kind: "http", URL: srv.URL})
	if !errors.Is(err, ErrBlockedAddress) {
		t.Fatalf("got %v", err)
	}
	body, _, err := NewFetcher(true).Download(context.Background(), Resource{Kind: "http", URL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	body.Close()
}

// A public-looking URL that redirects inward is stopped the same way.
func TestFetcherChecksRedirectTargets(t *testing.T) {
	inner := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("secret")) }))
	defer inner.Close()
	f := NewFetcher(true)
	outer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, inner.URL, http.StatusFound) }))
	defer outer.Close()
	body, _, err := f.Download(context.Background(), Resource{Kind: "http", URL: outer.URL})
	if err != nil {
		t.Fatal(err)
	}
	body.Close()
	if _, _, err := NewFetcher(false).Download(context.Background(), Resource{Kind: "http", URL: outer.URL}); !errors.Is(err, ErrBlockedAddress) {
		t.Fatalf("got %v", err)
	}
}
