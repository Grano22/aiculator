package api_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Grano22/aiculator/internal/auth"
	"github.com/Grano22/aiculator/internal/engine"
	"github.com/Grano22/aiculator/internal/meta"
)

func (f *fixture) call(t *testing.T, claims auth.Claims, method, path, body string) (int, string, http.Header) {
	t.Helper()
	tok, err := f.auth.Issue(claims)
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(method, f.url+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b), resp.Header
}

func TestAgentsCanReadMetadataButNotWriteIt(t *testing.T) {
	f := setup(t)
	if _, err := f.svc.Engine.AddTask(context.Background(), engine.TaskSpec{ID: "t", Title: "x"}); err != nil {
		t.Fatal(err)
	}
	admin := auth.Claims{Agent: "admin", Admin: true}
	agent := auth.Claims{Agent: "a1", Tier: 1}
	patch := `{"props":{"k":{"value":1}}}`
	if code, body, _ := f.call(t, admin, "POST", "/v1/tasks/t/meta", patch); code != 200 {
		t.Fatalf("admin write: %d %s", code, body)
	}
	if code, _, _ := f.call(t, agent, "POST", "/v1/tasks/t/meta", patch); code != 403 {
		t.Errorf("an agent wrote metadata: %d", code)
	}
	if code, _, _ := f.call(t, agent, "POST", "/v1/blobs", "x"); code != 403 {
		t.Errorf("an agent stored a blob: %d", code)
	}
	if code, body, _ := f.call(t, agent, "GET", "/v1/tasks/t/meta", ""); code != 200 || !strings.Contains(body, `"k"`) {
		t.Errorf("agent read: %d %s", code, body)
	}
	if code, _, _ := f.call(t, admin, "POST", "/v1/tasks/t/meta", `{"props":{"k":{"resource":{"kind":"http","url":"file:///etc/passwd"}}}}`); code != 400 {
		t.Errorf("bad resource: %d", code)
	}
}

func TestBlobsAreNeverServedAsActiveContent(t *testing.T) {
	f := setup(t)
	admin := auth.Claims{Agent: "admin", Admin: true}
	code, body, _ := f.call(t, admin, "POST", "/v1/blobs?name=x.html", "<script>alert(1)</script>")
	if code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	var ref meta.BlobRef
	if err := json.Unmarshal([]byte(body), &ref); err != nil {
		t.Fatal(err)
	}
	code, got, h := f.call(t, auth.Claims{Agent: "a1", Tier: 1}, "GET", "/v1/blobs/"+ref.SHA256, "")
	if code != 200 || got != "<script>alert(1)</script>" {
		t.Fatalf("%d %s", code, got)
	}
	if h.Get("Content-Type") != "application/octet-stream" || h.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("%v", h)
	}
	if code, _, _ := f.call(t, admin, "GET", "/v1/blobs/not-a-hash", ""); code != 404 {
		t.Errorf("bad name: %d", code)
	}
}

func TestAgentReadsAttachmentsOverMCP(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	if _, err := f.svc.Engine.AddTask(ctx, engine.TaskSpec{ID: "t", Title: "x"}); err != nil {
		t.Fatal(err)
	}
	ref, err := f.svc.PutBlob(ctx, strings.NewReader("hello from a file"), "a.txt", "text/plain")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.PatchMeta(ctx, "t", meta.Patch{Props: map[string]*meta.Prop{"a.txt": {Blob: &ref}, "n": {Value: 5}}}); err != nil {
		t.Fatal(err)
	}
	s := f.session(t, "a1", 1)
	call := func(name string, args map[string]any) string {
		r, err := s.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
		if err != nil {
			t.Fatal(err)
		}
		b, _ := json.Marshal(r.StructuredContent)
		return string(b)
	}
	if got := call("task_meta", map[string]any{"task_id": "t"}); !strings.Contains(got, `"a.txt"`) {
		t.Errorf("task_meta: %s", got)
	}
	if got := call("fetch_resource", map[string]any{"task_id": "t", "name": "a.txt"}); !strings.Contains(got, "hello from a file") {
		t.Errorf("fetch_resource: %s", got)
	}
}
