package core

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fclaude/nodwyn/internal/llm"
	"github.com/fclaude/nodwyn/internal/log"
	"github.com/fclaude/nodwyn/internal/scheme"
	fake "github.com/fclaude/nodwyn/test/fakes/ollama"
)

// diskSession starts a file-backed session over o with the given init file
// (none if empty) and scripted model turns. Confirms are answered by drain.
func diskSession(t *testing.T, o Options, init string, turns ...fake.Turn) *rig {
	t.Helper()
	if init != "" {
		os.MkdirAll(filepath.Join(o.Repo, ".nodwyn"), 0o755)
		os.WriteFile(filepath.Join(o.Repo, ".nodwyn", "init.scm"), []byte(init), 0o644)
	}
	srv := fake.NewT(t, turns...)
	t.Cleanup(srv.Close)
	o.Provider = llm.NewOllama(srv.URL)
	s, err := newInitSession(o)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return &rig{t: t, s: s, srv: srv, repo: o.Repo}
}

// past runs one finished session in o's repository.
func past(t *testing.T, o Options, request, answer string) string {
	t.Helper()
	r := diskSession(t, o, "", fake.Turn{Content: answer})
	if err := r.s.Send(request); err != nil {
		t.Fatal(err)
	}
	r.drain("n")
	id := r.s.ID()
	r.s.Close()
	return id
}

func evalValue(t *testing.T, r *rig, src string) (scheme.Value, error) {
	t.Helper()
	return r.s.Eval(context.Background(), src)
}

func TestSessionHistoryIsThisRepositorys(t *testing.T) {
	t.Parallel()
	o := diskOptions(t)
	mine := past(t, o, "first task", "first answer")
	other := o
	other.Repo = t.TempDir()
	theirs := past(t, other, "other repository's task", "elsewhere")

	r := diskSession(t, o, "")
	if got := r.eval(`(map (lambda (s) (assoc-ref s 'first-message)) (filter (lambda (s) (not (assoc-ref s 'current))) (session-list)))`); got != `("first task")` {
		t.Fatalf("session-list: %s", got)
	}
	if got := r.eval(`(length (session-list 1))`); got != "1" {
		t.Fatalf("session-list 1: %s", got)
	}
	got := r.eval(`(map (lambda (e) (assoc-ref (assoc-ref e 'payload) "text")) (session-events "` + mine + `" '("user.msg")))`)
	if got != `("first task")` {
		t.Fatalf("session-events: %s", got)
	}
	if _, err := evalValue(t, r, `(session-events "`+theirs+`")`); err == nil || !strings.Contains(err.Error(), "another repository") {
		t.Fatalf("another repository's session was readable: %v", err)
	}
}

func TestReloadAppliesWhatTheSessionAllows(t *testing.T) {
	t.Parallel()
	o := diskOptions(t)
	r := diskSession(t, o, `
(register-tool "greet" (lambda (who) "hi") '((doc . "Greet someone") (params . (("who" "string" "whom")))))
(define marker "v1")`)
	tools := len(r.s.loop.Tools())
	os.WriteFile(filepath.Join(o.Repo, ".nodwyn", "init.scm"), []byte(`
(register-tool "greet" (lambda (who) "hello") '((doc . "Greet someone") (params . (("who" "string" "whom")))))
(register-tool "extra" (lambda () "x") '((doc . "Something new") (params . ())))
(define marker "v2")
(define (system-prompt repo) "A different system prompt.")`), 0o644)

	notes := r.eval(`(reload)`)
	if !strings.Contains(notes, "tool extra is new") || !strings.Contains(notes, "system-prompt changed") {
		t.Fatalf("reload notes: %s", notes)
	}
	if got := r.eval(`marker`); got != `"v2"` {
		t.Fatalf("marker after reload: %s", got)
	}
	if got := r.eval(`(greet "you")`); got != `"hello"` {
		t.Fatalf("greet kept its old body: %s", got)
	}
	if len(r.s.loop.Tools()) != tools {
		t.Fatal("reload changed the tool list offered to the model")
	}
	if !strings.HasPrefix(r.s.loop.WindowContext().System, "You are nodwyn") {
		t.Fatal("reload changed the running system prompt")
	}

	os.WriteFile(filepath.Join(o.Repo, ".nodwyn", "init.scm"), []byte(`(define marker`), 0o644)
	if _, err := evalValue(t, r, `(reload)`); err == nil || !strings.Contains(err.Error(), "reload") {
		t.Fatalf("a broken init file reloaded: %v", err)
	}
}

