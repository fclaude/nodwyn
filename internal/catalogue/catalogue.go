// Package catalogue is what providers publish about their models: how many
// tokens a model's context holds, how long one reply may be, the reasoning
// efforts it takes, whether it reads images and what it costs, as models.dev
// collects them (ADR 41, amended 2026-10-05). OpenAI's and Cerebras' APIs say
// none of it, so without it a person types every number for every model.
//
// The catalogue is a snapshot built into nodwyn (models.json, written by
// `go generate ./internal/catalogue`). It describes models; it decides
// nothing about where a request may go. Which provider a model is on comes
// from the address its server has in config.toml, matched against the hosts
// below, which only nodwyn's source names: models.dev never supplies an
// address. A model's own entry in config.toml wins over anything here.
package catalogue

import (
	_ "embed"
	"encoding/json"
	"net/url"
	"sort"
	"strings"
	"sync"
)

//go:generate go run ./gen

//go:embed models.json
var embedded []byte

// Model is what a provider publishes about one model. Zero is not
// published.
type Model struct {
	Context int      `json:"context,omitempty"` // tokens the context holds, replies included
	Output  int      `json:"output,omitempty"`  // the most one reply may take
	Efforts []string `json:"efforts,omitempty"` // the reasoning efforts it takes, in its provider's words
	Images  bool     `json:"images"`            // it reads images
	Price   *Price   `json:"price,omitempty"`
}

// Price is a model's price in USD per million tokens. CacheRead and
// CacheWrite are prompt tokens read from and written to the provider's cache;
// nil is not published. Tiers are the prices of a request whose prompt is
// longer than a tier's Above, as OpenAI's long-context pricing is.
type Price struct {
	Input      float64  `json:"input"`
	Output     float64  `json:"output"`
	CacheRead  *float64 `json:"cache_read,omitempty"`
	CacheWrite *float64 `json:"cache_write,omitempty"`
	Tiers      []Tier   `json:"tiers,omitempty"`
}

// Tier is the price of a request whose prompt holds more than Above tokens.
type Tier struct {
	Above      int      `json:"above"`
	Input      float64  `json:"input"`
	Output     float64  `json:"output"`
	CacheRead  *float64 `json:"cache_read,omitempty"`
	CacheWrite *float64 `json:"cache_write,omitempty"`
}

// Provider is one provider the catalogue describes: models.dev's id for it
// and the address of its OpenAI-compatible API, whose host is how a server
// in config.toml is known to be it.
type Provider struct {
	ID  string
	URL string
}

// Providers are the providers nodwyn reads from models.dev: those with an
// OpenAI-compatible API, at the address each documents, and Anthropic, whose
// own API is reached at its base.
var Providers = []Provider{
	{"openai", "https://api.openai.com/v1"},
	{"anthropic", "https://api.anthropic.com/v1"},
	{"openrouter", "https://openrouter.ai/api/v1"},
	{"cerebras", "https://api.cerebras.ai/v1"},
	{"groq", "https://api.groq.com/openai/v1"},
	{"deepseek", "https://api.deepseek.com/v1"},
	{"mistral", "https://api.mistral.ai/v1"},
	{"xai", "https://api.x.ai/v1"},
	{"togetherai", "https://api.together.xyz/v1"},
	{"fireworks-ai", "https://api.fireworks.ai/inference/v1"},
	{"deepinfra", "https://api.deepinfra.com/v1/openai"},
	{"moonshotai", "https://api.moonshot.ai/v1"},
	{"zai", "https://api.z.ai/api/paas/v4"},
	{"baseten", "https://inference.baseten.co/v1"},
	{"ollama-cloud", "https://ollama.com/v1"},
}

// judges are the prices of the judges' typed-question APIs, by host and
// model, which nodwyn keeps itself: models.dev does not list Jev, and lists
// gpt-6-luna at its chat prices, while OpenAI's Decisions API charges only
// for input. typesafe.ai publishes Jev's price on its home page as "$42 per
// billion input tokens"; OpenAI's Decisions guide
// (developers.openai.com/api/docs/guides/decisions) gives gpt-6-luna's as
// "$0.10 per 1M tokens" of input, with no output charge. Whoever changes one
// cites its provider's page here.
var judges = map[string]map[string]Price{
	"api.typesafe.ai": {"jev-latest": {Input: 0.042, Output: 0}},
	"api.openai.com":  {"gpt-6-luna": {Input: 0.10, Output: 0}},
}

