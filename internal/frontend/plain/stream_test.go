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

// A contained server's refused attempts are shown; the routine ones stay in
// the log.
func TestReplayShowsRefusedEgress(t *testing.T) {
	var history bytes.Buffer
	w := log.New(&history, "egress")
	w.Append(log.KindMCPEgress, log.MCPEgress{Server: "notes", Method: "CONNECT", Host: "api.notes.example", Port: 443, Allowed: true})
	w.Append(log.KindMCPEgress, log.MCPEgress{Server: "notes", Method: "CONNECT", Host: "evil.example.net", Port: 443, Reason: "not on the server's network list"})
	w.Append(log.KindMCPEgress, log.MCPEgress{Server: "notes", Dropped: 12})
	events, err := log.Read(&history)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	Replay(events, &out, false)
	got := out.String()
	if strings.Contains(got, "api.notes.example") || !strings.Contains(got, "notes could not reach evil.example.net:443: not on the server's network list") || !strings.Contains(got, "12 more connection attempts") {
		t.Fatal(got)
	}
}

// A turn that stopped short says where it had got to, after the error; one
// that completed says nothing more.
func TestStoppedTurnPrintsWhereItStopped(t *testing.T) {
	var out bytes.Buffer
	c := &Chat{Out: &out}
	c.show(event.Event{Kind: event.Error, Payload: event.ErrorInfo{Err: "hard stop: tool_calls budget reached"}})
	c.show(event.Event{Kind: string(log.KindTurnEnd), Payload: log.TurnEnd{Text: "turn ended", Outcome: "hard_budget", Where: "plan: 1 of 4 steps marked; last command: go test ./... (exited 1)"}})
	c.show(event.Event{Kind: string(log.KindTurnEnd), Payload: log.TurnEnd{Text: "turn ended", Outcome: "completed"}})
	want := "error: hard stop: tool_calls budget reached\nwhere it stopped: plan: 1 of 4 steps marked; last command: go test ./... (exited 1)\n"
	if out.String() != want {
		t.Fatalf("got %q\nwant %q", out.String(), want)
	}
}

// A reply, a command's output or a file name is the model's or a
// repository's, and printed raw an escape sequence in it acts on the
// terminal: line mode and replay leave the controls out.
func TestLineModeLeavesTerminalControlsOut(t *testing.T) {
	hostile := "done\x1b]0;owned\x07\x1b[2J\rhidden\tkept\n"
	var out bytes.Buffer
	c := &Chat{Out: TerminalText(&out)}
	c.show(event.Event{Kind: event.StreamText, Payload: event.Text{Text: hostile}})
	c.show(event.Event{Kind: event.Status, Payload: event.StatusInfo{State: "idle"}})
	if strings.ContainsAny(out.String(), "\x1b\x07\r") || !strings.Contains(out.String(), "hidden\tkept") {
		t.Fatalf("line mode: %q", out.String())
	}
}

// nodwyn replay prints a log the same way.
func TestReplayLeavesTerminalControlsOut(t *testing.T) {
	hostile := "done\x1b]0;owned\x07\x1b[2J\rhidden\tkept\n"
	var history bytes.Buffer
	w := log.New(&history, "s")
	if _, err := w.Append(log.KindLLMResp, log.LLMResp{Content: hostile}); err != nil {
		t.Fatal(err)
	}
	events, err := log.Read(&history)
	if err != nil {
		t.Fatal(err)
	}
	var replayed bytes.Buffer
	Replay(events, &replayed, false)
	if strings.ContainsAny(replayed.String(), "\x1b\x07\r") || !strings.Contains(replayed.String(), "done") {
		t.Fatalf("replay: %q", replayed.String())
	}
}

// :log with a count that is not one from 1 says how it is used; a negative
// one sliced past the end and the panic ended line mode with no session.end.
func TestLogCommandRefusesABadCount(t *testing.T) {
	var out bytes.Buffer
	c := &Chat{Out: &out}
	for _, line := range []string{":log -1", ":log x", ":log 0"} {
		out.Reset()
		if quit := c.command(line); quit || !strings.Contains(out.String(), "usage: :log") {
			t.Errorf("%s: %q", line, out.String())
		}
	}
}
