package rpc

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fclaude/nibb/internal/config"
	"github.com/fclaude/nibb/internal/event"
)

func TestAttentionListCarriesPendingPromptsAndListCarriesChanges(t *testing.T) {
	o := options(t)
	path, _ := start(t, o)
	c := connect(t, path)
	c.hello()
	repo := t.TempDir()
	id := opened(t, c, repo)
	subscribed(t, c, id, 0, 100)

	var none []attentionItem
	json.Unmarshal(c.ok("attention.list", map[string]any{}), &none)
	if len(none) != 0 {
		t.Fatalf("idle session listed: %+v", none)
	}
	eval(c, id, `(fs-write "notes.txt" "one")`)
	p := prompt[event.Confirm](t, c, event.PromptConfirm)

	var items []attentionItem
	json.Unmarshal(c.ok("attention.list", map[string]any{}), &items)
	if len(items) != 1 || items[0].ID != id || len(items[0].Pending) != 1 || items[0].Pending[0].Kind != event.PromptConfirm {
		t.Fatalf("attention: %+v", items)
	}
	confirm := decodePayload[event.Confirm](t, items[0].Pending[0].Payload)
	if confirm.ID != p.ID || confirm.Prompt.Path != "notes.txt" || confirm.Prompt.Detail == "" {
		t.Fatalf("pending descriptor: %+v", confirm)
	}
	c.ok("session.answer", map[string]any{"id": id, "prompt_id": p.ID, "reply": "y"})
	if r := finishEval(t, c); r.Error != "" {
		t.Fatal(r.Error)
	}
	// A denied write is not a change.
	eval(c, id, `(fs-write "denied.txt" "no")`)
	p = prompt[event.Confirm](t, c, event.PromptConfirm)
	c.ok("session.answer", map[string]any{"id": id, "prompt_id": p.ID, "reply": "n"})
	finishEval(t, c)

	var list []sessionInfo
	json.Unmarshal(c.ok("session.list", map[string]any{}), &list)
	if len(list) != 1 || strings.Join(list[0].Changes, ",") != "notes.txt" || list[0].Started.IsZero() {
		t.Fatalf("list entry: %+v", list)
	}
}

func TestNotesReadAndGuardedWrite(t *testing.T) {
	o := options(t)
	path, _ := start(t, o)
	c := connect(t, path)
	c.hello()
	repo := t.TempDir()
	id := opened(t, c, repo)
	subscribed(t, c, id, 0, 100)
	var notes notesResult
	json.Unmarshal(c.ok("notes.read", map[string]any{"repo": repo}), &notes)
	if notes.Text != "" {
		t.Fatalf("fresh notes: %q", notes.Text)
	}
	if r := c.call("notes.read", map[string]any{"repo": "relative"}); r.Error == nil {
		t.Fatal("relative repo accepted")
	}
	var started inspectionStarted
	json.Unmarshal(c.ok("session.notes.write", map[string]any{"id": id, "text": "Build with make build."}), &started)
	result := prompt[event.Policy](t, c, event.PolicyResult)
	if result.ID != started.OperationID || !result.Saved || result.Err != "" || result.Name != "notes" {
		t.Fatalf("notes result: %+v", result)
	}
	c.until(event.Status)
	json.Unmarshal(c.ok("notes.read", map[string]any{"repo": repo}), &notes)
	if notes.Text != "Build with make build." {
		t.Fatalf("notes after save: %q", notes.Text)
	}
	if _, err := os.Stat(filepath.Join(repo, ".nibb")); err == nil {
		t.Fatal("notes must not be written into the repository")
	}
}

func TestModelsStatusProbesWithoutCredentials(t *testing.T) {
	var sawAuth bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawAuth = sawAuth || r.Header.Get("Authorization") != ""
		if r.URL.Path == "/api/tags" {
			w.Write([]byte(`{"models":[{"name":"qwen:latest"}]}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()
	o := options(t)
	o.Config.Control = config.Model{URL: server.URL, Model: "qwen"}
	o.Config.Models = map[string]config.Model{"other": {URL: server.URL, Model: "absent"}, "gone": {URL: "http://127.0.0.1:1", Model: "x"}}
	path, _ := start(t, o)
	c := connect(t, path)
	c.hello()
	var status []modelStatus
	json.Unmarshal(c.ok("models.status", map[string]any{}), &status)
	states := map[string]string{}
	for _, s := range status {
		states[s.Name] = s.State
	}
	if states["control"] != "ready" || states["other"] != "missing" || states["gone"] != "unreachable" || sawAuth {
		t.Fatalf("status: %+v (auth sent: %v)", status, sawAuth)
	}
}
