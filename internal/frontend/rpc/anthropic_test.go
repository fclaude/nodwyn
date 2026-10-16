package rpc

import (
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/fclaude/nodwyn/internal/config"
	"github.com/fclaude/nodwyn/test/fakes/chat"
)

// An Anthropic provider is added with its key like any other, but the key is
// bound where Anthropic reads it, as x-api-key and never as a bearer token:
// the rows say so, readiness is judged against them, and the provider is
// sent the key in the header it reads. A wrong key is found by a test request
// as a rejected key, and the key never comes back out.
func TestAnthropicProviderIsSavedWithItsKeyAtXAPIKey(t *testing.T) {
	c, srv, o, _ := withChat(t)
	var settings settingsResult
	into(t, c.ok("settings.provider.write", map[string]any{"provider": "anthropic", "url": srv.V1() + "/", "api_key": "sk-ant-wrong-000000"}), &settings)
	if len(settings.Servers) != 1 {
		t.Fatalf("servers: %+v", settings.Servers)
	}
	p := settings.Servers[0]
	if p.Name != "127-0-0-1" || p.Provider != "anthropic" || p.URL != srv.V1() || p.Dialect != "" || p.Credential != "model-127-0-0-1" || p.Key != "ready" {
		t.Fatalf("provider: %+v", p)
	}
	cfg := read(t, o.Paths.ConfigFile)
	if !strings.Contains(cfg, "[servers.127-0-0-1]\nprovider = \"anthropic\"\nurl = \""+srv.V1()+"\"\ncredential = \"model-127-0-0-1\"\n") || strings.Contains(cfg, "sk-ant") {
		t.Fatalf("config.toml:\n%s", cfg)
	}
	// POST for the model and GET for its list, at x-api-key, there and nowhere
	// else; no row lets the key go as a bearer token.
	bindings := read(t, o.Paths.Bindings)
	if strings.Count(bindings, "[[bind]]") != 2 || strings.Count(bindings, `positions = ["header:x-api-key"]`) != 2 || strings.Contains(bindings, "Bearer") ||
		strings.Count(bindings, `dest = "127.0.0.1"`) != 2 || strings.Contains(bindings, "sk-ant") {
		t.Fatalf("bindings:\n%s", bindings)
	}
	var result testResult
	into(t, c.ok("models.test", map[string]any{"server": "127-0-0-1", "model": "thinker"}), &result)
	if result.OK || result.State != "rejected" || result.Status != 401 || !strings.Contains(result.Detail, "did not accept the key") || strings.Contains(result.Detail, "sk-ant-wrong") {
		t.Fatalf("a wrong key: %+v", result)
	}
	into(t, c.ok("settings.provider.write", map[string]any{"name": "127-0-0-1", "provider": "anthropic", "url": srv.V1(), "api_key": "sk-good-key-123456"}), &settings)
	if settings.Keychain.Credentials != 1 || strings.Count(read(t, o.Paths.Bindings), "[[bind]]") != 2 {
		t.Fatalf("replacing the key: %+v\n%s", settings.Keychain, read(t, o.Paths.Bindings))
	}
	into(t, c.ok("models.test", map[string]any{"server": "127-0-0-1", "model": "thinker"}), &result)
	if !result.OK || result.State != "ok" {
		t.Fatalf("the right key: %+v", result)
	}
	into(t, c.ok("models.test", map[string]any{"server": "127-0-0-1", "model": "absent"}), &result)
	if result.OK || result.State != "no_model" || result.Status != 404 {
		t.Fatalf("a missing model: %+v", result)
	}
	for i, r := range srv.Requests() {
		if r.Path != "/v1/messages" || r.KeyHeader != "x-api-key" || r.HasEffort {
			t.Errorf("request %d: %+v", i, r)
		}
	}
	if got := srv.Requests(); len(got) != 3 || got[0].Authenticated || !got[1].Authenticated {
		t.Fatalf("requests: %+v", got)
	}
	// The provider's models are listed with the same key, which only its
	// binding at x-api-key can release.
	srv.ListNeedsKey = true
	var names []string
	into(t, c.ok("models.offered", map[string]any{"server": "127-0-0-1"}), &names)
	if !reflect.DeepEqual(names, []string{"claude", "plain", "thinker"}) {
		t.Fatalf("models offered: %v", names)
	}
}

