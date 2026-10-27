package plain

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/fclaude/nodwyn/internal/budget"
	"github.com/fclaude/nodwyn/internal/log"
)

func TestUsageLine(t *testing.T) {
	cached, cost, est := 9672, 0.0123, 900
	for _, tc := range []struct {
		u    log.Usage
		want string
	}{
		// Ollama: no cache report, no cost, a duration.
		{log.Usage{PromptTokens: 1200, OutputTokens: 30, TotalMS: 2000}, "1,200→30 tok · 2.0s"},
		// A provider that reports no cache count: the estimate from its
		// prompt-eval time is marked ≈.
		{log.Usage{PromptTokens: 1200, OutputTokens: 30, TotalMS: 2000, EstimatedCachedTokens: &est}, "1,200→30 tok · ≈75% cached · 2.0s"},
		// A reported share wins over the estimate.
		{log.Usage{PromptTokens: 12400, OutputTokens: 800, TotalMS: 2000, CachedTokens: &cached, EstimatedCachedTokens: &est}, "12,400→800 tok · 78% cached · 2.0s"},
		// A hosted model: hit rate and cost, no duration.
		{log.Usage{PromptTokens: 12400, OutputTokens: 800, CachedTokens: &cached, Cost: &cost}, "12,400→800 tok · 78% cached · $0.012"},
		// An estimate from configured prices is marked.
		{log.Usage{PromptTokens: 100, OutputTokens: 10, Cost: &cost, CostEstimated: true}, "100→10 tok · ≈$0.012"},
		// The request's wall time gives every provider a tok/s, beside
		// whatever the provider itself reported.
		{log.Usage{PromptTokens: 1000, OutputTokens: 44, WallMS: 2000}, "1,000→44 tok · 22.0 tok/s"},
		{log.Usage{PromptTokens: 12400, OutputTokens: 800, WallMS: 4000, TotalMS: 3900, Cost: &cost}, "12,400→800 tok · $0.012 · 200.0 tok/s · 3.9s"},
		// No tokens reported, no speed to read.
		{log.Usage{Unreported: true, WallMS: 2000}, "tokens not reported"},
	} {
		if got := UsageLine(tc.u); got != tc.want {
			t.Errorf("got %q, want %q", got, tc.want)
		}
	}
	// An older log's usage has none of the new fields: unknown, as before.
	var old log.LLMResp
	json.Unmarshal([]byte(`{"content":"x","usage":{"prompt_tokens":10,"output_tokens":2,"total_ms":0}}`), &old)
	if got := UsageLine(old.Usage); got != "10→2 tok" {
		t.Fatalf("old log: %q", got)
	}
}

func TestSpendSoFar(t *testing.T) {
	if SpendSoFar(budget.State{Unpriced: 3}) != "" {
		t.Fatal("no known cost shows nothing")
	}
	if got := SpendSoFar(budget.State{Cost: 0.25, Priced: 2}); got != "$0.250 so far" {
		t.Fatal(got)
	}
	if got := SpendSoFar(budget.State{Cost: 0.25, Priced: 2, Unpriced: 1}); got != "at least $0.250 so far" {
		t.Fatal(got)
	}
}

func TestUsageLineReasoningAndUnreported(t *testing.T) {
	if got := UsageLine(log.Usage{PromptTokens: 1200, OutputTokens: 400, ReasoningTokens: 300}); got != "1,200→400 tok (300 reasoning)" {
		t.Fatalf("reasoning: %q", got)
	}
	if got := UsageLine(log.Usage{Unreported: true}); got != "tokens not reported" {
		t.Fatalf("unreported: %q", got)
	}
}

func TestCompactLine(t *testing.T) {
	auto := log.ContextCompact{Trigger: "auto", Compacted: true, Before: 59210, After: 4100, Threshold: 58983, NoteTokens: 812}
	if got := CompactLine(auto); got != "── compacted (past ~58,983 tokens): context ~59,210 → ~4,100 tokens, handover note 812 tokens ──" {
		t.Fatalf("auto: %q", got)
	}
	cut := log.ContextCompact{Trigger: "command", Compacted: true, Before: 20000, After: 3000, NoteTokens: 4096, Truncated: true}
	if got := CompactLine(cut); !strings.Contains(got, "(/compact)") || !strings.Contains(got, "cut short at the output limit") {
		t.Fatalf("command: %q", got)
	}
	gave := log.ContextCompact{Trigger: "requested", Before: 30000}
	if got := CompactLine(gave); !strings.HasPrefix(got, "⚑ compaction (asked for by the strategy) gave up") || !strings.Contains(got, "~30,000 tokens") {
		t.Fatalf("gave up: %q", got)
	}
}

func TestJudgeLineAndTally(t *testing.T) {
	cost := 0.00022
	priced := log.JevReq{Tool: "judge-choice", OK: true, Usage: &log.JevUsage{InputTokens: 318, OutputTokens: 34, Cost: &cost}}
	if got := JudgeLine(priced); got != "[judge choice: 318→34 tok · ≈$0.0002]" {
		t.Fatalf("priced: %q", got)
	}
	bare := log.JevReq{Tool: "judge-yes-no", OK: true, Usage: &log.JevUsage{InputTokens: 296, OutputTokens: 20}}
	if got := JudgeLine(bare); got != "[judge yes-no: 296→20 tok]" {
		t.Fatalf("unpriced: %q", got)
	}
	failed := log.JevReq{Tool: "judge-yes-no", Error: "401 Unauthorized"}
	if got := JudgeLine(failed); got != "[judge yes-no failed: 401 Unauthorized]" {
		t.Fatalf("failed: %q", got)
	}
	// A log from before the judge's primitives were renamed shows its names.
	if got := JudgeLine(log.JevReq{Tool: "jev-noul", Error: "401 Unauthorized"}); got != "[judge jev-noul failed: 401 Unauthorized]" {
		t.Fatalf("an older log: %q", got)
	}
	var j JudgeTally
	j.Add(priced)
	if got := j.String(); got != "1 judge call, 352 tokens, ≈$0.0002" {
		t.Fatalf("one: %q", got)
	}
	j.Add(bare)
	j.Add(failed)
	if got := j.String(); got != "3 judge calls, 668 tokens, at least ≈$0.0002" {
		t.Fatalf("mixed: %q", got)
	}
	var none JudgeTally
	none.Add(bare)
	if got := none.String(); got != "1 judge call, 316 tokens, not priced ([judge] price_input, price_output)" {
		t.Fatalf("unpriced: %q", got)
	}
	if JudgeSoFar(budget.State{}) != "" {
		t.Fatal("a turn without the judge mentions it")
	}
}
