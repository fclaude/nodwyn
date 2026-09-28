package core

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fclaude/nibb/internal/config"
	"github.com/fclaude/nibb/internal/event"
	"github.com/fclaude/nibb/internal/llm"
	"github.com/fclaude/nibb/internal/log"
	"github.com/fclaude/nibb/internal/scheme"
	fake "github.com/fclaude/nibb/test/fakes/ollama"
)

// strategyRig is a session whose .nibb/init.scm is init: the way a project
// replaces the agent loop.
func strategyRig(t *testing.T, init string, maxToolCalls int, turns ...fake.Turn) *rig {
	t.Helper()
	repo := t.TempDir()
	os.WriteFile(filepath.Join(repo, "main.go"), []byte("package main\n"), 0o644)
	os.MkdirAll(filepath.Join(repo, ".nibb"), 0o755)
	os.WriteFile(filepath.Join(repo, ".nibb", "init.scm"), []byte(init), 0o644)
	srv := fake.New(turns...)
	t.Cleanup(srv.Close)
	cfg := config.Default()
	cfg.Loop.MaxToolCalls = maxToolCalls
	logb := &bytes.Buffer{}
	s, err := New(Options{Config: cfg, Repo: repo, Version: "test", Provider: llm.NewOllama(srv.URL), NoConfirm: true, LogWriter: logb})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return &rig{t: t, s: s, srv: srv, repo: repo, logb: logb}
}

// turn sends text and returns the turn's error, "" if it ended cleanly.
func (r *rig) turn(text string) string {
	r.t.Helper()
	if err := r.s.Send(text); err != nil {
		r.t.Fatal(err)
	}
	for _, ev := range r.drain("n") {
		if ev.Kind == event.Error {
			return ev.Payload.(event.ErrorInfo).Err
		}
	}
	return ""
}

// results returns the error text of every tool.result in the log, by id.
func (r *rig) results() map[string]string {
	r.t.Helper()
	evs, err := log.Read(bytes.NewReader(r.logb.Bytes()))
	if err != nil {
		r.t.Fatal(err)
	}
	out := map[string]string{}
	for _, ev := range evs {
		if ev.Kind != log.KindToolResult {
			continue
		}
		var res log.ToolResult
		if err := ev.Decode(&res); err != nil {
			r.t.Fatal(err)
		}
		if _, dup := out[res.ID]; dup {
			r.t.Fatalf("call %s has two results", res.ID)
		}
		out[res.ID] = res.Error
	}
	return out
}

func (r *rig) eval(src string) string {
	r.t.Helper()
	v, err := r.s.Eval(context.Background(), src)
	if err != nil {
		r.t.Fatal(err)
	}
	return scheme.Write(v)
}

func readCall(id string) fake.ToolCall {
	return fake.ToolCall{ID: id, Name: "fs-read", Args: map[string]any{"path": "main.go"}}
}

func TestStrategyRedefinedInInit(t *testing.T) {
	r := strategyRig(t, `
(define asked #f)
(define (agent-turn request)
  (set! asked request)
  (let loop ((reply (model-turn)))
    (let ((calls (assoc-ref reply 'tool-calls '())))
      (unless (null? calls)
        (for-each (lambda (c)
                    (if (equal? (assoc-ref c 'name) "fs-list")
                        (skip-tools (list c) "listing is off here")
                        (run-tools (list c))))
                  calls)
        (loop (model-turn))))))`, 10,
		fake.Turn{ToolCalls: []fake.ToolCall{{ID: "l", Name: "fs-list", Args: map[string]any{"path": "."}}, readCall("r")}},
		fake.Turn{Content: "done"},
	)
	if err := r.turn("what is here?"); err != "" {
		t.Fatal(err)
	}
	if got := r.eval("asked"); got != `"what is here?"` {
		t.Fatalf("strategy got request %s", got)
	}
	raw := string(r.srv.Requests[1].Raw)
	if !strings.Contains(raw, "Not executed: listing is off here") || !strings.Contains(raw, `package main\n`) {
		t.Fatalf("second request:\n%s", raw)
	}
}