// JudgePrice is what model's provider publishes as the price of a judge's
// question to it at the server whose base URL is baseURL, which is not the
// price of a chat with the same model; false when nodwyn does not know it.
func JudgePrice(baseURL, model string) (Price, bool) {
	p, ok := judges[host(baseURL)][model]
	return p, ok
}

// Data is the snapshot as models.json holds it: where it came from, when,
// and each provider's models by id.
type Data struct {
	Source    string                      `json:"source"`
	Fetched   string                      `json:"fetched"`
	Providers map[string]map[string]Model `json:"providers"`
}

// Catalogue answers what is published about a model at an address.
type Catalogue struct {
	byHost map[string]map[string]Model
}

// New is a catalogue of the models at each host, by id.
func New(byHost map[string]map[string]Model) *Catalogue {
	return &Catalogue{byHost: byHost}
}

var builtIn = sync.OnceValue(func() *Catalogue {
	var d Data
	if err := json.Unmarshal(embedded, &d); err != nil {
		panic("catalogue: models.json does not read: " + err.Error())
	}
	byHost := map[string]map[string]Model{}
	for _, p := range Providers {
		if models, ok := d.Providers[p.ID]; ok {
			byHost[host(p.URL)] = models
		}
	}
	return New(byHost)
})

// Default is the snapshot nodwyn was built with.
func Default() *Catalogue { return builtIn() }

// Lookup is what is published about model at the server whose base URL is
// baseURL; false when its host is no provider the catalogue describes, or
// the provider publishes no such model.
func (c *Catalogue) Lookup(baseURL, model string) (Model, bool) {
	if c == nil {
		return Model{}, false
	}
	m, ok := c.byHost[host(baseURL)][model]
	return m, ok
}

// Like is what the catalogue's providers publish about a model whose id is
// spelled like model, whatever its host: the same id once case, a
// "vendor/" prefix and every character but letters and digits are set
// aside, so qwen-3.8-27b on Cerebras is qwen3.8-27b elsewhere and
// anthropic/claude-sonnet-5.5 is claude-sonnet-5-5. Several providers may
// publish it: their smallest context and reply cap are what it holds on any
// of them. The provider whose context that is names where it came from.
// It is for a server the catalogue does not describe, or that does not list
// the model, and says nothing about prices or efforts, which are each
// provider's own.
func (c *Catalogue) Like(model string) (m Model, provider string, ok bool) {
	if c == nil {
		return Model{}, "", false
	}
	want := spelling(model)
	if want == "" {
		return Model{}, "", false
	}
	for _, h := range c.hosts() {
		for id, found := range c.byHost[h] {
			if spelling(id) != want || found.Context == 0 {
				continue
			}
			if !ok || found.Context < m.Context {
				m.Context, provider = found.Context, h
			}
			if found.Output > 0 && (m.Output == 0 || found.Output < m.Output) {
				m.Output = found.Output
			}
			ok = true
		}
	}
	return Model{Context: m.Context, Output: m.Output}, provider, ok
}

// hosts are the catalogue's hosts in order, so Like answers the same way
// every time.
func (c *Catalogue) hosts() []string {
	hs := make([]string, 0, len(c.byHost))
	for h := range c.byHost {
		hs = append(hs, h)
	}
	sort.Strings(hs)
	return hs
}

// spelling is model's id with a "vendor/" prefix set aside, lower-cased,
// letters and digits only.
func spelling(model string) string {
	id := strings.ToLower(strings.TrimSpace(model))
	if i := strings.LastIndexByte(id, '/'); i >= 0 {
		id = id[i+1:]
	}
	var b strings.Builder
	for _, r := range id {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// host is the lower-cased host of a base URL, "" when it does not parse.
func host(base string) string {
	u, err := url.Parse(strings.TrimSpace(base))
	if err != nil {
		return ""
	}
	return strings.ToLower(u.Hostname())
}
