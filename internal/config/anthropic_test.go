package config

import (
	"slices"
	"strings"
	"testing"

	"github.com/fclaude/nodwyn/internal/catalogue"
)

// Anthropic is a provider kind of its own: a server may name it, take a key
// and the models on it the settings Claude's API reads, and nothing it does
// not (a dialect, a session key, a reply cap field, a seed).
func TestAnthropicProviderValidation(t *testing.T) {
	const server = "[servers.claude]\nprovider = \"anthropic\"\nurl = \"https://api.anthropic.com/v1\"\ncredential = \"model-anthropic\"\n\n"
	c, err := Parse([]byte(server + `
[control]
server = "claude"
model = "claude-sonnet-5-5"
effort = "high"

[models.sonnet]
server = "claude"
model = "claude-sonnet-5-5"
efforts = ["low", "medium", "high"]
cache_control = "1h"
vision = true
temperature = 1
`))
	if err != nil {
		t.Fatal(err)
	}
	if srv := c.Servers["claude"]; srv.Provider != "anthropic" || srv.Credential != "model-anthropic" {
		t.Fatalf("server: %+v", srv)
	}
	if m := c.Resolve(c.Models["sonnet"]); m.Provider != "anthropic" || m.URL != "https://api.anthropic.com/v1" || m.Credential != "model-anthropic" || m.CacheControl != "1h" {
		t.Fatalf("model: %+v", m)
	}
	for name, tc := range map[string]struct{ body, want string }{
		"a dialect":           {server + "[models.m]\nserver = \"claude\"\nmodel = \"m\"\n[servers.other]\nprovider = \"anthropic\"\nurl = \"https://a.test\"\ndialect = \"cerebras\"\n", "dialect is for openai servers"},
		"a seed":              {server + "[models.m]\nserver = \"claude\"\nmodel = \"m\"\nseed = 7\n", "seed is not supported for anthropic"},
		"a temperature of 2":  {server + "[models.m]\nserver = \"claude\"\nmodel = \"m\"\ntemperature = 2\n", "between 0 and 1 for anthropic"},
		"a session key":       {server + "[models.m]\nserver = \"claude\"\nmodel = \"m\"\ncache_key = \"header\"\n", "cache_key is for openai servers"},
		"a reply cap field":   {server + "[models.m]\nserver = \"claude\"\nmodel = \"m\"\noutput_field = \"max_tokens\"\n", "output_field is for openai servers"},
		"an unknown provider": {"[servers.s]\nprovider = \"gemini\"\nurl = \"https://a.test\"\n", "provider \"gemini\" is not supported (ollama, openai, anthropic)"},
	} {
		if _, err := Parse([]byte(tc.body)); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v, want an error saying %q", name, err, tc.want)
		}
	}
	// Ollama still has no key, and still none of these.
	if _, err := Parse([]byte("[servers.s]\nurl = \"http://a:1\"\ncredential = \"k\"\n")); err == nil {
		t.Error("a key for Ollama was accepted")
	}
}

// A key goes where its provider reads it, which is also the position its
// binding must allow.
func TestKeyPosition(t *testing.T) {
	for provider, want := range map[string]string{"anthropic": "header:x-api-key", "openai": "header:Authorization:Bearer", "": "header:Authorization:Bearer"} {
		if got := KeyPosition(provider); got != want {
			t.Errorf("KeyPosition(%q) = %q, want %q", provider, got, want)
		}
	}
}

// What Anthropic publishes about a model fills what its entry leaves unset,
// found by the address of its server like any other provider's.
func TestAnthropicModelsArePublished(t *testing.T) {
	c, err := Parse([]byte(`
[servers.claude]
provider = "anthropic"
url = "https://api.anthropic.com/v1"

[servers.elsewhere]
provider = "anthropic"
url = "https://claude-proxy.example.com/v1"

[models.sonnet]
server = "claude"
model = "claude-sonnet-5-5"

[models.proxied]
server = "elsewhere"
model = "claude-sonnet-5-5"
`))
	if err != nil {
		t.Fatal(err)
	}
	cacheRead, cacheWrite := 0.2, 2.5
	c.Catalogue = catalogue.New(map[string]map[string]catalogue.Model{"api.anthropic.com": {
		"claude-sonnet-5-5": {Context: 1000000, Output: 128000, Efforts: []string{"low", "medium", "high", "xhigh", "max"}, Images: true,
			Price: &catalogue.Price{Input: 2, Output: 10, CacheRead: &cacheRead, CacheWrite: &cacheWrite}},
	}})
	sonnet := c.WithPublished(c.Models["sonnet"])
	if sonnet.NumCtx != 1000000 || sonnet.MaxOutput != 128000 || !sonnet.TakesImages() || !slices.Equal(c.Efforts(c.Models["sonnet"]), []string{"low", "medium", "high", "xhigh", "max"}) {
		t.Errorf("sonnet: %+v, efforts %v", sonnet, c.Efforts(c.Models["sonnet"]))
	}
	rates := c.Rates(c.Models["sonnet"])
	if !rates.Known() || *rates.Input != 2 || *rates.Output != 10 || *rates.CachedInput != 0.2 || *rates.CacheWrite != 2.5 {
		t.Errorf("rates: %+v", rates)
	}
	if _, ok := c.Published(c.Models["proxied"]); ok {
		t.Error("a proxy at another address was described as Anthropic")
	}
}