// A strategy that catches everything still cannot keep a turn going past a
// stop: the budgets and the progress guard are not the strategy's to waive.
func TestStrategyCannotCatchStops(t *testing.T) {
	const swallow = `
(define (agent-turn request)
  (let loop ((reply (model-turn)))
    (let ((calls (assoc-ref reply 'tool-calls '())))
      (unless (null? calls)
        (guard (e (else 'ignored)) (run-tools calls))
        (loop (model-turn))))))`
	same := fake.Turn{ToolCalls: []fake.ToolCall{{ID: "c", Name: "fs-list", Args: map[string]any{"path": "."}}}}
	for _, tc := range []struct {
		name         string
		maxToolCalls int
		want         string
	}{
		{"tool-call limit", 3, "hard stop"},
		{"progress guard", 10, "progress stop"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := strategyRig(t, swallow, tc.maxToolCalls, same, same, same, same, same, same)
			if err := r.turn("loop forever"); !strings.Contains(err, tc.want) {
				t.Fatalf("turn error %q, want %q", err, tc.want)
			}
			if n := len(r.srv.Requests); n != 4 {
				t.Fatalf("the model was asked %d times after the stop", n-4)
			}
		})
	}
}

func TestStrategyCannotFabricateCalls(t *testing.T) {
	r := strategyRig(t, `
(define (agent-turn request)
  (model-turn)
  (run-tools '(((id . "made-up") (name . "fs-write") (args . "{\"path\":\"x\",\"content\":\"y\"}")))))`, 10,
		fake.Turn{Content: "nothing to do"},
	)
	if err := r.turn("hi"); !strings.Contains(err, "not an unanswered call") {
		t.Fatalf("turn error %q", err)
	}
	if _, err := os.Stat(filepath.Join(r.repo, "x")); err == nil {
		t.Fatal("a call the model never made ran")
	}
	if strings.Contains(r.logb.String(), `"tool.call"`) {
		t.Fatal("a fabricated call was logged as the model's")
	}
}

// Refusing one call in a batch refuses the batch: the calls before it are
// not left claimed with no result.
func TestStrategyCannotRewriteArgs(t *testing.T) {
	r := strategyRig(t, `
(define (agent-turn request)
  (let* ((calls (assoc-ref (model-turn) 'tool-calls))
         (a (car calls))
         (b (cadr calls)))
    (run-tools (list a (list (assoc 'id b) (assoc 'name b) (cons 'args "{\"path\":\"elsewhere\"}"))))))`, 10,
		fake.Turn{ToolCalls: []fake.ToolCall{readCall("a"), readCall("b")}},
	)
	if err := r.turn("read twice"); !strings.Contains(err, "args differ") {
		t.Fatalf("turn error %q", err)
	}
	for id, res := range r.results() {
		if res != "Not executed: the turn ended before this call ran" {
			t.Fatalf("call %s: %q", id, res)
		}
	}
	if len(r.results()) != 2 {
		t.Fatalf("results: %v", r.results())
	}
}

// Every call gets exactly one result, whatever the strategy did with it, so
// the next request is well-formed.
func TestStrategyUnansweredCallsAreClosed(t *testing.T) {
	r := strategyRig(t, `
(define (agent-turn request)
  (model-turn)
  (model-turn))`, 10,
		fake.Turn{ToolCalls: []fake.ToolCall{readCall("first")}},
		fake.Turn{ToolCalls: []fake.ToolCall{readCall("second")}},
		fake.Turn{Content: "ok"},
		fake.Turn{Content: "still ok"},
	)
	if err := r.turn("go"); err != "" {
		t.Fatal(err)
	}
	got := r.results()
	if got["first"] != "Not executed: the strategy asked the model again before running this call" ||
		got["second"] != "Not executed: the turn ended before this call ran" {
		t.Fatalf("results: %v", got)
	}
	if err := r.turn("again"); err != "" {
		t.Fatal(err)
	}
	if !strings.Contains(string(r.srv.Requests[2].Raw), `"tool_call_id":"second"`) {
		t.Fatalf("third request lost the skipped call:\n%s", r.srv.Requests[2].Raw)
	}
}

