package tui

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/fclaude/nodwyn/internal/log"
)

// The gauge follows the context the model holds: the explorer's and the
// planner's requests while their narrowed contexts are open, named by their
// agent (session 20261005T060224Z-2f9eb922 showed nothing through 830
// records of exploring), and the conversation's once they fold; a
// compaction sets it to what the conversation became.
func TestContextGauge(t *testing.T) {
	var g contextGauge
	feed := func(kind log.Kind, p any) {
		raw, _ := json.Marshal(p)
		g.observe(string(kind), raw)
	}
	feed(log.KindSessionStart, log.SessionStart{Context: &log.WindowContext{HighWater: 61440}})
	feed(log.KindUserMsg, log.UserMsg{Text: "go"})
	feed(log.KindContextNarrow, log.ContextNarrow{System: "You explore.", Agent: "explorer"})
	if got := g.title(); got != "" {
		t.Fatalf("before any request %q", got)
	}
	feed(log.KindLLMReq, log.LLMReq{EstTokens: 52000})
	feed(log.KindLLMResp, log.LLMResp{Usage: log.Usage{PromptTokens: 39000}})
	if got := g.title(); got != "context ~52k of ~61k (explorer)" {
		t.Fatalf("while the explorer asks %q", got)
	}
	if r := g.report(); !strings.Contains(r, "the explorer's latest request") || !strings.Contains(r, "counted 39,000") {
		t.Fatalf("report while exploring:\n%s", r)
	}
	feed(log.KindContextFold, log.ContextFold{})
	if got := g.title(); got != "" {
		t.Fatalf("folded before the conversation asked %q", got)
	}
	feed(log.KindLLMReq, log.LLMReq{EstTokens: 45200})
	feed(log.KindLLMResp, log.LLMResp{Usage: log.Usage{PromptTokens: 38000}})
	feed(log.KindContextNarrow, log.ContextNarrow{System: "You plan.", Agent: "planner"})
	feed(log.KindLLMReq, log.LLMReq{EstTokens: 3000})
	feed(log.KindLLMResp, log.LLMResp{Usage: log.Usage{PromptTokens: 2500}})
	if got := g.title(); got != "context ~3k of ~61k (planner)" {
		t.Fatalf("while the planner asks %q", got)
	}
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
	// Moved to another model, the conversation is trimmed by the window
	// sized for that model (ADR 40).
	feed(log.KindSessionModel, log.SessionModel{Model: "long", Context: &log.WindowContext{HighWater: 86016}})
	if got := g.title(); got != "context ~4k of ~86k" {
		t.Fatalf("after a switch %q", got)
	}
}
