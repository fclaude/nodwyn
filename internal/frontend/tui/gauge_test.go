package tui

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/fclaude/nodwyn/internal/log"
)

// The gauge follows the conversation's own requests: a planner's request in
// a narrowed context does not change it, and a compaction sets it to what
// the context became.
func TestContextGauge(t *testing.T) {
	var g contextGauge
	feed := func(kind log.Kind, p any) {
		raw, _ := json.Marshal(p)
		g.observe(string(kind), raw)
	}
	feed(log.KindSessionStart, log.SessionStart{Context: &log.WindowContext{HighWater: 61440}})
	feed(log.KindUserMsg, log.UserMsg{Text: "go"})
	feed(log.KindLLMReq, log.LLMReq{EstTokens: 45200})
	feed(log.KindLLMResp, log.LLMResp{Usage: log.Usage{PromptTokens: 38000}})
	feed(log.KindContextNarrow, log.ContextNarrow{System: "You plan.", Agent: "planner"})
	feed(log.KindLLMReq, log.LLMReq{EstTokens: 3000})
	feed(log.KindLLMResp, log.LLMResp{Usage: log.Usage{PromptTokens: 2500}})
	feed(log.KindContextFold, log.ContextFold{})
	if got := g.title(); got != "context ~45k of ~61k" {
		t.Fatalf("title %q", got)
	}
	if r := g.report(); !strings.Contains(r, "~45,200 tokens") || !strings.Contains(r, "counted 38,000") || !strings.Contains(r, "No compaction") {
		t.Fatalf("report:\n%s", r)
	}
	feed(log.KindContextCompact, log.ContextCompact{Trigger: "auto", Compacted: true, Before: 59000, After: 4100, Threshold: 58983, NoteTokens: 900})
	if got := g.title(); got != "context ~4k of ~61k" {
		t.Fatalf("after compaction %q", got)
	}
	if r := g.report(); !strings.Contains(r, "Compactions in this session: 1") || !strings.Contains(r, "~59,000 → ~4,100") {
		t.Fatalf("report:\n%s", r)
	}
}
