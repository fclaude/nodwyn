package core

import (
	"bytes"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/fclaude/nibb/internal/log"
	"github.com/fclaude/nibb/internal/window"
	fake "github.com/fclaude/nibb/test/fakes/ollama"
)

// A strategy's sub-agent works in its own context; once it answers, the
// caller's context is as it was, plus what the strategy does with the answer.
func TestSubAgentFoldsAway(t *testing.T) {
	r := strategyRig(t, `
(define (agent-turn request)
  (let ((reply (model-turn)))
    (let ((verdict (sub-agent "You review changes." "Review: the change adds a flag." '() "reviewer")))
      (add-message (string-append "A reviewer says: " verdict))
      (model-turn))))`, 10,
		fake.Turn{Content: "I added the flag."},
		fake.Turn{Content: "VERDICT: incomplete; the flag is never read."},
		fake.Turn{Content: "Now it is read."},
	)
	if err := r.turn("add a --verbose flag"); err != "" {
		t.Fatal(err)
	}
	review := requestMessages(t, r.srv.Requests[1])
	if len(review) != 2 || review[0].Content != "You review changes." || review[1].Content != "Review: the change adds a flag." {
		t.Fatalf("the sub-agent's context: %+v", review)
	}
	after := requestMessages(t, r.srv.Requests[2])
	if len(after) != 4 || after[2].Content != "I added the flag." ||
		after[3].Content != "A reviewer says: VERDICT: incomplete; the flag is never read." {
		t.Fatalf("the caller's context after the fold: %+v", after)
	}
	if strings.Contains(string(r.srv.Requests[2].Raw), "Review: the change adds a flag.") {
		t.Fatal("the sub-agent's task leaked into the caller's context")
	}
}

