package config

import (
	"strings"
	"testing"
)

// cache_control says a model's provider is to be asked to cache its prompt.
// It is read, round-trips through the file Settings writes, and is refused
// where it means nothing: a word the provider is not sent, and Ollama.
func TestCacheControlParsesAndRoundTrips(t *testing.T) {
	c, err := Parse([]byte(`
[servers.router]
provider = "openai"
url = "https://openrouter.ai/api/v1"

[control]
server = "router"
model = "anthropic/claude-sonnet-5.5"
cache_control = "ephemeral"

[models.long]
server = "router"
model = "anthropic/claude-opus-5"
cache_control = "1h"
`))
	if err != nil {
		t.Fatal(err)
	}
	if c.Control.CacheControl != "ephemeral" || c.Models["long"].CacheControl != "1h" {
		t.Fatalf("parsed: %q, %q", c.Control.CacheControl, c.Models["long"].CacheControl)
	}
	out, back, err := SetControl([]byte("[servers.router]\nprovider = \"openai\"\nurl = \"https://openrouter.ai/api/v1\"\n"), c.Control)
	if err != nil {
		t.Fatal(err)
	}
	if back.Control.CacheControl != "ephemeral" || !strings.Contains(string(out), "cache_control = \"ephemeral\"\n") {
		t.Fatalf("not written back:\n%s", out)
	}
}

func TestCacheControlRefusals(t *testing.T) {
	for name, tc := range map[string]struct{ toml, want string }{
		"a word nobody sends": {"[control]\nprovider = \"openai\"\nurl = \"https://h/v1\"\nmodel = \"m\"\ncache_control = \"forever\"\n", `cache_control "forever" is not supported (ephemeral, 1h)`},
		"ollama":              {"[control]\nurl = \"http://h\"\nmodel = \"m\"\ncache_control = \"ephemeral\"\n", "cache_control is for openai and anthropic servers"},
	} {
		if _, err := Parse([]byte(tc.toml)); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v, want an error saying %q", name, err, tc.want)
		}
	}
}
