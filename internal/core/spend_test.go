package core

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/fclaude/nodwyn/internal/config"
	"github.com/fclaude/nodwyn/internal/llm"
	"github.com/fclaude/nodwyn/internal/log"
)

// A chat-completions server's reported cost and cache use reach llm.resp;
// without a report, configured prices give an estimate marked as one, and
// neither gives nothing.
func TestModelSpendReachesTheLog(t *testing.T) {
	for _, tc := range []struct {
		name, usage string
		prices      bool
		cost        *float64
		estimated   bool
		cachedKnown bool
	}{
		{"reported", `{"prompt_tokens":1000,"completion_tokens":100,"cost":0.004,"prompt_tokens_details":{"cached_tokens":800}}`, false, price(0.004), false, true},
		{"estimated", `{"prompt_tokens":1000,"completion_tokens":100}`, true, price(0.0045), true, false},
		{"unknown", `{"prompt_tokens":1000,"completion_tokens":100}`, false, nil, false, false},
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"done"},"finish_reason":"stop"}],"usage":` + tc.usage + `}`))
		}))
		cfg := config.Default()
		cfg.Control = config.Model{Provider: "openai", URL: srv.URL + "/v1", Model: "m"}
		if tc.prices {
			cfg.Control.PriceInput, cfg.Control.PriceOutput = price(3), price(15)
		}
		logb := &bytes.Buffer{}
		s, err := New(Options{Config: cfg, Repo: t.TempDir(), Provider: llm.NewOpenAI(srv.URL+"/v1", nil), NoConfirm: true, LogWriter: logb})
		if err != nil {
			t.Fatal(err)
		}
		r := &rig{t: t, s: s, logb: logb}
		if e := r.turn("hello"); e != "" {
			t.Fatal(e)
		}
		s.Close()
		srv.Close()
		var u log.Usage
		for _, ev := range r.logged() {
			if ev.Kind == log.KindLLMResp {
				var p log.LLMResp
				ev.Decode(&p)
				u = p.Usage
			}
		}
		if (u.Cost == nil) != (tc.cost == nil) || (u.Cost != nil && (*u.Cost-*tc.cost > 1e-12 || *tc.cost-*u.Cost > 1e-12)) || u.CostEstimated != tc.estimated || (u.CachedTokens != nil) != tc.cachedKnown {
			t.Fatalf("%s: usage %+v", tc.name, u)
		}
	}
}

func price(f float64) *float64 { return &f }