func TestLoopPrimitivesAreNotForTools(t *testing.T) {
	r := strategyRig(t, `
(register-tool "again" (lambda () (model-turn))
  '((doc . "Ask the model from inside a tool") (params . ())))`, 10,
		fake.Turn{ToolCalls: []fake.ToolCall{{ID: "c", Name: "again", Args: map[string]any{}}}},
		fake.Turn{Content: "done"},
	)
	if err := r.turn("try it"); err != "" {
		t.Fatal(err)
	}
	if res := r.results()["c"]; !strings.Contains(res, "not callable from a model-originated evaluation") {
		t.Fatalf("tool result %q", res)
	}
	if _, err := r.s.Eval(context.Background(), "(model-turn)"); err == nil || !strings.Contains(err.Error(), "only available while a turn is running") {
		t.Fatalf("model-turn outside a turn: %v", err)
	}
}

// Tool results reach the strategy with the provenance of what the tool
// produced, and the strategy cannot wipe it.
func TestStrategyResultsKeepProvenance(t *testing.T) {
	r := strategyRig(t, `
(define seen '())
(define (agent-turn request)
  (let* ((results (run-tools (assoc-ref (model-turn) 'tool-calls)))
         (value (assoc-ref (car results) 'value)))
    (set! seen (taint-of value))
    (wipe value)))`, 10,
		fake.Turn{ToolCalls: []fake.ToolCall{readCall("r")}},
	)
	if err := r.turn("read"); !strings.Contains(err, "user-only") {
		t.Fatalf("turn error %q", err)
	}
	if got := r.eval("seen"); got != `("external:file")` {
		t.Fatalf("provenance seen by the strategy: %s", got)
	}
}

// The example in docs/manual.md, "Changing the loop".
func TestStrategyStopsBatchAtFirstFailure(t *testing.T) {
	r := strategyRig(t, `
(define (run-until-failure calls)
  (unless (null? calls)
    (let ((result (car (run-tools (list (car calls))))))
      (if (assoc-ref result 'ok)
          (run-until-failure (cdr calls))
          (skip-tools (cdr calls) "an earlier call in this batch failed")))))

(define (agent-turn request)
  (let loop ((reply (model-turn)))
    (let ((calls (assoc-ref reply 'tool-calls '())))
      (unless (null? calls)
        (run-until-failure calls)
        (loop (model-turn))))))`, 10,
		fake.Turn{ToolCalls: []fake.ToolCall{
			readCall("a"),
			{ID: "b", Name: "fs-read", Args: map[string]any{"path": "missing.go"}},
			readCall("c"),
		}},
		fake.Turn{Content: "done"},
	)
	if err := r.turn("read three"); err != "" {
		t.Fatal(err)
	}
	got := r.results()
	if got["a"] != "" || got["b"] == "" || got["c"] != "Not executed: an earlier call in this batch failed" {
		t.Fatalf("results: %v", got)
	}
}

// failAfterUserMsg is a log destination that breaks once the user's
// message is written, as a full disk would.
type failAfterUserMsg struct{ broken bool }

func (w *failAfterUserMsg) Write(p []byte) (int, error) {
	if w.broken {
		return 0, errors.New("disk full")
	}
	w.broken = bytes.Contains(p, []byte(`"user.msg"`))
	return len(p), nil
}

