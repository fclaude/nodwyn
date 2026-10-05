package core

import (
	"bytes"
	"math"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/fclaude/nodwyn/internal/binding"
	"github.com/fclaude/nodwyn/internal/catalogue"
	"github.com/fclaude/nodwyn/internal/config"
	"github.com/fclaude/nodwyn/internal/keychain"
	"github.com/fclaude/nodwyn/internal/log"
	"github.com/fclaude/nodwyn/test/fakes/chat"
)

// A server that lists its models only to a key, as OpenAI and Cerebras do,
// is asked for its list with the model's key, after the binding check every
// request with it has. Sessions 20261005T042752Z-871dfe24 (OpenAI, 401) and
// 20261004T182412Z-747045aa (Cerebras, 403) asked without one and kept the
// default window. A key bound for POST alone, as keys saved before were, is
// not sent, and the note says why.
func TestAModelListThatNeedsTheKeyIsAskedWithIt(t *testing.T) {
	t.Parallel()
	const key = "sk-list-key-0123456789"
	row := func(method string) binding.Binding {
		return binding.Binding{Class: "model-key", Dest: "127.0.0.1", Positions: []string{"header:Authorization:Bearer"}, Methods: []string{method}}
	}
	for _, tc := range []struct {
		name   string
		rows   []binding.Binding
		numCtx int
		note   string
	}{
		{"bound for its list", []binding.Binding{row("POST"), row("GET")}, 1050000, "sized for sol's context of 1050000 tokens less its reply cap of 16384"},
		{"bound for POST alone", []binding.Binding{row("POST")}, 0, "method GET at position header:Authorization:Bearer is not bound"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			srv := chat.New(key, chat.Model{ID: "sol", Context: 1050000})
			srv.ListNeedsKey = true
			t.Cleanup(srv.Close)
			cfg, err := config.Parse([]byte(`
[servers.openai]
provider = "openai"
url = "` + srv.V1() + `"
credential = "model-key"

[control]
server = "openai"
model = "sol"
`))
			if err != nil {
				t.Fatal(err)
			}
			kc, err := keychain.Create(filepath.Join(t.TempDir(), "kc"), keychain.FastKDF, []byte("p"))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := kc.Add("model-key", []byte(key)); err != nil {
				t.Fatal(err)
			}
			logb := &bytes.Buffer{}
			s, err := New(Options{Config: cfg, Repo: t.TempDir(), NoConfirm: true, LogWriter: logb, Keychain: kc, Bindings: binding.Table{Bind: tc.rows}})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { s.Close() })
			r := &rig{t: t, s: s, logb: logb}
			if got := r.evalInt("(assoc-ref (model-info) 'num-ctx)"); got != tc.numCtx {
				t.Errorf("num-ctx %d, want %d", got, tc.numCtx)
			}
			if !strings.Contains(logb.String(), tc.note) {
				t.Errorf("the log does not say %q:\n%.2000s", tc.note, logb.String())
			}
			if strings.Contains(logb.String(), key) {
				t.Fatal("the key reached the log")
			}
			var asked bool
			for _, ev := range s.EventsSoFar() {
				var check log.EgressCheck
				if ev.Kind == log.KindEgressCheck && ev.Decode(&check) == nil && check.Method == "GET" {
					asked = true
					if check.Bound != (tc.numCtx > 0) {
						t.Errorf("the list's egress check: %+v", check)
					}
				}
			}
			if !asked {
				t.Error("no egress check was recorded for the model list")
			}
			// The model's own requests are unchanged either way.
			if e := r.turn("hi"); e != "" {
				t.Fatal(e)
			}
		})
	}
}