// An agent tool is called by the model like any tool and answered by a
// sub-agent, which may use only the tools it was given.
func TestAgentToolAnswersFromSubAgent(t *testing.T) {
	r := strategyRig(t, `
(define-agent-tool "ask-expert" "Ask an expert who can read files."
  '(("question" "string" "what to ask"))
  (lambda (call args)
    (sub-agent "You are an expert." (assoc-ref args "question") '("fs-read"))))`, 10,
		fake.Turn{ToolCalls: []fake.ToolCall{{ID: "q", Name: "ask-expert", Args: map[string]any{"question": "What is in main.go?"}}}},
		fake.Turn{ToolCalls: []fake.ToolCall{readCall("r"), {ID: "l", Name: "fs-list", Args: map[string]any{"path": "."}}}},
		fake.Turn{Content: "It only declares package main."},
		fake.Turn{Content: "done"},
	)
	if err := r.turn("what does main.go do?"); err != "" {
		t.Fatal(err)
	}
	if len(r.srv.Requests) != 4 {
		t.Fatalf("%d requests, want 4", len(r.srv.Requests))
	}
	expert := requestMessages(t, r.srv.Requests[1])
	if expert[0].Content != "You are an expert." || expert[1].Content != "What is in main.go?" {
		t.Fatalf("the expert's context: %+v", expert)
	}
	results := r.results()
	if results["r"] != "" || !strings.Contains(results["l"], "not available to this agent") {
		t.Fatalf("the expert's own calls: %+v", results)
	}
	main := requestMessages(t, r.srv.Requests[3])
	if len(main) != 4 || main[2].Role != "assistant" || main[3].Role != "tool" || main[3].Content != "It only declares package main." {
		t.Fatalf("the model's context after its call was answered: %+v", main)
	}
	// The log names the agent and the call it answered, so its transcript
	// can be read on its own later.
	evs, err := log.Read(bytes.NewReader(r.logb.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	var narrow log.ContextNarrow
	for _, ev := range evs {
		if ev.Kind == log.KindContextNarrow {
			if err := ev.Decode(&narrow); err != nil {
				t.Fatal(err)
			}
		}
	}
	if narrow.Agent != "ask-expert" || !slices.Equal(narrow.Answers, []string{"q"}) {
		t.Fatalf("the sub-agent's context is not labelled: %+v", narrow)
	}
}

// A sub-agent given a list of tools is offered only those, and the log
// records the offer, so replay rebuilds the same request.
func TestSubAgentIsOfferedOnlyItsTools(t *testing.T) {
	r := strategyRig(t, `
(define-agent-tool "ask-expert" "Ask an expert who can read files."
  '(("question" "string" "what to ask"))
  (lambda (call args)
    (sub-agent "You are an expert." (assoc-ref args "question") '("fs-read" "fs-list"))))`, 10,
		fake.Turn{ToolCalls: []fake.ToolCall{{ID: "q", Name: "ask-expert", Args: map[string]any{"question": "What is in main.go?"}}}},
		fake.Turn{Content: "It only declares package main."},
		fake.Turn{Content: "done"},
	)
	if err := r.turn("what does main.go do?"); err != "" {
		t.Fatal(err)
	}
	names := func(i int) []string {
		var out []string
		for _, raw := range r.srv.Requests[i].Tools {
			var tool struct {
				Function struct{ Name string } `json:"function"`
			}
			if err := json.Unmarshal(raw, &tool); err != nil {
				t.Fatal(err)
			}
			out = append(out, tool.Function.Name)
		}
		return out
	}
	if main := names(0); len(main) <= 2 || !slices.Contains(main, "ask-expert") {
		t.Fatalf("the model's own request offered %v", main)
	}
	if expert := names(1); !slices.Equal(expert, []string{"fs-read", "fs-list"}) && !slices.Equal(expert, []string{"fs-list", "fs-read"}) {
		t.Fatalf("the expert was offered %v, want fs-read and fs-list", expert)
	}
	evs, err := log.Read(bytes.NewReader(r.logb.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	for _, ev := range evs {
		if ev.Kind != log.KindLLMReq {
			continue
		}
		req, err := window.ReplayRequest(evs, ev.Seq)
		if err != nil {
			t.Fatalf("replay of seq %d: %v", ev.Seq, err)
		}
		if n := len(req.Tools); n == 2 {
			return // the expert's request replays with its two tools
		}
	}
	t.Fatal("no replayed request offered the expert's two tools")
}

// A tools option naming a tool that does not exist is an error that lists
// the tools there are.
func TestModelTurnRejectsUnknownToolNames(t *testing.T) {
	r := strategyRig(t, `(define (agent-turn request) (model-turn "control" '((tools "no-such-tool"))))`, 10,
		fake.Turn{Content: "done"})
	if err := r.turn("hello"); !strings.Contains(err, `no tool named "no-such-tool"`) || !strings.Contains(err, "fs-read") {
		t.Fatalf("error %q", err)
	}
}

// A sub-agent that repeats the same call is skipped once, then asked for its
// answer without tools, before the core's progress stop would end the whole
// turn.
func TestSubAgentRepeatingItselfEndsOnlyItself(t *testing.T) {
	read := func(id string) fake.Turn { return fake.Turn{ToolCalls: []fake.ToolCall{readCall(id)}} }
	r := strategyRig(t, `
(define-agent-tool "ask-expert" "Ask an expert who can read files."
  '(("question" "string" "what to ask"))
  (lambda (call args)
    (sub-agent "You are an expert." (assoc-ref args "question") '("fs-read"))))`, 10,
		fake.Turn{ToolCalls: []fake.ToolCall{{ID: "q", Name: "ask-expert", Args: map[string]any{"question": "What is in main.go?"}}}},
		read("r1"), read("r2"), // run (results record only failures)
		read("r3"), // skipped: the result is above
		read("r4"), // still repeating: the expert is asked for its answer
		fake.Turn{Content: "It declares package main."},
		fake.Turn{Content: "done"},
	)
	if err := r.turn("what does main.go do?"); err != "" {
		t.Fatal(err)
	}
	if len(r.srv.Requests) != 7 {
		t.Fatalf("%d requests, want 7", len(r.srv.Requests))
	}
	if len(r.srv.Requests[5].Tools) != 0 || !strings.Contains(lastMessage(r, 5), "no tool rounds left") {
		t.Fatalf("the wrap-up request: %d tools, last message %q", len(r.srv.Requests[5].Tools), lastMessage(r, 5))
	}
	results := r.results()
	if results["r1"] != "" || results["r2"] != "" || !strings.Contains(results["r3"], "you already ran this exact call") ||
		!strings.Contains(results["r4"], "stopped for repeating") {
		t.Fatalf("the expert's calls: %+v", results)
	}
	main := requestMessages(t, r.srv.Requests[6])
	if answer := main[len(main)-1]; answer.Role != "tool" || !strings.HasPrefix(answer.Content, "It declares package main.") ||
		!strings.Contains(answer.Content, "stopped for repeating the same call") {
		t.Fatalf("the model's answer from the expert: %+v", answer)
	}
}

// A sub-agent out of rounds is asked, without tools, for what it found, so
// its work is not lost with its last tool calls.
func TestSubAgentOutOfRoundsStillAnswers(t *testing.T) {
	r := strategyRig(t, `
(define sub-agent-max-rounds 1)
(define-agent-tool "ask-expert" "Ask an expert who can read files."
  '(("question" "string" "what to ask"))
  (lambda (call args)
    (sub-agent "You are an expert." (assoc-ref args "question") '("fs-read" "fs-list"))))`, 10,
		fake.Turn{ToolCalls: []fake.ToolCall{{ID: "q", Name: "ask-expert", Args: map[string]any{"question": "What is in main.go?"}}}},
		fake.Turn{ToolCalls: []fake.ToolCall{readCall("r1")}},
		fake.Turn{ToolCalls: []fake.ToolCall{{ID: "l", Name: "fs-list", Args: map[string]any{"path": "."}}}}, // out of rounds
		fake.Turn{Content: "main.go declares package main; I did not get to list the directory."},
		fake.Turn{Content: "done"},
	)
	if err := r.turn("what does main.go do?"); err != "" {
		t.Fatal(err)
	}
	if len(r.srv.Requests) != 5 {
		t.Fatalf("%d requests, want 5", len(r.srv.Requests))
	}
	if results := r.results(); !strings.Contains(results["l"], "out of rounds") {
		t.Fatalf("the expert's last call: %+v", results)
	}
	if len(r.srv.Requests[3].Tools) != 0 || !strings.Contains(lastMessage(r, 3), "no tool rounds left") {
		t.Fatalf("the wrap-up request: %d tools, last message %q", len(r.srv.Requests[3].Tools), lastMessage(r, 3))
	}
	if got := lastMessage(r, 4); got != "main.go declares package main; I did not get to list the directory." {
		t.Fatalf("the model got %q from the expert", got)
	}
}

// A turn that ends inside a sub-agent's context folds it, so the call that
// waits for it is answered right after the call and the transcript stays
// well-formed for the next turn.
func TestTurnEndFoldsContextsWithWaitingCalls(t *testing.T) {
	r := strategyRig(t, `
(define (agent-turn request)
  (if (equal? request "start")
      (begin (model-turn)
             (narrow-context "side")
             (add-message "side work")
             (model-turn))
      (model-turn)))`, 10,
		fake.Turn{ToolCalls: []fake.ToolCall{readCall("a")}},
		fake.Turn{Content: "side answer"},
		fake.Turn{Content: "fine"},
	)
	if err := r.turn("start"); err != "" {
		t.Fatal(err)
	}
	if !strings.Contains(r.results()["a"], "the sub-agent answering this call finished") {
		t.Fatalf("the waiting call: %q", r.results()["a"])
	}
	if err := r.turn("next"); err != "" {
		t.Fatal(err)
	}
	next := requestMessages(t, r.srv.Requests[2])
	for i, m := range next {
		if m.Content == "side work" || m.Content == "side answer" {
			t.Fatalf("the folded context is in the next turn: %+v", next)
		}
		if m.Role == "assistant" && i+1 < len(next) && next[i+1].Role != "tool" && strings.Contains(string(r.srv.Requests[2].Raw), `"a"`) {
			t.Fatalf("a call is not followed by its result: %+v", next)
		}
	}
}
