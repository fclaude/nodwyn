package core

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/fclaude/nodwyn/internal/log"
	fake "github.com/fclaude/nodwyn/test/fakes/ollama"
)

// The fake server reports 279 + 10 prompt tokens per message, so a
// threshold of 300 is passed by the second request (system, user, one
// assistant call and its result: 319).
const compactInit = `(define compact-threshold 300)`

type sent struct{ Role, Content string }

func requestMessages(t *testing.T, req fake.Request) []sent {
	t.Helper()
	out := make([]sent, len(req.Messages))
	for i, raw := range req.Messages {
		if err := json.Unmarshal(raw, &out[i]); err != nil {
			t.Fatal(err)
		}
	}
	return out
}

func (r *rig) narrowings() int {
	r.t.Helper()
	evs, err := log.Read(bytes.NewReader(r.logb.Bytes()))
	if err != nil {
		r.t.Fatal(err)
	}
	n := 0
	for _, ev := range evs {
		if ev.Kind == log.KindContextNarrow {
			n++
		}
	}
	return n
}

func TestCompactionHandsOverInANarrowedContext(t *testing.T) {
	r := strategyRig(t, compactInit, 10,
		fake.Turn{ToolCalls: []fake.ToolCall{readCall("a")}},
		fake.Turn{ToolCalls: []fake.ToolCall{readCall("b")}},
		fake.Turn{Content: "NOTE: main.go only declares package main; nothing changed yet."},
		fake.Turn{Content: "done"},
	)
	if err := r.turn("make main.go print hello"); err != "" {
		t.Fatal(err)
	}
	if len(r.srv.Requests) != 4 {
		t.Fatalf("%d requests, want 4", len(r.srv.Requests))
	}
	if ask := requestMessages(t, r.srv.Requests[2]); !strings.Contains(ask[len(ask)-1].Content, "write a handover note") {
		t.Fatalf("third request did not ask for the note: %+v", ask[len(ask)-1])
	}
	after := requestMessages(t, r.srv.Requests[3])
	system := r.s.loop.WindowContext().System
	if len(after) != 2 || after[0].Role != "system" || after[0].Content != system || after[1].Role != "user" {
		t.Fatalf("compacted request is not the session prompt and one hand-over: %+v", after)
	}
	handover := after[1].Content
	if !strings.HasPrefix(handover, "make main.go print hello") || !strings.HasSuffix(handover, "Handover note:\nNOTE: main.go only declares package main; nothing changed yet.") {
		t.Fatalf("hand-over:\n%s", handover)
	}
	if strings.Contains(string(r.srv.Requests[3].Raw), `package main\n`) {
		t.Fatal("the compacted context still carries the old tool results")
	}
	if n := r.narrowings(); n != 1 {
		t.Fatalf("%d narrowings, want 1", n)
	}
}

func TestCompactionIsOffByDefault(t *testing.T) {
	r := strategyRig(t, "", 10,
		fake.Turn{ToolCalls: []fake.ToolCall{readCall("a")}},
		fake.Turn{ToolCalls: []fake.ToolCall{readCall("b")}},
		fake.Turn{Content: "done"},
	)
	if err := r.turn("make main.go print hello"); err != "" {
		t.Fatal(err)
	}
	if len(r.srv.Requests) != 3 || r.narrowings() != 0 {
		t.Fatalf("%d requests and %d narrowings without a threshold", len(r.srv.Requests), r.narrowings())
	}
	if got := r.eval("compact-threshold"); got != "#f" {
		t.Fatalf("compact-threshold defaults to %s", got)
	}
}

// A model that answers the note request with tool calls has them skipped;
// after compact-attempts replies compaction gives up, the turn goes on in
// the full context, and it is not retried until the context grows.
func TestCompactionGivesUpWithoutANote(t *testing.T) {
	r := strategyRig(t, compactInit, 10,
		fake.Turn{ToolCalls: []fake.ToolCall{readCall("a")}},
		fake.Turn{ToolCalls: []fake.ToolCall{readCall("b")}},
		fake.Turn{ToolCalls: []fake.ToolCall{readCall("c")}},
		fake.Turn{ToolCalls: []fake.ToolCall{readCall("d")}},
		fake.Turn{Content: "done"},
	)
	if err := r.turn("make main.go print hello"); err != "" {
		t.Fatal(err)
	}
	if r.narrowings() != 0 {
		t.Fatal("compacted without a note")
	}
	results := r.results()
	for _, id := range []string{"c", "d"} {
		if !strings.Contains(results[id], "write the handover note first") {
			t.Fatalf("call %s during the note request: %q", id, results[id])
		}
	}
	last := requestMessages(t, r.srv.Requests[len(r.srv.Requests)-1])
	if !strings.Contains(last[len(last)-1].Content, "Never mind the handover note") {
		t.Fatalf("the model was not told to go on: %+v", last[len(last)-1])
	}
	if got := r.eval("compact-gave-up-at"); got == "0" {
		t.Fatal("giving up did not record the size")
	}
}