// A model whose context is smaller than the default window is sized down to
// fit it, its default reply cap a quarter of the context, and its requests
// carry that cap. The window used to grow only: a 32768-token model kept the
// 61440-token high water and was sent requests it could not hold.
func TestASmallContextShrinksTheWindowAndTheReplyCap(t *testing.T) {
	t.Parallel()
	srv := chat.New("", chat.Model{ID: "small", Context: 32768})
	t.Cleanup(srv.Close)
	cfg, err := config.Parse([]byte(`
[servers.local]
provider = "openai"
url = "` + srv.V1() + `"

[control]
server = "local"
model = "small"
`))
	if err != nil {
		t.Fatal(err)
	}
	logb := &bytes.Buffer{}
	s, err := New(Options{Config: cfg, Repo: t.TempDir(), NoConfirm: true, LogWriter: logb})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	r := &rig{t: t, s: s, logb: logb}
	for src, want := range map[string]int{
		"(assoc-ref (window-limits) 'high-water)": (32768 - 8192) * 3 / 4,
		"(assoc-ref (window-limits) 'low-water)":  (32768 - 8192) * 3 / 8,
		"(assoc-ref (window-limits) 'ceiling)":    32768 - 8192,
		"(assoc-ref (model-info) 'max-output)":    8192,
	} {
		if got := r.evalInt(src); got != want {
			t.Errorf("%s = %d, want %d", src, got, want)
		}
	}
	if !strings.Contains(logb.String(), "sized for small's context of 32768 tokens less its reply cap of 8192") {
		t.Errorf("the log does not say how the window was sized:\n%.2000s", logb.String())
	}
	if e := r.turn("hi"); e != "" {
		t.Fatal(e)
	}
	var capped bool
	for _, ev := range s.EventsSoFar() {
		var req log.LLMReq
		if ev.Kind == log.KindLLMReq && ev.Decode(&req) == nil {
			capped = req.MaxOutput == 8192
		}
	}
	if !capped {
		t.Error("the request did not record the 8192-token reply cap, so it would not replay")
	}
	if n := replayAll(t, logb); n != 1 {
		t.Fatalf("replayed %d requests", n)
	}
}

// A model whose entry says nothing of it has what its provider publishes
// (ADR 41, amended 2026-10-05): the conversation is sized for the published
// context less the published reply limit, its requests carry that limit, it
// offers the published efforts, and its replies are priced at the published
// prices, a cache write and a long prompt's tier included. Session
// 20261005T042752Z-871dfe24, gpt-6.1-sol at api.openai.com, kept the default
// window, capped replies at 16384 and priced nothing.
func TestAModelHasWhatItsProviderPublishes(t *testing.T) {
	t.Parallel()
	srv := chat.New("", chat.Model{ID: "sol", Efforts: []string{"low", "medium", "high", "xhigh", "max"}, Caches: true})
	t.Cleanup(srv.Close)
	cfg, err := config.Parse([]byte(`
[servers.openai]
provider = "openai"
url = "` + srv.V1() + `"

[control]
server = "openai"
model = "sol"
cache_control = "ephemeral"
`))
	if err != nil {
		t.Fatal(err)
	}
	read, write, tierRead, tierWrite := 0.1, 2.5, 0.2, 5.0
	cfg.Catalogue = catalogue.New(map[string]map[string]catalogue.Model{"127.0.0.1": {"sol": {
		Context: 1050000, Output: 128000, Efforts: []string{"low", "medium", "high", "xhigh", "max"}, Images: true,
		Price: &catalogue.Price{Input: 2, Output: 10, CacheRead: &read, CacheWrite: &write,
			Tiers: []catalogue.Tier{{Above: 272000, Input: 4, Output: 15, CacheRead: &tierRead, CacheWrite: &tierWrite}}},
	}}})
	logb := &bytes.Buffer{}
	s, err := New(Options{Config: cfg, Repo: t.TempDir(), NoConfirm: true, LogWriter: logb})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	r := &rig{t: t, s: s, logb: logb}
	for src, want := range map[string]int{
		"(assoc-ref (model-info) 'num-ctx)":       1050000,
		"(assoc-ref (model-info) 'max-output)":    128000,
		"(assoc-ref (window-limits) 'high-water)": (1050000 - 128000) * 3 / 4,
	} {
		if got := r.evalInt(src); got != want {
			t.Errorf("%s = %d, want %d", src, got, want)
		}
	}
	if !strings.Contains(logb.String(), "sized for sol's context of 1050000 tokens less its reply cap of 128000") {
		t.Errorf("the log does not say how the window was sized:\n%.2000s", logb.String())
	}
	if got := s.Model().Efforts; !slices.Equal(got, []string{"low", "medium", "high", "xhigh", "max"}) {
		t.Errorf("efforts offered: %v", got)
	}
	if _, err := s.SetModel(ModelChoice{Name: "control", Effort: "xhigh"}); err != nil {
		t.Fatalf("a published effort: %v", err)
	}
	if e := r.turn(strings.Repeat("a long enough prompt to be cached ", 200)); e != "" {
		t.Fatal(e)
	}
	if got := srv.Requests(); len(got) != 1 || got[0].Effort != "xhigh" {
		t.Fatalf("the provider saw %+v", got)
	}
	var priced int
	for _, ev := range s.EventsSoFar() {
		switch ev.Kind {
		case log.KindLLMReq:
			var req log.LLMReq
			if ev.Decode(&req) == nil && req.MaxOutput != 128000 {
				t.Errorf("a request capped at %d, want the published 128000", req.MaxOutput)
			}
		case log.KindLLMResp:
			var resp log.LLMResp
			if err := ev.Decode(&resp); err != nil {
				t.Fatal(err)
			}
			u := resp.Usage
			cached := 0
			if u.CachedTokens != nil {
				cached = *u.CachedTokens
			}
			if u.CacheWriteTokens == 0 {
				t.Fatalf("the fake wrote nothing to its cache: %+v", u)
			}
			want := (float64(u.PromptTokens-cached-u.CacheWriteTokens)*2 + float64(cached)*0.1 + float64(u.CacheWriteTokens)*2.5 + float64(u.OutputTokens)*10) / 1e6
			if u.Cost == nil || !u.CostEstimated || math.Abs(*u.Cost-want) > 1e-12 {
				t.Fatalf("a reply's cost: %v (estimated %v), want %v at the published prices", u.Cost, u.CostEstimated, want)
			}
			priced++
		}
	}
	if priced != 1 {
		t.Fatalf("%d replies priced", priced)
	}
}

