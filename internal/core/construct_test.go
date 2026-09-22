package core

import (
	"testing"
	"time"

	"github.com/fclaude/nibb/internal/config"
)

func TestJevOptionsPreserveDefaults(t *testing.T) {
	for _, timeout := range []string{"", "invalid"} {
		got := jevOptions(config.Jev{URL: "https://judge.example", Model: "test-model", Timeout: timeout})
		if got.URL != "https://judge.example" || got.Model != "test-model" || got.Content != "plan" || got.Timeout != 10*time.Second {
			t.Fatalf("defaults for %q: %+v", timeout, got)
		}
	}
	// Successfully parsed durations are preserved, including zero.
	for _, timeout := range []time.Duration{0, 3 * time.Second} {
		got := jevOptions(config.Jev{Content: "plan+summaries", Timeout: timeout.String()})
		if got.Content != "plan+summaries" || got.Timeout != timeout {
			t.Fatalf("explicit settings: %+v", got)
		}
	}
}
