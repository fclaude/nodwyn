package window

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/fclaude/nodwyn/internal/llm"
	"github.com/fclaude/nodwyn/internal/log"
)

// checkToolPairs models the strict history validation used by Cerebras:
// every call has one adjacent result, and no result is left without a call.
func checkToolPairs(messages []llm.Message) error {
	pending := map[string]bool{}
	for i, m := range messages {
		if m.Role == llm.RoleTool {
			if !pending[m.ToolCallID] {
				return fmt.Errorf("message %d: orphan tool result %q", i, m.ToolCallID)
			}
			delete(pending, m.ToolCallID)
			continue
		}
		if len(pending) != 0 {
			return fmt.Errorf("message %d interrupts unanswered calls %v", i, pending)
		}
		for _, call := range m.ToolCalls {
			pending[call.ID] = true
		}
	}
	if len(pending) != 0 {
		return fmt.Errorf("unanswered calls %v", pending)
	}
	return nil
}

// appendHistory fails setup immediately if the fixture could not be recorded.
func appendHistory(t *testing.T, s *session, kind log.Kind, payload any) log.Event {
	t.Helper()
	e, err := s.w.Append(kind, payload)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func TestTrimmedToolCallsKeepTheirResultsPaired(t *testing.T) {
	s := newSession()
	for i := range 4 {
		appendHistory(t, s, log.KindUserMsg, log.UserMsg{Text: "continue"})
		thought := strings.Repeat("think ", 1000)
		if i == 3 {
			thought = "done"
		}
		appendHistory(t, s, log.KindLLMResp, log.LLMResp{Thinking: thought, ToolCalls: []log.ToolCall{
			{ID: "a", Name: "fs-read", Args: json.RawMessage(`{"path":"a"}`)},
			{ID: "b", Name: "fs-read", Args: json.RawMessage(`{"path":"b"}`)},
		}})
		// IDs may be reused by a model in later batches. Results belong
		// only to the most recent call, including results arriving reversed.
		for _, id := range []string{"b", "a"} {
			appendHistory(t, s, log.KindToolResult, log.ToolResult{ID: id, Name: "fs-read", OK: i%2 == 0, Value: "body", Error: "not executed"})
		}
		appendHistory(t, s, log.KindLLMResp, log.LLMResp{Content: "done"})
	}
	p := current(Policy{HighWater: 3000, LowWater: 1000, MaxResultTokens: 100})
	r := s.build(t, p)
	if !r.Trimmed || len(r.Elided) == 0 {
		t.Fatal("fixture did not trim")
	}
	if err := checkToolPairs(r.Messages); err != nil {
		t.Fatal(err)
	}
	if r.EstTokens > p.LowWater {
		t.Fatalf("trim did not reach low water: %d", r.EstTokens)
	}
	// Rebuilding with room must neither resurrect a removed result nor
	// change the prefix, and the estimate counts only emitted messages.
	p.HighWater, p.LowWater = 100000, 50000
	later := s.build(t, p)
	if !reflect.DeepEqual(r.Messages, later.Messages) {
		t.Fatal("sticky trimming changed the history")
	}
	want := 10
	for _, m := range r.Messages {
		if m.Role == llm.RoleSystem {
			want += Estimate(m.Content)
		} else {
			want += estimate(m)
		}
	}
	if r.EstTokens != want {
		t.Fatalf("estimate %d, emitted messages %d", r.EstTokens, want)
	}
}

func TestProtectedToolResultKeepsItsWholeCallBatch(t *testing.T) {
	s := newSession()
	appendHistory(t, s, log.KindUserMsg, log.UserMsg{Text: "read them"})
	var calls []log.ToolCall
	for i := range 6 {
		calls = append(calls, log.ToolCall{ID: fmt.Sprint(i), Name: "fs-read", Args: json.RawMessage(`{"path":"x"}`)})
	}
	appendHistory(t, s, log.KindLLMResp, log.LLMResp{Thinking: strings.Repeat("think ", 1000), ToolCalls: calls})
	for _, c := range calls {
		appendHistory(t, s, log.KindToolResult, log.ToolResult{ID: c.ID, Name: c.Name, OK: true, Value: "body"})
	}
	r := Build(s.events(t), "sys", 0, current(Policy{HighWater: 1000, LowWater: 500, MaxResultTokens: 100}))
	if !r.Trimmed {
		t.Fatal("fixture did not trim")
	}
	if err := checkToolPairs(r.Messages); err != nil {
		t.Fatal(err)
	}
	if len(r.Messages[2].ToolCalls) != len(calls) {
		t.Fatal("a protected result lost its call batch")
	}
	for _, m := range r.Messages[len(r.Messages)-Keep:] {
		if m.Content != "body" {
			t.Fatalf("protected result changed: %+v", m)
		}
	}
}

// A stopped v15 session already recorded sticky elisions that orphaned
// results. Resuming uses the new pairing rule, while its past requests must
// still replay exactly as sent, including their old malformed history.
func TestResumeRepairsPreviouslyElidedToolPairs(t *testing.T) {
	s := newSession()
	p := Policy{HighWater: 1000, LowWater: 500, MaxResultTokens: 100, Thinking: true, InConversation: true}
	recipe := log.WindowContext{Version: "window-v15", System: "sys", Tools: json.RawMessage(`[]`), HighWater: p.HighWater, LowWater: p.LowWater, MaxResultTokens: p.MaxResultTokens}
	appendHistory(t, s, log.KindSessionStart, log.SessionStart{Context: &recipe})
	appendHistory(t, s, log.KindUserMsg, log.UserMsg{Text: "read"})
	for i := range 3 {
		thought := ""
		if i == 0 {
			thought = strings.Repeat("think ", 1000)
		}
		appendHistory(t, s, log.KindLLMResp, log.LLMResp{Thinking: thought, ToolCalls: []log.ToolCall{{ID: fmt.Sprint(i), Name: "fs-read", Args: json.RawMessage(`{}`)}}})
		appendHistory(t, s, log.KindToolResult, log.ToolResult{ID: fmt.Sprint(i), Name: "fs-read", OK: true, Value: "body"})
	}
	old := Build(s.events(t), "sys", Estimate("[]"), p)
	if !old.Trimmed || checkToolPairs(old.Messages) == nil {
		t.Fatal("fixture did not reproduce the old orphaned history")
	}
	record := func(r Result) log.Event {
		req := llm.Request{Messages: r.Messages, Tools: []llm.Tool{}}
		hash, err := RequestHash(req)
		if err != nil {
			t.Fatal(err)
		}
		return appendHistory(t, s, log.KindLLMReq, log.LLMReq{Elided: r.Elided, RequestHash: hash})
	}
	oldRequest := record(old)
	recipe.Version = RecipeVersion
	// No fresh trim is needed: the earlier elisions alone must remove the
	// orphaned results, and remain removed when the context has more room.
	p = current(p)
	p.HighWater, p.LowWater = 100000, 50000
	recipe.HighWater, recipe.LowWater = p.HighWater, p.LowWater
	// The runtime records these marks when it resumes.
	appendHistory(t, s, log.KindSessionResume, log.SessionStart{Context: &recipe})
	now := Build(s.events(t), "sys", Estimate("[]"), p)
	if now.Trimmed {
		t.Fatal("repair depended on a new trim")
	}
	if err := checkToolPairs(now.Messages); err != nil {
		t.Fatal(err)
	}
	newRequest := record(now)
	for _, tc := range []struct {
		seq  uint64
		want []llm.Message
	}{{oldRequest.Seq, old.Messages}, {newRequest.Seq, now.Messages}} {
		got, err := ReplayRequest(s.events(t), tc.seq)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got.Messages, tc.want) {
			t.Fatalf("request %d changed during replay", tc.seq)
		}
	}
}
