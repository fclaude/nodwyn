package plain

import (
	"bytes"
	"github.com/fclaude/nodwyn/internal/budget"
	"github.com/fclaude/nodwyn/internal/log"
	"strings"
	"testing"

	"github.com/fclaude/nodwyn/internal/event"
)

func TestThinkingPrefixedPerLineNotPerFragment(t *testing.T) {
	var out bytes.Buffer
	c := &Chat{Out: &out, ShowThinking: true}
	for _, frag := range []string{"let me", " think\nabout", " it", "\n"} {
		c.show(event.Event{Kind: event.StreamThinking, Payload: event.Text{Text: frag}})
	}
	for _, frag := range []string{"The ans", "wer."} {
		c.show(event.Event{Kind: event.StreamText, Payload: event.Text{Text: frag}})
	}
	c.show(event.Event{Kind: event.Status, Payload: event.StatusInfo{State: "idle"}})
	want := "· let me think\n· about it\nThe answer.\n"
	if out.String() != want {
		t.Fatalf("got %q\nwant %q", out.String(), want)
	}
}

func TestChatStreamsAreIndependent(t *testing.T) {
	var first, second bytes.Buffer
	a := &Chat{Out: &first, ShowThinking: true}
	b := &Chat{Out: &second, ShowThinking: true}
	a.writeStream("thinking", "first")
	b.writeStream("thinking", "second")
	a.writeStream("thinking", " continued")
	b.show(event.Event{Kind: event.Status, Payload: event.StatusInfo{State: "idle"}})
	a.writeStream("text", "answer")
	a.show(event.Event{Kind: event.Status, Payload: event.StatusInfo{State: "idle"}})
	if got := first.String(); got != "· first continued\nanswer\n" {
		t.Fatalf("first stream: %q", got)
	}
	if got := second.String(); got != "· second\n" {
		t.Fatalf("second stream: %q", got)
	}
}

func TestCheckpointReplayDoesNotPrompt(t *testing.T) {
	var history bytes.Buffer
	w := log.New(&history, "checkpoint")
	_, err := w.Append(log.KindTurnCheckpoint, log.TurnCheckpoint{Checkpoint: budget.Checkpoint{Dimension: "tool_calls", Used: 50, Limit: budget.Limit{Soft: 50, Hard: 500}}, Choice: budget.Choice{Continue: true, Soft: 100}})
	if err != nil {
		t.Fatal(err)
	}
	events, err := log.Read(&history)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	Replay(events, &out, false)
	if !strings.Contains(out.String(), "continue=true, soft=100") || strings.Contains(out.String(), "continue?") {
		t.Fatal(out.String())
	}
}

func TestRPCReplayShowsEvaluationAndApprovalWithoutExecuting(t *testing.T) {
	var history bytes.Buffer
	w := log.New(&history, "rpc")
	w.Append(log.KindEvalReq, log.EvalRequest{Source: "(+ 1 2)", Actor: "unix:uid=1,gid=1,pid=2"})
	w.Append(log.KindEvalResult, log.EvalResult{Value: "3"})
	w.Append(log.KindApprovalChange, log.ApprovalChange{Actor: "unix:uid=1,gid=1,pid=2", Scope: "session", Kind: "write", Prefix: "notes/"})
	events, err := log.Read(&history)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	Replay(events, &out, false)
	for _, want := range []string{"scheme> (+ 1 2)\n3\n", "approval session write notes/ by unix:uid=1,gid=1,pid=2"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("missing %q in %q", want, out.String())
		}
	}
}
