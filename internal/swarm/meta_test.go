package swarm

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/Grano22/aiculator/internal/engine"
	"github.com/Grano22/aiculator/internal/meta"
	"github.com/Grano22/aiculator/internal/store"
)

var bg = context.Background()

func service(t *testing.T) *Service {
	t.Helper()
	st, err := store.Open(bg, store.Options{Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(bg, Options{Store: st})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close(); st.Close() })
	if _, err := s.Engine.AddTask(bg, engine.TaskSpec{ID: "t", Title: "x"}); err != nil {
		t.Fatal(err)
	}
	return s
}

type countingDownloader struct{ calls atomic.Int64 }

func (d *countingDownloader) Download(context.Context, meta.Resource) (io.ReadCloser, string, error) {
	d.calls.Add(1)
	return io.NopCloser(strings.NewReader("attachment bytes")), "text/plain", nil
}

func TestResourceIsFetchedOnce(t *testing.T) {
	s := service(t)
	d := &countingDownloader{}
	s.Connections = func(name string) meta.Downloader {
		if name == "jira" {
			return d
		}
		return nil
	}
	p := meta.Patch{Props: map[string]*meta.Prop{"log": {Resource: &meta.Resource{Kind: "jira.attachment", URL: "https://x.example/a", Name: "log", Connection: "jira"}}}}
	if _, err := s.PatchMeta(bg, "t", p); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		ref, err := s.FetchResource(bg, "t", "log")
		if err != nil {
			t.Fatal(err)
		}
		if ref.SHA256 != store.HashHex([]byte("attachment bytes")) || ref.MediaType != "text/plain" {
			t.Fatalf("%+v", ref)
		}
	}
	if n := d.calls.Load(); n != 1 {
		t.Fatalf("downloaded %d times", n)
	}
	m, err := s.GetMeta(bg, "t")
	if err != nil {
		t.Fatal(err)
	}
	if r := m.Props["log"].Resource; r.Blob == nil || r.FetchedAt.IsZero() {
		t.Fatalf("the copy was not recorded: %+v", r)
	}
	body, size, err := s.OpenBlob(bg, m.Props["log"].Resource.Blob.SHA256)
	if err != nil {
		t.Fatal(err)
	}
	defer body.Close()
	b, _ := io.ReadAll(body)
	if string(b) != "attachment bytes" || size != int64(len(b)) {
		t.Fatalf("%q %d", b, size)
	}
}

func TestBlobLimitsAndNames(t *testing.T) {
	s := service(t)
	if _, err := s.Store.PutBlob(bg, strings.NewReader("too long"), "x", "", 4); !errors.Is(err, store.ErrTooLarge) {
		t.Fatalf("got %v", err)
	}
	a, err := s.PutBlob(bg, strings.NewReader("same"), "a", "")
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.PutBlob(bg, strings.NewReader("same"), "b", "")
	if err != nil {
		t.Fatal(err)
	}
	if a.SHA256 != b.SHA256 {
		t.Fatal("the same bytes got two names")
	}
	for _, bad := range []string{"../../etc/passwd", "ABC", strings.Repeat("z", 64)} {
		if _, _, err := s.OpenBlob(bg, bad); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("%s: %v", bad, err)
		}
	}
	if _, err := s.GetMeta(bg, "missing"); !errors.Is(err, engine.ErrNotFound) {
		t.Errorf("unknown task: %v", err)
	}
}

func TestConcurrentPatchesKeepBothChanges(t *testing.T) {
	s := service(t)
	var wg sync.WaitGroup
	for i := range 16 {
		wg.Go(func() {
			k := string(rune('a' + i))
			if _, err := s.PatchMeta(bg, "t", meta.Patch{Props: map[string]*meta.Prop{k: {Value: i + 1}}}); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	m, err := s.GetMeta(bg, "t")
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Props) != 16 {
		t.Fatalf("%d of 16 changes survived", len(m.Props))
	}
}

// Attaching a lot to a task must not change what a commit costs.
func TestMetadataStaysOutOfTheTaskRecord(t *testing.T) {
	s := service(t)
	big := strings.Repeat("x", 200<<10)
	if _, err := s.PatchMeta(bg, "t", meta.Patch{Props: map[string]*meta.Prop{"big": {Value: big}}}); err != nil {
		t.Fatal(err)
	}
	x, _, err := s.Store.GetTask("t")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(x.Spec, "xxx") || len(x.Labels) != 0 {
		t.Fatalf("%+v", x)
	}
}
