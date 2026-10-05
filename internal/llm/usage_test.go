package llm

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func chatWithUsage(t *testing.T, usage string) (Usage, string) {
	t.Helper()
	var sent string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		sent = string(b)
		w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":` + usage + `}`))
	}))
	defer srv.Close()
	resp, err := NewOpenAI(srv.URL+"/v1", nil).Chat(context.Background(), Request{Model: "m", Messages: []Message{{Role: RoleUser, Content: "hi"}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return resp.Usage, sent
}

// An OpenRouter-shaped usage: cached, cache-write and reasoning tokens and
// the cost are read. Nothing is asked for: OpenRouter reports usage in
// every response, and a strict server refuses unknown parameters.
func TestOpenAIReadsCacheAndCost(t *testing.T) {
	u, sent := chatWithUsage(t, `{"prompt_tokens":12400,"completion_tokens":800,"cost":0.0123,
		"prompt_tokens_details":{"cached_tokens":9672,"cache_write_tokens":40},
		"completion_tokens_details":{"reasoning_tokens":300}}`)
	if u.PromptTokens != 12400 || u.OutputTokens != 800 || u.CachedTokens == nil || *u.CachedTokens != 9672 ||
		u.CacheWriteTokens != 40 || u.ReasoningTokens != 300 || u.Cost == nil || *u.Cost != 0.0123 {
		t.Fatalf("usage %+v", u)
	}
	if strings.Contains(sent, `"usage"`) || strings.Contains(sent, "stream_options") {
		t.Fatalf("the request asked for usage: %s", sent)
	}
}

// A server that reports only counts leaves the rest unknown, a reported 0
// stays 0, and a field of an unexpected shape costs only that field.
func TestOpenAIUsageUnknownZeroAndOddShapes(t *testing.T) {
	u, _ := chatWithUsage(t, `{"prompt_tokens":10,"completion_tokens":2}`)
	if u.PromptTokens != 10 || u.CachedTokens != nil || u.Cost != nil {
		t.Fatalf("vanilla %+v", u)
	}
	u, _ = chatWithUsage(t, `{"prompt_tokens":10,"completion_tokens":2,"cost":0,"prompt_tokens_details":{"cached_tokens":0}}`)
	if u.CachedTokens == nil || *u.CachedTokens != 0 || u.Cost == nil || *u.Cost != 0 {
		t.Fatalf("reported zeros %+v", u)
	}
	u, _ = chatWithUsage(t, `{"prompt_tokens":10,"completion_tokens":2,"cost":"0.1","prompt_tokens_details":null,"completion_tokens_details":{"reasoning_tokens":"x"}}`)
	if u.PromptTokens != 10 || u.OutputTokens != 2 || u.Cost != nil || u.CachedTokens != nil || u.ReasoningTokens != 0 {
		t.Fatalf("odd shapes %+v", u)
	}
	if u.Unreported {
		t.Fatal("counts reported as missing")
	}
	// No counts at all: not a request of zero tokens.
	for _, missing := range []string{`null`, `{}`, `{"cost":0.01}`} {
		if u, _ = chatWithUsage(t, missing); !u.Unreported || u.PromptTokens != 0 {
			t.Fatalf("%s: %+v", missing, u)
		}
	}
}

func price(f float64) *float64 { return &f }

func TestCost(t *testing.T) {
	cached := 9000
	u := Usage{PromptTokens: 10000, OutputTokens: 1000, CachedTokens: &cached}
	rates := Rates{Input: price(3), CachedInput: price(0.3), Output: price(15)}
	// 1000 uncached × $3 + 9000 cached × $0.30 + 1000 out × $15, per million.
	if c, est := Cost(u, rates); c == nil || !est || (*c-0.0207) > 1e-12 || (0.0207-*c) > 1e-12 {
		t.Fatalf("estimate %v %v", c, est)
	}
	// Cached input unset is priced as input.
	if c, _ := Cost(u, Rates{Input: price(3), Output: price(15)}); c == nil || (*c-0.045) > 1e-12 || (0.045-*c) > 1e-12 {
		t.Fatalf("no cached rate %v", *c)
	}
	// A reported cost wins over any rate, and is not an estimate.
	u.Cost = price(0.5)
	if c, est := Cost(u, rates); c == nil || *c != 0.5 || est {
		t.Fatalf("reported %v %v", c, est)
	}
	// No report and no rates: unknown, not free.
	if c, _ := Cost(Usage{PromptTokens: 5, OutputTokens: 5}, Rates{Input: price(1)}); c != nil {
		t.Fatalf("unknown came back as %v", *c)
	}
}

// Prompt tokens written to the cache are priced at their own rate, as OpenAI
// and Anthropic charge more for them, and a prompt past a tier is priced at
// the tier's rates, as OpenAI charges more past 272,000 tokens; unstated,
// a write is priced as input.
func TestCostOfCacheWritesAndLongPrompts(t *testing.T) {
	near := func(got *float64, want float64) bool {
		return got != nil && *got-want < 1e-12 && want-*got < 1e-12
	}
	rates := Rates{Input: price(2), CachedInput: price(0.1), CacheWrite: price(2.5), Output: price(10),
		Tiers: []RateTier{{Above: 272000, Input: price(4), CachedInput: price(0.2), CacheWrite: price(5), Output: price(15)}}}
	cached := 1000
	short := Usage{PromptTokens: 10000, CachedTokens: &cached, CacheWriteTokens: 6000, OutputTokens: 500}
	// 3000 × $2 + 1000 × $0.10 + 6000 × $2.50 + 500 × $10, per million.
	if c, _ := Cost(short, rates); !near(c, 0.0261) {
		t.Fatalf("a short prompt: %v", *c)
	}
	long := Usage{PromptTokens: 300000, CachedTokens: &cached, CacheWriteTokens: 6000, OutputTokens: 500}
	// 293000 × $4 + 1000 × $0.20 + 6000 × $5 + 500 × $15, per million.
	if c, _ := Cost(long, rates); !near(c, 1.2097) {
		t.Fatalf("a prompt past the tier: %v", *c)
	}
	if c, _ := Cost(short, Rates{Input: price(2), CachedInput: price(0.1), Output: price(10)}); !near(c, 0.0231) {
		t.Fatalf("a write without its own rate: %v", *c)
	}
}