// A model on Anthropic is tried with the effort it takes (adaptive thinking
// and output_config.effort) and the ask to cache its prompt; the refusal of
// an effort a model older than 4.6 cannot take says what Anthropic's effort
// asks for, not that the cache is at fault.
func TestAnthropicModelTestCarriesEffortAndCache(t *testing.T) {
	c, srv, _, _ := withChat(t)
	c.ok("settings.provider.write", map[string]any{"provider": "anthropic", "url": srv.V1(), "api_key": "sk-good-key-123456"})
	c.ok("settings.model.write", map[string]any{"server": "127-0-0-1", "model": "thinker", "efforts": levels, "effort": "medium", "settings": map[string]any{"cache_control": "1h"}})
	// "plain" is an older model, declared to take an effort it cannot.
	c.ok("settings.model.write", map[string]any{"server": "127-0-0-1", "model": "plain", "efforts": []string{"high"}, "settings": map[string]any{"cache_control": "ephemeral"}})
	var result testResult
	into(t, c.ok("models.test", map[string]any{"name": "127-0-0-1-thinker", "effort": "high"}), &result)
	if !result.OK || result.Effort != "high" || result.CacheControl != "1h" {
		t.Fatalf("an effort the model takes: %+v", result)
	}
	into(t, c.ok("models.test", map[string]any{"name": "127-0-0-1-plain", "effort": "high"}), &result)
	if result.OK || result.State != "refused" || result.Status != 400 || !strings.Contains(result.Detail, "adaptive thinking") || strings.Contains(result.Detail, "turn Prompt cache off") {
		t.Fatalf("an effort the model cannot take: %+v", result)
	}
	got := srv.Requests()
	if len(got) != 2 || got[0].Effort != "high" || !strings.Contains(got[0].Thinking, "adaptive") || got[0].CacheControl != `{"type":"ephemeral","ttl":"1h"}` || got[0].Answered != 200 || got[1].Answered != 400 {
		t.Fatalf("the provider saw %+v", got)
	}
}

// Anthropic has no dialect and no seed, and a key goes only where Anthropic
// reads it: a dialect is refused for it, as is a seed for its models.
func TestAnthropicProviderRefusals(t *testing.T) {
	c, srv, o, _ := withChat(t)
	c.ok("settings.provider.write", map[string]any{"provider": "anthropic", "url": srv.V1(), "api_key": "sk-good-key-123456"})
	for name, tc := range map[string]struct {
		params map[string]any
		method string
		want   string
	}{
		"a dialect":            {map[string]any{"provider": "anthropic", "url": "https://other.example/v1", "dialect": "responses"}, "settings.provider.write", "takes no dialect"},
		"a key over http":      {map[string]any{"provider": "anthropic", "url": "http://api.example.test/v1", "api_key": "sk-ant-x"}, "settings.provider.write", "over https"},
		"a seed":               {map[string]any{"server": "127-0-0-1", "model": "thinker", "settings": map[string]any{"seed": 7}}, "settings.model.write", "seed is not supported for anthropic"},
		"a temperature of 1.5": {map[string]any{"server": "127-0-0-1", "model": "thinker", "settings": map[string]any{"temperature": 1.5}}, "settings.model.write", "between 0 and 1 for anthropic"},
		"a session key":        {map[string]any{"server": "127-0-0-1", "model": "thinker", "settings": map[string]any{"cache_key": "header"}}, "settings.model.write", "cache_key is for openai servers"},
		"a reply cap field":    {map[string]any{"server": "127-0-0-1", "model": "thinker", "settings": map[string]any{"output_field": "max_tokens"}}, "settings.model.write", "output_field is for openai servers"},
	} {
		before := read(t, o.Paths.ConfigFile)
		r := c.call(tc.method, tc.params)
		if r.Error == nil || !strings.Contains(r.Error.Message, tc.want) {
			t.Errorf("%s: %+v, want an error saying %q", name, r.Error, tc.want)
		}
		if read(t, o.Paths.ConfigFile) != before {
			t.Errorf("%s: config.toml changed", name)
		}
	}
	// Vision and the ask to cache are for it, and are saved.
	c.ok("settings.model.write", map[string]any{"server": "127-0-0-1", "model": "thinker", "settings": map[string]any{"vision": false, "cache_control": "ephemeral", "temperature": 0.7}})
	cfg := read(t, o.Paths.ConfigFile)
	if !strings.Contains(cfg, "vision = false") || !strings.Contains(cfg, `cache_control = "ephemeral"`) || !strings.Contains(cfg, "temperature = 0.7") {
		t.Fatalf("config.toml:\n%s", cfg)
	}
}

