package core

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fclaude/nibb/internal/config"
	"github.com/fclaude/nibb/internal/event"
	"github.com/fclaude/nibb/internal/llm"
	"github.com/fclaude/nibb/internal/log"
	fake "github.com/fclaude/nibb/test/fakes/ollama"
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
	repo := t.TempDir()
	os.WriteFile(filepath.Join(repo, "main.go"), []byte("package main\n"), 0o644)
	srv := fake.New(turns...)
	t.Cleanup(srv.Close)
	cfg := config.Default()
	cfg.Control.URL = srv.URL
	cfg.Loop.MaxToolCalls = 3
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
	r := newRig(t, false,
		fake.Turn{Thinking: "need the file", ToolCalls: []fake.ToolCall{{ID: "c1", Name: "fs-read", Args: map[string]any{"path": "main.go"}}}},
		fake.Turn{Content: "It is a Go main package.", Chunk: 4},
	)
	if err := r.s.Send("what is main.go?"); err != nil {
		t.Fatal(err)
	}
	evs := r.drain("n")
	want := "session.start user.msg llm.req status stream.thinking llm.resp tool.call status guard.decision tool.result llm.req status stream.text stream.text stream.text stream.text stream.text stream.text llm.resp status"
	if got := kinds(evs); got != want {
		t.Fatalf("events:\n got %s\nwant %s", got, want)
	}
	// The second request carried the tool result verbatim and no thinking.
	raw := string(r.srv.Requests[1].Raw)
	if !strings.Contains(raw, `"role":"tool","content":"package main\n","tool_name":"fs-read","tool_call_id":"c1"`) {
		t.Fatalf("tool result not fed back:\n%s", raw)
	}
	if strings.Contains(raw, "need the file") {
		t.Fatal("thinking was fed back")
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
	if evsLog[0].Kind != log.KindSessionStart || evsLog[len(evsLog)-1].Kind != log.KindLLMResp {
		t.Fatalf("log shape: %s ... %s", evsLog[0].Kind, evsLog[len(evsLog)-1].Kind)
	}
}

func TestConfirmYesAndNo(t *testing.T) {
	r := newRig(t, false,
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

func TestNoConfirmDeniesWithoutPrompt(t *testing.T) {
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
	r := newRig(t, true,
		fake.Turn{ToolCalls: []fake.ToolCall{{ID: "x", Name: "car", Args: map[string]any{}}}},
		fake.Turn{ToolCalls: []fake.ToolCall{{ID: "y", Name: "fs-read", Args: map[string]any{"file": "main.go"}}}},
		fake.Turn{Content: "sorry"},
	)
	r.s.Send("go")
	r.drain("n")
	if raw := string(r.srv.Requests[1].Raw); !strings.Contains(raw, `unknown tool`) || !strings.Contains(raw, `available: fs-list, fs-read, fs-write, shell-exec`) {
		t.Fatalf("unknown tool: %s", r.srv.Requests[1].Raw)
	}
	if !strings.Contains(string(r.srv.Requests[2].Raw), `missing argument`) {
		t.Fatalf("bad args: %s", r.srv.Requests[2].Raw)
	}
}

func TestHardStop(t *testing.T) {
	call := fake.Turn{ToolCalls: []fake.ToolCall{{ID: "c", Name: "fs-list", Args: map[string]any{"path": "."}}}}
	r := newRig(t, true, call, call, call, call, call)
	r.s.Send("loop forever")
	evs := r.drain("n")
	last := evs[len(evs)-2]
	if last.Kind != event.Error || !strings.Contains(last.Payload.(event.ErrorInfo).Err, "hard stop") {
		t.Fatalf("expected hard stop error, got %s", kinds(evs))
	}
}

func TestAbortDuringConfirm(t *testing.T) {
	r := newRig(t, false,
		fake.Turn{ToolCalls: []fake.ToolCall{{ID: "w", Name: "fs-write", Args: map[string]any{"path": "a", "content": "b"}}}},
	)
	r.s.Send("write")
	var evs []event.Event
	for ev := range r.s.Events() {
		evs = append(evs, ev)
		if ev.Kind == event.PromptConfirm {
			r.s.Abort()
		}
		if ev.Kind == event.Status && ev.Payload.(event.StatusInfo).State == "idle" {
			break
		}
	}
	if !strings.Contains(kinds(evs), "error") {
		t.Fatalf("abort should surface as an error: %s", kinds(evs))
	}
	if r.s.Busy() {
		t.Fatal("still busy after abort")
	}
	if err := r.s.Send("again"); err != nil {
		t.Fatalf("session unusable after abort: %v", err)
	}
	r.drain("n")
}

func TestSendWhileBusyAndEval(t *testing.T) {
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
	repo := t.TempDir()
	os.MkdirAll(filepath.Join(repo, ".nibb"), 0o755)
	os.WriteFile(filepath.Join(repo, ".nibb", "init.scm"), []byte(`
(register-tool "greet" (lambda (name) (string-append "hello " name))
  '((doc . "Greet someone") (params . (("name" "string" "who")))))`), 0o644)
	srv := fake.New(fake.Turn{ToolCalls: []fake.ToolCall{{ID: "g", Name: "greet", Args: map[string]any{"name": "fc"}}}}, fake.Turn{Content: "done"})
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