func TestOptimizeImprovesInitFromHistory(t *testing.T) {
	t.Parallel()
	o := diskOptions(t)
	o.NoConfirm = false
	past(t, o, "run the tests", "I ran pytest with the system python; the imports failed.")
	write := map[string]any{"path": ".nodwyn/init.scm", "content": "(define marker \"tests use the venv\")\n"}
	r := diskSession(t, o, `(define marker "old")`,
		fake.Turn{ToolCalls: []fake.ToolCall{{ID: "r", Name: "fs-read", Args: map[string]any{"path": ".nodwyn/init.scm"}}}},
		fake.Turn{ToolCalls: []fake.ToolCall{
			{ID: "w", Name: "fs-write", Args: write},
			{ID: "x", Name: "fs-write", Args: map[string]any{"path": "main.go", "content": "package main\n"}},
		}},
		fake.Turn{Content: "Pattern: tests ran with the wrong interpreter (1 session). Changed: marker."},
		fake.Turn{Content: "The optimizer changed how tests run."},
	)
	if err := r.s.Send("/optimize 5 test runs"); err != nil {
		t.Fatal(err)
	}
	r.drain("y")
	logged, err := os.ReadFile(log.SessionPath(o.Paths.SessionsDir, r.s.ID()))
	if err != nil {
		t.Fatal(err)
	}
	r.logb = bytes.NewBuffer(logged)

	if got := r.eval(`marker`); got != `"tests use the venv"` {
		t.Fatalf("the optimized init file was not reloaded: %s", got)
	}
	optimizer := requestMessages(t, r.srv.Requests[0])
	if !strings.HasPrefix(optimizer[0].Content, "You improve how nodwyn") ||
		!strings.HasPrefix(optimizer[1].Content, "Focus on: test runs") ||
		!strings.Contains(optimizer[1].Content, "USER: run the tests") ||
		!strings.Contains(optimizer[1].Content, "ANSWER: I ran pytest with the system python") {
		t.Fatalf("the optimizer's context: %+v", optimizer)
	}
	if !strings.Contains(r.results()["x"], "not available to this agent") {
		t.Fatalf("the optimizer wrote outside .nodwyn/: %q", r.results()["x"])
	}
	final := requestMessages(t, r.srv.Requests[3])
	last := final[len(final)-1].Content
	if !strings.Contains(last, "The optimizer read 1 sessions") || !strings.Contains(last, "changed and reloaded") {
		t.Fatalf("the model's report request: %s", last)
	}
	if strings.Contains(string(r.srv.Requests[3].Raw), "USER: run the tests") {
		t.Fatal("the optimizer's history leaked into the conversation")
	}
}

func TestOptimizeKeepsAnEditPendingTrustReview(t *testing.T) {
	o := diskOptions(t)
	o.NoConfirm = false
	past(t, o, "test the project", "The tests need the project environment.")
	r := diskSession(t, o, `(define marker "old")`,
		fake.Turn{ToolCalls: []fake.ToolCall{{ID: "w", Name: "fs-write", Args: map[string]any{"path": ".nodwyn/init.scm", "content": `(define marker "review me")`}}}},
		fake.Turn{Content: "Edited the strategy."}, fake.Turn{Content: "Review the proposed change."})
	// This time the edit belongs to a repository, not an init file explicitly
	// authorised by the Go host. Reload must retain the edit without running it.
	r.s.opts.InitFile = ""
	if err := r.s.Send("/optimize"); err != nil {
		t.Fatal(err)
	}
	r.drain("y")
	if got := r.eval("marker"); got != `"old"` {
		t.Fatalf("unreviewed code ran: %s", got)
	}
	raw, err := os.ReadFile(filepath.Join(o.Repo, ".nodwyn", "init.scm"))
	if err != nil || string(raw) != `(define marker "review me")` {
		t.Fatalf("proposal was lost: %q, %v", raw, err)
	}
	if len(r.srv.Requests) != 3 || !strings.Contains(string(r.srv.Requests[2].Raw), "kept for review") {
		t.Fatal("the report did not explain pending trust")
	}
}
