package rpc

import (
	"encoding/json"
	"github.com/fclaude/nodwyn/internal/event"
	"os"
	"path/filepath"
	"testing"
)

func TestFrictionInspectionIsAsyncAndFixedPath(t *testing.T) {
	path, _ := start(t, options(t))
	c := connect(t, path)
	c.hello()
	repo := t.TempDir()
	os.MkdirAll(filepath.Join(repo, "docs"), 0700)
	os.WriteFile(filepath.Join(repo, "docs", "friction.md"), []byte("RPC friction fixture"), 0600)
	id := opened(t, c, repo)
	subscribed(t, c, id, 0, 100)
	if r := c.call("session.friction", map[string]any{"id": id, "path": "other.txt"}); r.Error == nil {
		t.Fatal("accepted arbitrary path")
	}
	var started inspectionStarted
	if err := json.Unmarshal(c.ok("session.friction", map[string]any{"id": id}), &started); err != nil {
		t.Fatal(err)
	}
	result := prompt[event.Inspection](t, c, event.InspectionResult)
	if result.ID != started.OperationID || result.Text != "RPC friction fixture" || result.Err != "" {
		t.Fatalf("unexpected result: %+v", result)
	}
}
