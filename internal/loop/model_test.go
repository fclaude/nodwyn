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
