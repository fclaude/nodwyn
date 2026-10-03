package core

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fclaude/nodwyn/internal/config"
	"github.com/fclaude/nodwyn/internal/event"
	"github.com/fclaude/nodwyn/internal/llm"
	"github.com/fclaude/nodwyn/internal/log"
	fake "github.com/fclaude/nodwyn/test/fakes/ollama"
)

type rig struct {
	t    *testing.T
	s    *Session
	srv  *fake.Server
	repo string
	logb *bytes.Buffer
}

func newRig(t *testing.T, noConfirm bool, turns ...fake.Turn) *rig {
	t.Helper()
	return newRigWith(t, noConfirm, nil, turns...)
}

// newAskingRig is newRig with writes that ask (approval.writes = "ask"), for
// tests of the write prompt and its answers.
func newAskingRig(t *testing.T, turns ...fake.Turn) *rig {
	t.Helper()
	return newRigWith(t, false, func(c *config.Config) { c.Approval.Writes = config.ApproveAsk }, turns...)
}

func newRigWith(t *testing.T, noConfirm bool, adjust func(*config.Config), turns ...fake.Turn) *rig {
	t.Helper()
	repo := t.TempDir()
	os.WriteFile(filepath.Join(repo, "main.go"), []byte("package main\n"), 0o644)
	srv := fake.NewT(t, turns...)
	t.Cleanup(srv.Close)
	cfg := config.Default()
	cfg.Control.URL = srv.URL
	cfg.Control.Model = "test-model"
	cfg.Loop.MaxToolCalls = 3
	if adjust != nil {
		adjust(&cfg)
	}
	logb := &bytes.Buffer{}
	s, err := New(Options{Config: cfg, Repo: repo, Version: "test", Provider: llm.NewOllama(srv.URL), NoConfirm: noConfirm, LogWriter: logb})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return &rig{t: t, s: s, srv: srv, repo: repo, logb: logb}
}

// drain collects events until the session goes idle, answering prompts
// with answer.
func (r *rig) drain(answer string) []event.Event {
	r.t.Helper()
	var evs []event.Event
	timeout := time.After(10 * time.Second)
	for {
		select {
		case ev := <-r.s.Events():
			evs = append(evs, ev)
			switch ev.Kind {
			case event.PromptConfirm:
				if err := r.s.Answer(ev.Payload.(event.Confirm).ID, answer); err != nil {
					r.t.Fatal(err)
				}
			case event.Status:
				if ev.Payload.(event.StatusInfo).State == "idle" {
					return evs
				}
			}
		case <-timeout:
			r.t.Fatalf("no idle after 10s; events: %d", len(evs))
		}
	}
}

func kinds(evs []event.Event) string {
	var ks []string
	for _, e := range evs {
		ks = append(ks, e.Kind)
	}
	return strings.Join(ks, " ")
}