// A key for an Anthropic server is allowed where Anthropic reads it: one
// bound as a bearer token, which is how every other model key is bound, is
// said not to be allowed, and the page names the fix; once it is bound at
// x-api-key the model is ready.
func TestAnthropicKeyBoundAsABearerTokenIsNotAllowed(t *testing.T) {
	srv := chat.New("sk-good-key-123456", chat.Model{ID: "thinker", Efforts: levels})
	defer srv.Close()
	o := options(t)
	raw := "[servers.claude]\nprovider = \"anthropic\"\nurl = \"" + srv.V1() + "\"\ncredential = \"claude-key\"\n\n[control]\nserver = \"claude\"\nmodel = \"thinker\"\n"
	if err := os.MkdirAll(o.Paths.ConfigDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(o.Paths.ConfigFile, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	var err error
	if o.Config, err = config.Parse([]byte(raw)); err != nil {
		t.Fatal(err)
	}
	path, _ := start(t, o)
	c := connect(t, path)
	c.hello()
	c.ok("keychain.create", map[string]any{"no_passphrase": true})
	var stored credentialsResult
	into(t, c.ok("credentials.add", map[string]any{"class": "claude-key", "value": "sk-good-key-123456"}), &stored)
	state := func() modelStatus {
		forgetProbes()
		var status []modelStatus
		into(t, c.ok("models.status", map[string]any{}), &status)
		return status[0]
	}
	id := stored.Credentials[0].ID
	c.ok("credentials.bind", map[string]any{"id": id, "dest": "127.0.0.1", "methods": []string{"POST"}, "position": "header:Authorization:Bearer"})
	if got := state(); got.State != "unbound" || !strings.Contains(got.Detail, "position header:x-api-key") {
		t.Fatalf("a bearer binding for an Anthropic key: %+v", got)
	}
	c.ok("credentials.bind", map[string]any{"id": id, "dest": "127.0.0.1", "methods": []string{"POST"}, "position": "header:x-api-key"})
	if got := state(); got.State != "ready" {
		t.Fatalf("a binding at x-api-key: %+v", got)
	}
}

// The first-run form sets Anthropic up: the server is saved with its key
// bound at x-api-key, and the default model answers a test request through it.
func TestControlWriteSavesAnAnthropicServerWithItsKey(t *testing.T) {
	c, srv, o, _ := withChat(t)
	var settings settingsResult
	c.ok("settings.provider.write", map[string]any{"provider": "anthropic", "url": srv.V1(), "api_key": "sk-good-key-123456"})
	into(t, c.ok("settings.control.write", map[string]any{"server": "127-0-0-1", "model": "thinker"}), &settings)
	cfg := read(t, o.Paths.ConfigFile)
	if !strings.Contains(cfg, "[servers.127-0-0-1]\nprovider = \"anthropic\"\nurl = \""+srv.V1()+"\"\ncredential = \"model-127-0-0-1\"\n") || strings.Contains(cfg, "sk-good") {
		t.Fatalf("config.toml:\n%s", cfg)
	}
	bindings := read(t, o.Paths.Bindings)
	if strings.Count(bindings, `positions = ["header:x-api-key"]`) != 2 || strings.Contains(bindings, "Bearer") {
		t.Fatalf("bindings:\n%s", bindings)
	}
	var status []modelStatus
	into(t, c.ok("models.status", map[string]any{}), &status)
	if len(status) != 1 || status[0].Provider != "anthropic" || status[0].State != "ready" {
		t.Fatalf("status: %+v", status)
	}
	var result testResult
	into(t, c.ok("models.test", map[string]any{"name": "control"}), &result)
	if !result.OK {
		t.Fatalf("the default model: %+v", result)
	}
	if got := srv.Requests(); len(got) != 1 || got[0].Path != "/v1/messages" || got[0].KeyHeader != "x-api-key" {
		t.Fatalf("the provider saw %+v", got)
	}
}
