package loop

import (
	"testing"

	"github.com/fclaude/nodwyn/internal/llm"
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
