package core

import (
	"testing"
	"time"

	"github.com/fclaude/nodwyn/internal/config"
)

func TestJudgeOptionsPreserveDefaults(t *testing.T) {
	t.Parallel()
	servers := map[string]config.Server{"judge": {Provider: "jev", URL: "https://judge.example", Credential: "judge-key"}}
	for _, timeout := range []string{"", "invalid"} {
		got := judgeOptions(config.Config{Servers: servers, Judge: config.Judge{Server: "judge", Model: "test-model", Timeout: timeout}})
		if got.URL != "https://judge.example" || got.Provider != "jev" || got.Class != "judge-key" || got.Model != "test-model" || got.Content != "plan" || got.Timeout != 10*time.Second {
			t.Fatalf("defaults for %q: %+v", timeout, got)
		}
	}
	// Successfully parsed durations are preserved, including zero.
	for _, timeout := range []time.Duration{0, 3 * time.Second} {
		got := judgeOptions(config.Config{Judge: config.Judge{Content: "plan+summaries", Timeout: timeout.String()}})
		if got.Content != "plan+summaries" || got.Timeout != timeout {
			t.Fatalf("explicit settings: %+v", got)
		}
	}
}

// A judge whose [judge] sets no prices is priced at what its provider
// publishes for a judge's question, which for OpenAI is the Decisions API's
// input price and no output price, not gpt-6-luna's chat prices; its model
// is its provider's judge model when [judge] names none.
func TestJudgeOptionsTakePublishedPrices(t *testing.T) {
	t.Parallel()
	cfg := config.Default()
	cfg.Servers = map[string]config.Server{"openai-api": {Provider: "openai", URL: "https://api.openai.com/v1", Credential: "model-api-openai-com"}}
	cfg.Judge.Server = "openai-api"
	got := judgeOptions(cfg)
	if got.Provider != "openai" || got.Model != "gpt-6-luna" || got.Class != "model-api-openai-com" {
		t.Fatalf("judge on openai: %+v", got)
	}
	if got.PriceInput == nil || *got.PriceInput != 0.10 || got.PriceOutput == nil || *got.PriceOutput != 0 {
		t.Fatalf("prices: %v %v", got.PriceInput, got.PriceOutput)
	}
}
