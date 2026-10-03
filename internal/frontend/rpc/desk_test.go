package rpc

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/fclaude/nodwyn/internal/config"
	"github.com/fclaude/nodwyn/internal/event"
	"github.com/fclaude/nodwyn/internal/log"
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
	if _, err := os.Stat(filepath.Join(repo, ".nodwyn")); err == nil {
		t.Fatal("notes must not be written into the repository")
	}
}

func TestModelsStatusProbesWithoutCredentials(t *testing.T) {
	var sawAuth atomic.Bool // the probes run in parallel
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			sawAuth.Store(true)
		}
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
	if states["control"] != "ready" || states["other"] != "missing" || states["gone"] != "unreachable" || sawAuth.Load() {
		t.Fatalf("status: %+v (auth sent: %v)", status, sawAuth.Load())
	}
}

func TestViaNamesTheScreenAfterTheKernelIdentity(t *testing.T) {
	o := options(t)
	path, _ := start(t, o)
	c := connect(t, path)
	for _, bad := range []string{"phone", "web:Phone", "web:a b", "web:" + strings.Repeat("x", 40), "x:y via z"} {
		if r := c.call("hello", map[string]any{"protocol": 1, "via": bad}); r.Error == nil {
			t.Fatalf("accepted via %q", bad)
		}
	}
	var hello helloResult
	json.Unmarshal(c.ok("hello", map[string]any{"protocol": 1, "via": "web:phone"}), &hello)
	if !strings.HasPrefix(hello.Peer, "unix:uid=") || !strings.HasSuffix(hello.Peer, " via web:phone") {
		t.Fatalf("peer: %q", hello.Peer)
	}
	if r := c.call("hello", map[string]any{"protocol": 1, "via": "web:laptop"}); r.Error == nil {
		t.Fatal("changed via after hello")
	}
	repo := t.TempDir()
	id := opened(t, c, repo)
	subscribed(t, c, id, 0, 100)
	eval(c, id, `(fs-write "a.txt" "a")`)
	p := prompt[event.Confirm](t, c, event.PromptConfirm)
	c.ok("session.answer", map[string]any{"id": id, "prompt_id": p.ID, "reply": "y"})
	finishEval(t, c)
	found := false
	for _, e := range page(t, c, id, 0, 0, 1000).Events {
		if e.Kind == "guard.decision" && strings.Contains(string(e.Payload), p.ID) {
			found = strings.Contains(string(e.Payload), hello.Peer)
		}
	}
	if !found {
		t.Fatal("decision does not name the screen")
	}
}

// A person names, archives and deletes tasks (ADR 39). An open task records
// its name where a console showing it hears of it; it is neither archived
// nor deleted until it is closed.
func TestTasksAreNamedArchivedAndDeleted(t *testing.T) {
	o := options(t)
	path, _ := start(t, o)
	c := connect(t, path)
	c.hello()
	id := opened(t, c, t.TempDir())
	subscribed(t, c, id, 0, 100)
	listed := func() sessionInfo {
		t.Helper()
		var list []sessionInfo
		if err := json.Unmarshal(c.ok("session.list", map[string]any{}), &list); err != nil {
			t.Fatal(err)
		}
		for _, s := range list {
			if s.ID == id {
				return s
			}
		}
		return sessionInfo{}
	}
	c.ok("session.rename", map[string]any{"id": id, "title": "  Flaky test,\n second try "})
	named := c.until(string(log.KindSessionTitle))
	if p := decodePayload[log.SessionTitle](t, named[len(named)-1].Payload); p.Title != "Flaky test, second try" || p.Actor == "" {
		t.Fatalf("recorded name: %+v", p)
	}
	if got := listed(); got.Title != "Flaky test, second try" || got.Archived {
		t.Fatalf("listed: %+v", got)
	}
	if r := c.call("session.rename", map[string]any{"id": id, "title": strings.Repeat("x", maxName+1)}); r.Error == nil || r.Error.Code != -32602 {
		t.Fatalf("long name: %+v", r.Error)
	}
	if r := c.call("session.archive", map[string]any{"id": id, "archived": true}); r.Error == nil {
		t.Fatal("archived an open task")
	}
	if r := c.call("session.delete", map[string]any{"id": id}); r.Error == nil {
		t.Fatal("deleted an open task")
	}
	c.ok("session.close", map[string]any{"id": id})
	c.ok("session.archive", map[string]any{"id": id, "archived": true})
	c.ok("session.rename", map[string]any{"id": id, "title": "Put away"})
	if got := listed(); got.Title != "Put away" || !got.Archived {
		t.Fatalf("closed, archived and renamed: %+v", got)
	}
	c.ok("session.delete", map[string]any{"id": id})
	if _, err := os.Stat(log.SessionPath(o.Paths.SessionsDir, id)); !os.IsNotExist(err) {
		t.Fatalf("log after delete: %v", err)
	}
	if got := listed(); got.ID != "" {
		t.Fatalf("deleted task listed: %+v", got)
	}
	if r := c.call("session.rename", map[string]any{"id": id, "title": "Gone"}); r.Error == nil {
		t.Fatal("renamed a deleted task")
	}
}