// A strategy cannot catch a log failure and carry on unrecorded.
func TestStrategyCannotCatchLogFailure(t *testing.T) {
	repo := t.TempDir()
	os.MkdirAll(filepath.Join(repo, ".nibb"), 0o755)
	os.WriteFile(filepath.Join(repo, ".nibb", "init.scm"), []byte(`
(define (agent-turn request)
  (let retry ((n 0))
    (guard (e (else (if (< n 100) (retry (+ n 1)) 'gave-up)))
      (model-turn))))`), 0o644)
	srv := fake.New(fake.Turn{Content: "never asked"})
	t.Cleanup(srv.Close)
	s, err := New(Options{Config: config.Default(), Repo: repo, Provider: llm.NewOllama(srv.URL), NoConfirm: true, LogWriter: &failAfterUserMsg{}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	r := &rig{t: t, s: s, srv: srv}
	if err := r.turn("hi"); !strings.Contains(err, "disk full") {
		t.Fatalf("turn error %q", err)
	}
	if len(srv.Requests) != 0 {
		t.Fatal("the model was asked although the request could not be recorded")
	}
}

// add-message is how a strategy speaks when the model has not called a
// tool: the model reads it as a user message, and the task stays the user's.
func TestAddMessageReachesTheModel(t *testing.T) {
	r := strategyRig(t, `
(define (agent-turn request)
  (model-turn)
  (add-message "Before you finish, list the files you changed.")
  (model-turn))`, 10,
		fake.Turn{Content: "Done."},
		fake.Turn{Content: "I changed nothing."},
	)
	if err := r.turn("tidy up"); err != "" {
		t.Fatal(err)
	}
	raw := string(r.srv.Requests[1].Raw)
	if !strings.Contains(raw, `"role":"user","content":"Before you finish, list the files you changed."`) {
		t.Fatalf("second request lacks the harness message:\n%s", raw)
	}
	if !strings.Contains(raw, `Current task (the user's latest request):\ntidy up"`) {
		t.Fatalf("the harness message replaced the task:\n%s", raw)
	}
	if !strings.Contains(r.logb.String(), `"kind":"harness.msg"`) {
		t.Fatal("the harness message was not logged")
	}
}

// A tool call must be followed by its result, so a strategy cannot speak
// between them.
func TestAddMessageRefusedWhileCallsAreUnanswered(t *testing.T) {
	r := strategyRig(t, `
(define (agent-turn request)
  (model-turn)
  (add-message "hello"))`, 10,
		fake.Turn{ToolCalls: []fake.ToolCall{readCall("r")}},
	)
	if err := r.turn("go"); !strings.Contains(err, "unanswered; run or skip them first") {
		t.Fatalf("turn error %q", err)
	}
	if strings.Contains(r.logb.String(), `"kind":"harness.msg"`) {
		t.Fatal("a harness message was logged between a call and its result")
	}
}

// The host calls nibb-turn, so a project's own turn, even one that is not a
// procedure, does not break every turn.
func TestAProjectMayDefineTurn(t *testing.T) {
	r := strategyRig(t, `(define turn 0)`, 10, fake.Turn{Content: "hello"})
	if err := r.turn("hi"); err != "" {
		t.Fatal(err)
	}
}

// '((tools #f)) is a list, not #f: it is an error, where it used to leave
// the tools on silently.
func TestModelTurnToolsOptionIsStrict(t *testing.T) {
	r := strategyRig(t, `(define (agent-turn request) (model-turn "control" '((tools #f))))`, 10, fake.Turn{Content: "x"})
	if err := r.turn("hi"); !strings.Contains(err, "the tools option is #f (no tools)") {
		t.Fatalf("error %q", err)
	}
}

// A mistyped tool name gets the likely one: models invent names in the
// family's style (fs-grep for index-grep, fs_edit for fs-edit).
func TestUnknownToolSuggestsTheLikelyOne(t *testing.T) {
	r := strategyRig(t, "", 10,
		fake.Turn{ToolCalls: []fake.ToolCall{{ID: "g", Name: "fs-grep", Args: map[string]any{"path": ".", "pattern": "x"}}, {ID: "e", Name: "fs_edit", Args: map[string]any{}}, {ID: "z", Name: "zap", Args: map[string]any{}}}},
		fake.Turn{Content: "done"},
	)
	if err := r.turn("go"); err != "" {
		t.Fatal(err)
	}
	res := r.results()
	if !strings.Contains(res["g"], "did you mean index-grep?") || !strings.Contains(res["e"], "did you mean fs-edit?") || strings.Contains(res["z"], "did you mean") {
		t.Fatalf("%+v", res)
	}
}
