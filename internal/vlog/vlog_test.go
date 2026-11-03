package vlog

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

const fakeToken = "eyJhZ2VudCI6ImEiLCJ0aWVyIjoxfQ.c2lnbmF0dXJlLXRoYXQtaXMtbG9uZy1lbm91Z2g"

func capture(t *testing.T, level int, vmodule, format string) *bytes.Buffer {
	t.Helper()
	var b bytes.Buffer
	if err := Setup(Options{Level: level, VModule: vmodule, Format: format, Output: &b}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		tempMu.Lock()
		if tempTimer != nil {
			tempTimer.Stop()
			tempTimer = nil
		}
		tempMu.Unlock()
		_ = Setup(Options{Level: 0})
	})
	return &b
}

func TestLevelsGate(t *testing.T) {
	b := capture(t, 2, "", "text")
	l := For("engine")
	l.V(2).Info("shown")
	l.V(3).Info("hidden")
	l.V(0).Info("always")
	l.Error("failure")
	out := b.String()
	for _, want := range []string{"shown", "always", "failure", "component=engine", "v=2"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in %s", want, out)
		}
	}
	if strings.Contains(out, "hidden") {
		t.Error("a level above the setting was written")
	}
}

func TestErrorsShowAtLevelZero(t *testing.T) {
	b := capture(t, 0, "", "text")
	For("x").Error("bad")
	if !strings.Contains(b.String(), "bad") {
		t.Fatal("an error was hidden")
	}
}

func TestVModuleRaisesOneComponent(t *testing.T) {
	b := capture(t, 1, "engine=5,dr*=3", "text")
	For("engine").V(5).Info("engine-deep")
	For("driver").V(3).Info("driver-mid")
	For("driver").V(4).Info("driver-deep")
	For("work").V(2).Info("work-other")
	out := b.String()
	if !strings.Contains(out, "engine-deep") || !strings.Contains(out, "driver-mid") {
		t.Errorf("raised components: %s", out)
	}
	if strings.Contains(out, "driver-deep") || strings.Contains(out, "work-other") {
		t.Errorf("too much: %s", out)
	}
	// It can also lower one component below the global level.
	b2 := capture(t, 5, "noisy=0", "text")
	For("noisy").V(1).Info("quieted")
	For("other").V(5).Info("kept")
	if strings.Contains(b2.String(), "quieted") || !strings.Contains(b2.String(), "kept") {
		t.Errorf("lowering: %s", b2.String())
	}
}

func TestBadSettingsAreRefused(t *testing.T) {
	capture(t, 0, "", "text")
	for _, n := range []int{-1, 10} {
		if SetLevel(n) == nil {
			t.Errorf("level %d accepted", n)
		}
	}
	for _, s := range []string{"engine", "engine=", "engine=x", "engine=10", "=3", "[=3"} {
		if SetVModule(s) == nil {
			t.Errorf("vmodule %q accepted", s)
		}
	}
	if Setup(Options{Format: "xml"}) == nil {
		t.Error("format xml accepted")
	}
}

// Logging must be able to stay in hot paths.
func TestDisabledCallAllocatesNothing(t *testing.T) {
	capture(t, 0, "", "text")
	l := For("engine")
	if n := testing.AllocsPerRun(1000, func() {
		if v := l.V(5); v.Enabled() {
			v.Info("x")
		}
	}); n != 0 {
		t.Fatalf("%v allocations", n)
	}
}

func BenchmarkDisabled(b *testing.B) {
	_ = Setup(Options{Level: 0})
	l := For("engine")
	for b.Loop() {
		if v := l.V(5); v.Enabled() {
			v.Info("x")
		}
	}
}

// The promise that matters most: nothing logged can carry a credential.
func TestCredentialsNeverReachTheLog(t *testing.T) {
	for _, format := range []string{"text", "json"} {
		b := capture(t, 9, "", format)
		l := For("x").V(1)
		l.Info("by name", "token", "plainvalue-1", "api_key", "plainvalue-2", "Authorization", "plainvalue-3", "password", "plainvalue-4", "SWARM_TOKEN", "plainvalue-5")
		l.Info("by shape", "anything", "here is "+fakeToken+" inside text")
		l.Info("in the message "+fakeToken, "note", "Bearer abcdef123456")
		l.Info("as an error", "err", errors.New("failed with "+fakeToken))
		l.Info("as a stringer", "val", []string{fakeToken})
		For("y").Error("error line", "err", errors.New("auth: Bearer sekret-value-1"))
		out := b.String()
		for _, bad := range []string{"plainvalue", "c2lnbmF0dXJl", "abcdef123456", "sekret-value"} {
			if strings.Contains(out, bad) {
				t.Errorf("%s: %q leaked:\n%s", format, bad, out)
			}
		}
	}
}

func TestMessageTextIsScrubbedToo(t *testing.T) {
	b := capture(t, 1, "", "text")
	For("x").V(1).Info("message " + fakeToken)
	if strings.Contains(b.String(), "c2lnbmF0dXJl") {
		t.Fatalf("a token in the message leaked: %s", b.String())
	}
}

