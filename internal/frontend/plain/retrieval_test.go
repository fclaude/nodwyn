package plain

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/fclaude/nodwyn/internal/event"
	"github.com/fclaude/nodwyn/internal/log"
)

// A search reads as what it searched and where, an index build as one line
// naming what it read and left out, and a file it left out without asking
// is that line's, not a guard line of its own: live and in a replay (ADR 50).
func TestRetrievalLinesLiveAndReplayed(t *testing.T) {
	var history bytes.Buffer
	w := log.New(&history, "retrieval")
	call := log.ToolCall{ID: "s", Name: "index-search", Args: json.RawMessage(`{"query":"binding check","scope":"repo"}`)}
	w.Append(log.KindLLMResp, log.LLMResp{ToolCalls: []log.ToolCall{call}})
	refused := log.GuardDecision{Prim: "fs-read", Origin: "model", Decision: "confirm", Answer: "no", Actor: "not asked", Reason: "user checker secrets"}
	w.Append(log.KindGuardDecision, refused)
	update := log.IndexUpdate{Files: 4, Read: 4, Skipped: 1, Refused: []string{"key.txt"}, Millis: 300}
	w.Append(log.KindIndexUpdate, update)
	events, err := log.Read(&history)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	Replay(events, &out, false)
	const line = "[index updated: read 4 files · 1 skipped · left out by the guard: key.txt · holds 4 files · 0.3 s]"
	got := out.String()
	if !strings.Contains(got, "▸ index-search “binding check” in the repository") || !strings.Contains(got, line) || strings.Contains(got, "user checker secrets") {
		t.Fatalf("replay:\n%s", got)
	}

	out.Reset()
	c := &Chat{Out: &out}
	c.show(event.FromLog(log.Event{Kind: log.KindToolCall}, log.ToolCallEvent{ToolCall: call, Origin: "model"}))
	c.show(event.FromLog(log.Event{Kind: log.KindGuardDecision}, refused))
	c.show(event.FromLog(log.Event{Kind: log.KindIndexUpdate}, update))
	got = out.String()
	if !strings.Contains(got, "▸ index-search “binding check” in the repository") || !strings.Contains(got, line) || strings.Contains(got, "user checker secrets") {
		t.Fatalf("live:\n%s", got)
	}
}
