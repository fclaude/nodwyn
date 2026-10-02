package plain

import (
	"encoding/json"
	"testing"

	"github.com/fclaude/nodwyn/internal/budget"
	"github.com/fclaude/nodwyn/internal/log"
)

func TestUsageLine(t *testing.T) {
	cached, cost := 9672, 0.0123
	for _, tc := range []struct {
		u    log.Usage
		want string
	}{
		// Ollama: no cache report, no cost, a duration.
		{log.Usage{PromptTokens: 1200, OutputTokens: 30, TotalMS: 2000}, "1,200→30 tok · 2.0s"},
		// A hosted model: hit rate and cost, no duration.
		{log.Usage{PromptTokens: 12400, OutputTokens: 800, CachedTokens: &cached, Cost: &cost}, "12,400→800 tok · 78% cached · $0.012"},
		// An estimate from configured prices is marked.
		{log.Usage{PromptTokens: 100, OutputTokens: 10, Cost: &cost, CostEstimated: true}, "100→10 tok · ≈$0.012"},
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
