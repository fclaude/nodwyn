package window

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/fclaude/nibb/internal/llm"
	"github.com/fclaude/nibb/internal/log"
)

func events(t *testing.T, n int, resultSize int) []log.Event {
	t.Helper()
	var buf bytes.Buffer
	w := log.New(&buf, "s")
	for i := 0; i < n; i++ {
		w.Append(log.KindUserMsg, log.UserMsg{Text: fmt.Sprintf("question %d", i)})
		w.Append(log.KindLLMResp, log.LLMResp{ToolCalls: []log.ToolCall{{ID: fmt.Sprint("c", i), Name: "fs-read", Args: json.RawMessage(`{"path":"x"}`)}}})
		w.Append(log.KindGuardDecision, log.GuardDecision{Prim: "fs-read", Decision: "allow"}) // not a message
		w.Append(log.KindToolResult, log.ToolResult{ID: fmt.Sprint("c", i), Name: "fs-read", OK: true, Value: strings.Repeat("x", resultSize)})
		w.Append(log.KindLLMResp, log.LLMResp{Content: fmt.Sprintf("answer %d", i)})
	}
	evs, err := log.Read(&buf)
	if err != nil {
		t.Fatal(err)
	}
	return evs
}

func TestBuildUnderBudgetKeepsEverything(t *testing.T) {
	r := Build(events(t, 2, 30), "sys", 10, 100000)
	if len(r.Messages) != 1+2*4 || len(r.Elided) != 0 {
		t.Fatalf("messages=%d elided=%v", len(r.Messages), r.Elided)
	}
	if r.Messages[0].Role != llm.RoleSystem || r.Messages[1].Role != llm.RoleUser || r.Messages[2].ToolCalls[0].ID != "c0" || r.Messages[3].Role != llm.RoleTool || r.Messages[3].ToolCallID != "c0" {
		t.Fatalf("shape: %+v", r.Messages)
	}
}

func TestElisionIsInPlaceOldestFirstAndMonotonic(t *testing.T) {
	evs := events(t, 5, 3000) // each tool result ≈ 1000 tokens
	r := Build(evs, "sys", 0, 2600)
	if len(r.Elided) == 0 {
		t.Fatal("expected elision")
	}
	// Oldest tool results go first, and the marker sits where the content was.
	first := r.Elided[0]
	var idx int
	for i, m := range r.Messages {
		if m.Role == llm.RoleTool && m.Content == fmt.Sprintf("[elided seq=%d]", first) {
			idx = i
		}
	}
	if idx == 0 || r.Messages[idx].ToolCallID != "c0" {
		t.Fatalf("first elided should be the oldest tool result: %v", r.Elided)
	}
	if r.EstTokens > 2600 {
		t.Fatalf("still over budget: %d", r.EstTokens)
	}
	// Last Keep entries are untouched.
	n := len(r.Messages)
	for _, m := range r.Messages[n-Keep:] {
		if strings.HasPrefix(m.Content, "[elided") {
			t.Fatal("protected tail was elided")
		}
	}
	// Adding events never un-elides: the earlier prefix is identical.
	more := append(evs, events(t, 1, 3000)...)
	for i := range more[len(evs):] {
		more[len(evs)+i].Seq = uint64(len(evs) + i + 1)
	}
	r2 := Build(more, "sys", 0, 2600)
	for i := range r.Messages[:idx+1] {
		if r.Messages[i].Content != r2.Messages[i].Content {
			t.Fatalf("prefix changed at %d: %q vs %q", i, r.Messages[i].Content, r2.Messages[i].Content)
		}
	}
	if len(r2.Elided) < len(r.Elided) {
		t.Fatal("elision must be monotonic")
	}
}

func TestSecondPassElidesTurns(t *testing.T) {
	evs := events(t, 6, 10)
	// Make user messages big so tool elision alone cannot satisfy the budget.
	for i := range evs {
		if evs[i].Kind == log.KindUserMsg {
			evs[i].Payload, _ = json.Marshal(log.UserMsg{Text: strings.Repeat("u", 3000)})
		}
	}
	r := Build(evs, "sys", 0, 2500)
	if r.EstTokens > 2500 {
		t.Fatalf("over budget after both passes: %d", r.EstTokens)
	}
	if !strings.HasPrefix(r.Messages[1].Content, "[elided seq=") {
		t.Fatalf("oldest user turn should be elided: %q", r.Messages[1].Content[:20])
	}
}

func TestErrorResultsBecomeJSON(t *testing.T) {
	var buf bytes.Buffer
	w := log.New(&buf, "s")
	w.Append(log.KindToolResult, log.ToolResult{ID: "c", Name: "fs-read", OK: false, Error: "fs-read: denied: nope"})
	evs, _ := log.Read(&buf)
	r := Build(evs, "sys", 0, 1000)
	if r.Messages[1].Content != `{"error":"fs-read: denied: nope"}` {
		t.Fatalf("%q", r.Messages[1].Content)
	}
}
