package core

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fclaude/nibb/internal/event"
	"github.com/fclaude/nibb/internal/log"
	"github.com/fclaude/nibb/internal/window"
	fake "github.com/fclaude/nibb/test/fakes/ollama"
)

func TestIngressCanary(t *testing.T) {
	const key = "sk-ant-abcdefgh1234"
	const placeholder = "[REDACTED anthropic-key]"
	r := newRig(t, false,
		fake.Turn{ToolCalls: []fake.ToolCall{{ID: "c1", Name: "shell-exec", Args: map[string]any{"cmd": "cat", "args": []string{".env"}}}}},
		fake.Turn{Content: "The key is " + key, Thinking: key, Chunk: 1},
		fake.Turn{Content: "Only the placeholder remains."},
	)
	if err := os.WriteFile(filepath.Join(r.repo, ".env"), []byte("TOKEN="+key), 0600); err != nil {
		t.Fatal(err)
	}
	if err := r.s.Send("My key is " + key); err != nil {
		t.Fatal(err)
	}
	evs := r.drain("y")
	if err := r.s.Send("repeat the key"); err != nil {
		t.Fatal(err)
	}
	evs = append(evs, r.drain("y")...)
	for _, req := range r.srv.Requests {
		if strings.Contains(string(req.Raw), key) {
			t.Fatal("canary in model request")
		}
	}
	var tool log.ToolResult
	var audit bool
	for _, ev := range evs {
		b, err := json.Marshal(ev)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(b), key) {
			t.Fatalf("canary in frontend event %s", ev.Kind)
		}
		if ev.Kind == string(log.KindToolResult) {
			tool = ev.Payload.(log.ToolResult)
		}
		if ev.Kind == string(log.KindRedact) {
			audit = true
		}
	}
	if !tool.OK || !strings.Contains(tool.Value, placeholder) {
		t.Fatalf("shell result not redacted: %+v", tool)
	}
	if !audit {
		t.Fatal("no redaction audit")
	}
	if strings.Contains(r.logb.String(), key) {
		t.Fatal("canary on disk")
	}
	replay, err := log.Read(strings.NewReader(r.logb.String()))
	if err != nil {
		t.Fatal(err)
	}
	a, _ := json.Marshal(window.Build(replay, "system", 0, window.Policy{}))
	b, _ := json.Marshal(window.Build(r.s.EventsSoFar(), "system", 0, window.Policy{}))
	if string(a) != string(b) {
		t.Fatal("replay differs from live context")
	}
}

// The plan gate (plan.scm) neither sends a key in the request to the model
// nor records one the model writes into its plan.
func TestPlanAndEphemeralRedaction(t *testing.T) {
	const key = "sk-ant-abcdefgh1234"
	r := newRig(t, true, fake.Turn{Content: "1. call the API with " + key}, fake.Turn{Content: "done"})
	if err := r.s.SendPlanned("use " + key + " to call the API"); err != nil {
		t.Fatal(err)
	}
	r.drain("n")
	for i, req := range r.srv.Requests {
		if strings.Contains(string(req.Raw), key) {
			t.Fatalf("request %d carried the key", i)
		}
	}
	if !strings.Contains(r.logb.String(), `"kind":"gate.plan"`) || strings.Contains(r.logb.String(), key) {
		t.Fatal("the plan was not recorded, or was recorded with the key")
	}
	r.s.Emit(event.Event{Kind: event.Error, Payload: event.ErrorInfo{Err: key}})
	ev := <-r.s.Events()
	if strings.Contains(ev.Payload.(event.ErrorInfo).Err, key) {
		t.Fatal("error bypass")
	}
}

func TestSplitSchemeOutput(t *testing.T) {
	r := newRig(t, true)
	_, err := r.s.Eval(context.Background(), `(begin (display "sk-ant-") (display "abcdefgh1234"))`)
	if err != nil {
		t.Fatal(err)
	}
	<-r.s.Events() // session.start
	ev := <-r.s.Events()
	if ev.Kind != event.Output || ev.Payload.(event.Text).Text != "[REDACTED anthropic-key]" {
		t.Fatalf("split output bypass: %+v", ev)
	}
}

// Scheme escapes such as \n must not merge a secret with a preceding letter
// before scanning. Scan the actual string leaves, including nested values.
func TestEvalResultRedactsBeforeQuoting(t *testing.T) {
	const canary = "AKIAABCDEFGHIJKLMNOP"
	for _, source := range []string{
		`(fs-read "notes.txt")`,
		`(list (fs-read "notes.txt") (vector (fs-read "notes.txt")))`,
	} {
		t.Run(source, func(t *testing.T) {
			r := newRig(t, true)
			content := "Notes\n" + canary + "\tend"
			if err := os.WriteFile(filepath.Join(r.repo, "notes.txt"), []byte(content), 0600); err != nil {
				t.Fatal(err)
			}
			if err := r.s.StartEval(source, "test"); err != nil {
				t.Fatal(err)
			}
			events := r.drain("y")
			var resultSeq uint64
			var audited bool
			for _, ev := range events {
				raw, err := json.Marshal(ev)
				if err != nil {
					t.Fatal(err)
				}
				if strings.Contains(string(raw), canary) {
					t.Fatalf("canary in %s", ev.Kind)
				}
			}
			for _, ev := range r.s.EventsSoFar() {
				if strings.Contains(string(ev.Payload), canary) {
					t.Fatalf("canary in history %s", ev.Kind)
				}
				if ev.Kind == log.KindEvalResult {
					resultSeq = ev.Seq
					if !strings.Contains(string(ev.Payload), "[REDACTED aws-access-key]") {
						t.Fatal(string(ev.Payload))
					}
				}
				if ev.Kind == log.KindRedact {
					var audit struct {
						Source uint64 `json:"source_seq"`
					}
					if err := json.Unmarshal(ev.Payload, &audit); err != nil {
						t.Fatal(err)
					}
					if resultSeq != 0 && audit.Source == resultSeq {
						audited = true
					}
				}
			}
			if resultSeq == 0 || !audited {
				t.Fatal("missing evaluation result or linked redaction audit")
			}
			if strings.Contains(r.logb.String(), canary) {
				t.Fatal("canary persisted")
			}
			value, err := r.s.Eval(context.Background(), `(fs-read "notes.txt")`)
			if err != nil || value.S != content {
				t.Fatal("evaluation value changed", err)
			}
		})
	}
}