func TestTurnWithToolCall(t *testing.T) {
	t.Parallel()
	r := newRig(t, false,
		fake.Turn{Thinking: "need the file", ToolCalls: []fake.ToolCall{{ID: "c1", Name: "fs-read", Args: map[string]any{"path": "main.go"}}}},
		fake.Turn{Content: "It is a Go main package.", Chunk: 4},
	)
	if err := r.s.Send("what is main.go?"); err != nil {
		t.Fatal(err)
	}
	evs := r.drain("n")
	want := "session.start user.msg llm.req status stream.thinking llm.resp tool.call status guard.decision tool.result llm.req status stream.text llm.resp turn.end status"
	if got := kinds(evs); got != want {
		t.Fatalf("events:\n got %s\nwant %s", got, want)
	}
	// The second request carried the tool result verbatim, and the thinking
	// on the reply it came with (window recipe v14).
	raw := string(r.srv.Requests[1].Raw)
	if !strings.Contains(raw, `"role":"tool","content":"package main\n","tool_name":"fs-read","tool_call_id":"c1"`) {
		t.Fatalf("tool result not fed back:\n%s", raw)
	}
	if !strings.Contains(raw, `"role":"assistant","content":"","thinking":"need the file","tool_calls":[`) {
		t.Fatalf("thinking not fed back on its reply:\n%s", raw)
	}
	// Prefix stability: request 2 starts with request 1's messages.
	m1, m2 := r.srv.Requests[0].Messages, r.srv.Requests[1].Messages
	for i := range m1 {
		if string(m1[i]) != string(m2[i]) {
			t.Fatalf("prefix changed at message %d", i)
		}
	}
	// The log has everything and replays.
	evsLog, err := log.Read(bytes.NewReader(r.logb.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	if evsLog[0].Kind != log.KindSessionStart || evsLog[len(evsLog)-1].Kind != log.KindTurnEnd {
		t.Fatalf("log shape: %s ... %s", evsLog[0].Kind, evsLog[len(evsLog)-1].Kind)
	}
}

func TestConfirmYesAndNo(t *testing.T) {
	t.Parallel()
	r := newAskingRig(t,
		fake.Turn{ToolCalls: []fake.ToolCall{{ID: "w1", Name: "fs-write", Args: map[string]any{"path": "new.txt", "content": "hi\n"}}}},
		fake.Turn{Content: "written"},
		fake.Turn{ToolCalls: []fake.ToolCall{{ID: "w2", Name: "fs-write", Args: map[string]any{"path": "new.txt", "content": "bye\n"}}}},
		fake.Turn{Content: "understood"},
	)
	r.s.Send("create new.txt")
	evs := r.drain("y")
	var prompt event.Confirm
	for _, e := range evs {
		if e.Kind == event.PromptConfirm {
			prompt = e.Payload.(event.Confirm)
		}
	}
	if prompt.Prompt.Prim != "fs-write" || !strings.Contains(prompt.Prompt.Detail, "+hi") {
		t.Fatalf("prompt: %+v", prompt)
	}
	if b, _ := os.ReadFile(filepath.Join(r.repo, "new.txt")); string(b) != "hi\n" {
		t.Fatal("file not written after yes")
	}

	r.s.Send("now change it")
	r.drain("n")
	if b, _ := os.ReadFile(filepath.Join(r.repo, "new.txt")); string(b) != "hi\n" {
		t.Fatal("file changed after no")
	}
	raw := string(r.srv.Requests[3].Raw)
	if !strings.Contains(raw, `denied: not confirmed by the user`) {
		t.Fatalf("model was not told about the denial:\n%s", raw)
	}
}

// Several changes to one file go in one fs-edit call: the tool list shows
// an edit's fields, the call asks once with one diff of the file all the
// edits leave, and the file is written with every one of them.
func TestFsEditWithEditsAsksOnceWithOneDiff(t *testing.T) {
	t.Parallel()
	r := newAskingRig(t,
		fake.Turn{ToolCalls: []fake.ToolCall{{ID: "e1", Name: "fs-edit", Args: map[string]any{"path": "main.go", "edits": []any{
			map[string]any{"old": "package main\n", "new": "package main\n\nfunc a() {}\n"},
			map[string]any{"old": "func a() {}\n", "new": "func a() {}\n\nfunc b() {}\n"},
		}}}}},
		fake.Turn{Content: "added a and b"},
	)
	r.s.Send("add a and b")
	evs := r.drain("y")
	var prompts []event.Confirm
	for _, e := range evs {
		if e.Kind == event.PromptConfirm {
			prompts = append(prompts, e.Payload.(event.Confirm))
		}
	}
	if len(prompts) != 1 || prompts[0].Prompt.Prim != "fs-edit" {
		t.Fatalf("confirms: %+v", prompts)
	}
	if d := prompts[0].Prompt.Detail; !strings.Contains(d, "+func a() {}\n") || !strings.Contains(d, "+func b() {}\n") || strings.Count(d, "@@ ") != 1 {
		t.Fatalf("the confirm does not show one diff of both edits:\n%s", d)
	}
	if b, _ := os.ReadFile(filepath.Join(r.repo, "main.go")); string(b) != "package main\n\nfunc a() {}\n\nfunc b() {}\n" {
		t.Fatalf("main.go: %q", b)
	}
	if tools := string(r.srv.Requests[0].Raw); !strings.Contains(tools, `"edits":{"description":`) || !strings.Contains(tools, `"required":["old","new"],"type":"object"},"type":"array"}`) {
		t.Fatalf("the tool list does not show an edit's fields:\n%s", tools)
	}
	if raw := string(r.srv.Requests[1].Raw); !strings.Contains(raw, `"role":"tool","content":"ok","tool_name":"fs-edit"`) {
		t.Fatalf("the result fed back:\n%s", raw)
	}
}

func TestNoConfirmDeniesWithoutPrompt(t *testing.T) {
	t.Parallel()
	r := newRig(t, true,
		fake.Turn{ToolCalls: []fake.ToolCall{{ID: "s1", Name: "shell-exec", Args: map[string]any{"cmd": "true", "args": []any{}}}}},
		fake.Turn{Content: "ok"},
	)
	r.s.Send("run true")
	evs := r.drain("y")
	if strings.Contains(kinds(evs), event.PromptConfirm) {
		t.Fatal("prompted despite NoConfirm")
	}
	if !strings.Contains(string(r.srv.Requests[1].Raw), "not confirmed") {
		t.Fatal("expected a denial result")
	}
}

func TestUnknownToolAndBadArgsAreResults(t *testing.T) {
	t.Parallel()
	r := newRig(t, true,
		fake.Turn{ToolCalls: []fake.ToolCall{{ID: "x", Name: "car", Args: map[string]any{}}}},
		fake.Turn{ToolCalls: []fake.ToolCall{{ID: "y", Name: "fs-read", Args: map[string]any{"file": "main.go"}}}},
		fake.Turn{Content: "sorry"},
	)
	r.s.Send("go")
	r.drain("n")
	if raw := string(r.srv.Requests[1].Raw); !strings.Contains(raw, `unknown tool`) || !strings.Contains(raw, `available: doc-export, fs-edit, fs-find, fs-list, fs-read, fs-write, index-grep, plan-step, plan-write, read-definition, second-look, shell-exec`) {
		t.Fatalf("unknown tool: %s", r.srv.Requests[1].Raw)
	}
	if !strings.Contains(string(r.srv.Requests[2].Raw), `missing argument`) {
		t.Fatalf("bad args: %s", r.srv.Requests[2].Raw)
	}
}

func TestHardStop(t *testing.T) {
	t.Parallel()
	call := fake.Turn{ToolCalls: []fake.ToolCall{{ID: "c", Name: "fs-list", Args: map[string]any{"path": "."}}}}
	r := newRig(t, true, call, call, call, call, call)
	r.s.Send("loop forever")
	evs := r.drain("n")
	last := evs[len(evs)-3]
	if last.Kind != event.Error || !strings.Contains(last.Payload.(event.ErrorInfo).Err, "hard stop") {
		t.Fatalf("expected hard stop error, got %s", kinds(evs))
	}
}

// An abort while a write waits for its confirm ends the turn with an error,
// writes nothing, and leaves the session usable.
func TestAbortDuringConfirm(t *testing.T) {
	t.Parallel()
	r := newAskingRig(t,
		fake.Turn{ToolCalls: []fake.ToolCall{{ID: "w", Name: "fs-write", Args: map[string]any{"path": "a", "content": "b"}}}},
		fake.Turn{Content: "ok"},
	)
	r.s.Send("write")
	var evs []event.Event
	asked, failed := false, ""
	for ev := range r.s.Events() {
		evs = append(evs, ev)
		if ev.Kind == event.PromptConfirm {
			asked = true
			r.s.Abort()
		}
		if ev.Kind == event.Error {
			failed = ev.Payload.(event.ErrorInfo).Err
		}
		if ev.Kind == event.Status && ev.Payload.(event.StatusInfo).State == "idle" {
			break
		}
	}
	if !asked || !strings.Contains(failed, "context canceled") {
		t.Fatalf("abort at the confirm should surface as an error: asked %v, error %q, events %s", asked, failed, kinds(evs))
	}
	if _, err := os.Stat(filepath.Join(r.repo, "a")); err == nil {
		t.Fatal("the aborted write ran")
	}
	if r.s.Busy() {
		t.Fatal("still busy after abort")
	}
	if e := r.turn("again"); e != "" {
		t.Fatalf("session unusable after abort: %s", e)
	}
}

func TestSendWhileBusyAndEval(t *testing.T) {
	t.Parallel()
	r := newRig(t, true, fake.Turn{Content: "hi", Chunk: 1})
	r.s.Send("a")
	if err := r.s.Send("b"); err == nil {
		t.Fatal("second Send while busy should fail")
	}
	r.drain("n")
	v, err := r.s.Eval(context.Background(), `(begin (display "out") (+ 1 2))`)
	if err != nil || v.I != 3 {
		t.Fatalf("eval: %v %v", v, err)
	}
	select {
	case ev := <-r.s.Events():
		if ev.Kind != event.Output || ev.Payload.(event.Text).Text != "out" {
			t.Fatalf("output event: %+v", ev)
		}
	case <-time.After(time.Second):
		t.Fatal("no output event")
	}
}

func TestInitFileRegistersTools(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	os.MkdirAll(filepath.Join(repo, ".nodwyn"), 0o755)
	os.WriteFile(filepath.Join(repo, ".nodwyn", "init.scm"), []byte(`
(register-tool "greet" (lambda (name) (string-append "hello " name))
  '((doc . "Greet someone") (params . (("name" "string" "who")))))`), 0o644)
	srv := fake.NewT(t, fake.Turn{ToolCalls: []fake.ToolCall{{ID: "g", Name: "greet", Args: map[string]any{"name": "fc"}}}}, fake.Turn{Content: "done"})
	defer srv.Close()
	cfg := config.Default()
	s, err := New(Options{Config: cfg, Repo: repo, Provider: llm.NewOllama(srv.URL), NoConfirm: true, LogWriter: &bytes.Buffer{}})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	names := ""
	for _, tl := range s.Tools() {
		names += tl.Name + " "
	}
	if !strings.Contains(names, "greet ") {
		t.Fatalf("tools: %s", names)
	}
	r := &rig{t: t, s: s, srv: srv}
	s.Send("greet me")
	r.drain("n")
	if !strings.Contains(string(srv.Requests[1].Raw), `"content":"hello fc"`) {
		t.Fatalf("registered tool result: %s", srv.Requests[1].Raw)
	}
}

func TestTurnTrustAndStandingWrites(t *testing.T) {
	t.Parallel()
	w := func(id, path, content string) fake.Turn {
		return fake.Turn{ToolCalls: []fake.ToolCall{{ID: id, Name: "fs-write", Args: map[string]any{"path": path, "content": content}}}}
	}
	r := newAskingRig(t,
		w("1", "a/one.txt", "1"), w("2", "a/two.txt", "2"), w("3", "b/three.txt", "3"), fake.Turn{Content: "turn one done"},
		w("4", "a/four.txt", "4"), w("5", ".nodwyn/allow.toml", "x"), fake.Turn{Content: "turn two done"},
	)
	// Turn 1: answer "t" at the first prompt; the next two writes must not prompt.
	r.s.Send("write three files")
	prompts := 0
	var evs []event.Event
	for ev := range r.s.Events() {
		evs = append(evs, ev)
		if ev.Kind == event.PromptConfirm {
			prompts++
			r.s.Answer(ev.Payload.(event.Confirm).ID, "t")
		}
		if ev.Kind == event.Status && ev.Payload.(event.StatusInfo).State == "idle" {
			break
		}
	}
	if prompts != 1 {
		t.Fatalf("turn trust: %d prompts, want 1", prompts)
	}
	for _, f := range []string{"a/one.txt", "a/two.txt", "b/three.txt"} {
		if _, err := os.Stat(filepath.Join(r.repo, f)); err != nil {
			t.Fatalf("%s not written", f)
		}
	}
	notes := 0
	for _, e := range evs {
		if e.Kind == "note" && strings.Contains(e.Payload.(log.Note).Text, "turn trust") {
			notes++
		}
	}
	if notes != 2 {
		t.Fatalf("expected 2 turn-trust notes carrying diffs, got %d", notes)
	}
	// Turn 2: trust has expired; "a" at the a/ prompt makes a/ standing; the
	// protected .nodwyn/ path still prompts.
	r.s.Send("two more")
	prompts = 0
	for ev := range r.s.Events() {
		if ev.Kind == event.PromptConfirm {
			prompts++
			r.s.Answer(ev.Payload.(event.Confirm).ID, "a")
		}
		if ev.Kind == event.Status && ev.Payload.(event.StatusInfo).State == "idle" {
			break
		}
	}
	if prompts != 2 {
		t.Fatalf("turn two: %d prompts, want 2 (a/ then protected .nodwyn/)", prompts)
	}
	if got := strings.Join(r.s.AllowedWrites(), ","); got != "a/" {
		t.Fatalf("standing writes: %q", got)
	}
}

// A predicate matches with any true value, not only #t: one ending in
// (member …) returns a list, and must not fail open.
func TestRegisterCheckTruthyPredicateMatches(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	os.MkdirAll(filepath.Join(repo, ".nodwyn"), 0o755)
	os.WriteFile(filepath.Join(repo, "x.txt"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(repo, ".nodwyn", "init.scm"), []byte(`
(register-check "no-reads" "deny" (lambda (name args) (member name '("fs-read"))))`), 0o644)
	srv := fake.NewT(t, fake.Turn{ToolCalls: []fake.ToolCall{{ID: "r", Name: "fs-read", Args: map[string]any{"path": "x.txt"}}}}, fake.Turn{Content: "done"})
	defer srv.Close()
	s, err := New(Options{Config: config.Default(), Repo: repo, Provider: llm.NewOllama(srv.URL), NoConfirm: true, LogWriter: &bytes.Buffer{}})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	r := &rig{t: t, s: s, srv: srv}
	s.Send("read x")
	r.drain("n")
	if !strings.Contains(string(srv.Requests[1].Raw), "user checker no-reads") {
		t.Fatalf("a predicate returning a list did not match:\n%s", srv.Requests[1].Raw)
	}
}

func TestInitRegisterCheckSurvivesGuardSetup(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	os.MkdirAll(filepath.Join(repo, ".nodwyn"), 0o755)
	os.WriteFile(filepath.Join(repo, "x.txt"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(repo, ".nodwyn", "init.scm"), []byte(`
(register-check "no-reads" "deny" (lambda (name args) (equal? name "fs-read")))`), 0o644)
	srv := fake.NewT(t, fake.Turn{ToolCalls: []fake.ToolCall{{ID: "r", Name: "fs-read", Args: map[string]any{"path": "x.txt"}}}}, fake.Turn{Content: "done"})
	defer srv.Close()
	s, err := New(Options{Config: config.Default(), Repo: repo, Provider: llm.NewOllama(srv.URL), NoConfirm: true, LogWriter: &bytes.Buffer{}})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	r := &rig{t: t, s: s, srv: srv}
	s.Send("read x")
	r.drain("n")
	if !strings.Contains(string(srv.Requests[1].Raw), "user checker no-reads") {
		t.Fatalf("checker registered in init.scm was lost:\n%s", srv.Requests[1].Raw)
	}
}
