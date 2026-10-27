package loop

import (
	"testing"

	"github.com/fclaude/nodwyn/internal/llm"
	"github.com/fclaude/nodwyn/internal/log"
)

// responseRecord carries the request's host-measured wall time into the
// llm.resp payload: it is what a session's tok/s adds up, and the one
// timing a provider that reports none of its own still gets.
func TestResponseRecordCarriesWallMS(t *testing.T) {
	rec := responseRecord(&llm.Response{
		Message: llm.Message{Role: llm.RoleAssistant, Content: "ok"},
		Usage:   llm.Usage{PromptTokens: 10, OutputTokens: 44, WallMS: 1234},
	}, llm.Rates{})
	if rec.Usage.WallMS != 1234 || rec.Usage.OutputTokens != 44 {
		t.Fatalf("usage %+v", rec.Usage)
	}
	// A request ChatRetrying did not time (an older caller) records none.
	rec = responseRecord(&llm.Response{Message: llm.Message{Content: "ok"}}, llm.Rates{})
	if rec.Usage.WallMS != 0 {
		t.Fatalf("wall_ms %d, want it left out", rec.Usage.WallMS)
	}
}

// A reply's signed reasoning is logged with it, as the provider gave it,
// for the window to give back (recipe v18).
func TestResponseRecordKeepsTheBlocks(t *testing.T) {
	blocks := []llm.ThinkingBlock{{Type: "thinking", Thinking: "plan", Signature: "sig"}, {Type: "redacted_thinking", Data: "opaque", Before: 1}}
	rec := responseRecord(&llm.Response{Message: llm.Message{Blocks: blocks}}, llm.Rates{})
	if len(rec.Blocks) != 2 || llm.ThinkingBlock(rec.Blocks[0]) != blocks[0] || llm.ThinkingBlock(rec.Blocks[1]) != blocks[1] {
		t.Fatalf("logged blocks: %+v", rec.Blocks)
	}
}

// estimateCache is the cache share a provider that reports none (Ollama)
// still shows: the prompt-eval time a cached token does not take, against
// the session's slowest of the model's — the most-cold request. A reply
// that sets the reference gets no estimate, and a provider-reported share
// always wins.
func TestEstimateCache(t *testing.T) {
	l := &Loop{}
	// No signal: a reported share, no prompt tokens, no prompt-eval time.
	cached := 100
	if got := l.estimateCache("m", log.Usage{PromptTokens: 1000, PromptEvalMS: 5000, CachedTokens: &cached}); got != nil {
		t.Fatalf("a provider-reported share wins: got %d", *got)
	}
	if got := l.estimateCache("m", log.Usage{PromptEvalMS: 5000}); got != nil {
		t.Fatalf("no prompt tokens: got %d", *got)
	}
	if got := l.estimateCache("m", log.Usage{PromptTokens: 1000}); got != nil {
		t.Fatalf("no prompt-eval time: got %d", *got)
	}
	// The first reply is the most-cold seen: it sets the reference and gets
	// no estimate of its own.
	if got := l.estimateCache("m", log.Usage{PromptTokens: 1000, PromptEvalMS: 10000}); got != nil {
		t.Fatalf("the coldest reply: got %d, want no estimate", *got)
	}
	// A reply four times faster per token is estimated at 60% cached.
	if got := l.estimateCache("m", log.Usage{PromptTokens: 1000, PromptEvalMS: 4000}); got == nil || *got != 600 {
		t.Fatalf("warm reply: got %v, want 600", got)
	}
	// A slower reply raises the reference and itself gets no estimate.
	if got := l.estimateCache("m", log.Usage{PromptTokens: 1000, PromptEvalMS: 20000}); got != nil {
		t.Fatalf("a colder reply: got %d, want no estimate", *got)
	}
	if got := l.estimateCache("m", log.Usage{PromptTokens: 1000, PromptEvalMS: 4000}); got == nil || *got != 800 {
		t.Fatalf("against the raised reference: got %v, want 800", got)
	}
	// A near-zero prompt-eval is clamped at the reply's own prompt.
	if got := l.estimateCache("m", log.Usage{PromptTokens: 1000, PromptEvalMS: 1}); got == nil || *got != 1000 {
		t.Fatalf("clamped: got %v, want 1000", got)
	}
	// A model's reference is its own: another model starts cold again.
	if got := l.estimateCache("other", log.Usage{PromptTokens: 1000, PromptEvalMS: 4000}); got != nil {
		t.Fatalf("another model's first reply: got %d, want no estimate", *got)
	}
}