// A price an entry sets alone is kept, and the ones it leaves unset are what
// the provider publishes. A model given only its output price was priced
// entirely at the published prices, so the price typed in Settings did
// nothing; with no catalogue it would have priced nothing.
func TestAPriceSetAloneIsKeptBesideThePublishedOnes(t *testing.T) {
	t.Parallel()
	srv := chat.New("", chat.Model{ID: "sol"})
	t.Cleanup(srv.Close)
	cfg, err := config.Parse([]byte(`
[servers.openai]
provider = "openai"
url = "` + srv.V1() + `"

[control]
server = "openai"
model = "sol"
price_output = 20.0
`))
	if err != nil {
		t.Fatal(err)
	}
	cfg.Catalogue = catalogue.New(map[string]map[string]catalogue.Model{"127.0.0.1": {"sol": {
		Context: 1050000, Output: 128000, Price: &catalogue.Price{Input: 2, Output: 10},
	}}})
	logb := &bytes.Buffer{}
	s, err := New(Options{Config: cfg, Repo: t.TempDir(), NoConfirm: true, LogWriter: logb})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	r := &rig{t: t, s: s, logb: logb}
	if e := r.turn("hello"); e != "" {
		t.Fatal(e)
	}
	var priced int
	for _, ev := range s.EventsSoFar() {
		if ev.Kind != log.KindLLMResp {
			continue
		}
		var resp log.LLMResp
		if err := ev.Decode(&resp); err != nil {
			t.Fatal(err)
		}
		u := resp.Usage
		if u.PromptTokens == 0 || u.OutputTokens == 0 {
			t.Fatalf("the fake reported no tokens to price: %+v", u)
		}
		want := (float64(u.PromptTokens)*2 + float64(u.OutputTokens)*20) / 1e6
		if u.Cost == nil || !u.CostEstimated || math.Abs(*u.Cost-want) > 1e-12 {
			t.Fatalf("a reply's cost: %v (estimated %v), want %v: input as published, output as set", u.Cost, u.CostEstimated, want)
		}
		priced++
	}
	if priced != 1 {
		t.Fatalf("%d replies priced", priced)
	}
}