func TestTokenCountsAreNotRedacted(t *testing.T) {
	b := capture(t, 1, "", "text")
	For("x").V(1).Info("usage", "tokens", 1234)
	if !strings.Contains(b.String(), "tokens=1234") {
		t.Fatalf("a count was hidden: %s", b.String())
	}
	got := Body([]byte(`{"tokens":55,"token":"secretvalue","nested":{"api_key":"k1","title":"keep me"},"list":[{"password":"p1"}]}`), 1000)
	for _, bad := range []string{"secretvalue", "k1", "p1"} {
		if strings.Contains(got, bad) {
			t.Errorf("%s leaked: %s", bad, got)
		}
	}
	if !strings.Contains(got, `"tokens":55`) || !strings.Contains(got, "keep me") {
		t.Errorf("real data was lost: %s", got)
	}
}

func TestHeadersHideCredentials(t *testing.T) {
	h := http.Header{"Authorization": {"Bearer " + fakeToken}, "Cookie": {"a=b"}, "Accept": {"application/json"}, "X-Api-Key": {"k"}}
	got := Headers(h)
	if strings.Contains(got, "c2lnbmF0dXJl") || strings.Contains(got, "a=b") || strings.Contains(got, ": k") {
		t.Fatalf("leak: %s", got)
	}
	if !strings.Contains(got, "Accept: application/json") {
		t.Fatalf("lost: %s", got)
	}
}

func TestBodyLimits(t *testing.T) {
	long := bytes.Repeat([]byte("x"), 5000)
	if got := Body(long, truncatedBody); !strings.Contains(got, "[truncated 3976 bytes]") {
		t.Fatalf("truncate: %.60s", got)
	}
	if got := Body(long, fullBody); len(got) != 5000 {
		t.Fatalf("full: %d", len(got))
	}
	if Body(long, 0) != "" || Body(nil, 100) != "" {
		t.Fatal("no limit or no body should give nothing")
	}
	if got := Body([]byte("not json Bearer abc123def"), 100); strings.Contains(got, "abc123def") {
		t.Fatalf("plain text leak: %s", got)
	}
}

func TestHTTPLimitFollowsTheLevel(t *testing.T) {
	capture(t, 7, "", "text")
	if HTTPLimit("http") != 0 {
		t.Error("level 7 shows no bodies")
	}
	_ = SetLevel(8)
	if HTTPLimit("http") != truncatedBody {
		t.Error("level 8 truncates")
	}
	_ = SetLevel(9)
	if HTTPLimit("http") != fullBody {
		t.Error("level 9 keeps it all")
	}
}

func TestTemporaryChangeRestoresItself(t *testing.T) {
	capture(t, 2, "a=1", "text")
	if err := SetFor(9, "", 60*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if Level() != 9 || RevertsAt().IsZero() {
		t.Fatalf("level %d", Level())
	}
	// A second temporary change must still return to the original.
	if err := SetFor(5, "b=2", 60*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	time.Sleep(150 * time.Millisecond)
	if Level() != 2 || VModule() != "a=1" || !RevertsAt().IsZero() {
		t.Fatalf("after revert: level %d vmodule %q", Level(), VModule())
	}
	if err := SetFor(4, "", 0); err != nil {
		t.Fatal(err)
	}
	if Level() != 4 || !RevertsAt().IsZero() {
		t.Fatal("permanent change")
	}
	if SetFor(10, "", 0) == nil || SetFor(1, "bad", 0) == nil {
		t.Fatal("bad values accepted")
	}
	if Level() != 4 {
		t.Fatal("a refused change altered the level")
	}
}

func TestJSONFormatIsValidAndStructured(t *testing.T) {
	b := capture(t, 3, "", "json")
	For("engine").V(3).Info("hello", "task", "t1")
	var line map[string]any
	if err := json.Unmarshal(b.Bytes(), &line); err != nil {
		t.Fatalf("%v: %s", err, b.String())
	}
	if line["component"] != "engine" || line["task"] != "t1" || line["v"] != float64(3) || line["msg"] != "hello" {
		t.Fatalf("line: %v", line)
	}
}

func TestCaptureStreamsAndLimits(t *testing.T) {
	rec := httptest.NewRecorder()
	c := NewCapture(rec, 4)
	c.WriteHeader(201)
	_, _ = c.Write([]byte("hello world"))
	c.Flush()
	if rec.Code != 201 || rec.Body.String() != "hello world" || !rec.Flushed {
		t.Fatalf("passthrough: %d %q", rec.Code, rec.Body.String())
	}
	if string(c.Recorded()) != "hell" || c.Bytes != 11 || c.Status != 201 {
		t.Fatalf("recorded: %q %d %d", c.Recorded(), c.Bytes, c.Status)
	}
	if http.NewResponseController(c) == nil {
		t.Fatal("controller")
	}
}

func TestConcurrentChanges(t *testing.T) {
	capture(t, 1, "", "text")
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Go(func() {
			for j := range 200 {
				_ = SetLevel((i + j) % 10)
				_ = SetVModule("a=1")
				For("a").V(j % 10).Info("x")
				_ = VModule()
			}
		})
	}
	wg.Wait()
}
