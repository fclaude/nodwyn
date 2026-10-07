package rpc

import (
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/fclaude/nodwyn/internal/config"
)

// Jev is a provider like any other (ADR 46): its key is saved with the
// provider, kept in the keychain and allowed for its host by POST alone,
// since the judge's questions are its only requests. Chosen as the judge's
// server, it makes the judge ready, its model is jev-latest and its
// questions are priced as typesafe.ai publishes; the key is then used by
// the judge, the provider cannot be removed from under it, and no model
// can be put on it.
func TestJevIsAProviderTheJudgeIsOn(t *testing.T) {
	o := options(t)
	path, _ := start(t, o)
	c := connect(t, path)
	c.hello()
	c.ok("keychain.create", map[string]any{"no_passphrase": true})
	current := func() map[string]any {
		var settings map[string]any
		into(t, c.ok("settings.read", map[string]any{}), &settings)
		return settings
	}
	if s := current(); s["judge"] != false || s["judge_key"] != nil || s["judge_host"] != nil {
		t.Fatalf("a judge before one is chosen: %v %v %v", s["judge"], s["judge_key"], s["judge_host"])
	}
	c.ok("settings.provider.write", map[string]any{"name": "jev", "provider": "jev", "url": "https://api.typesafe.ai", "api_key": "sk-judge-canary-0001"})
	var list credentialsResult
	into(t, c.ok("credentials.list", map[string]any{}), &list)
	if len(list.Credentials) != 1 || !list.Credentials[0].Model || len(list.Credentials[0].Bindings) != 1 {
		t.Fatalf("the Jev key: %+v", list.Credentials)
	}
	if b := list.Credentials[0].Bindings[0]; b.Dest != "api.typesafe.ai" || !slices.Equal(b.Methods, []string{"POST"}) || !slices.Equal(b.Positions, []string{"header:Authorization:Bearer"}) {
		t.Fatalf("the Jev key's binding: %+v", b)
	}

	c.ok("settings.write", map[string]any{"set": map[string]any{"judge.server": "jev"}})
	if !strings.Contains(read(t, o.Paths.ConfigFile), "[judge]\nserver = \"jev\"") {
		t.Fatalf("config.toml:\n%s", read(t, o.Paths.ConfigFile))
	}
	s := current()
	published, _ := s["judge_published"].(map[string]any)
	if s["judge"] != true || s["judge_key"] != "ready" || s["judge_host"] != "api.typesafe.ai" || s["judge_model"] != "jev-latest" || published["price_input"] != 0.042 || published["price_output"] != 0.0 {
		t.Fatalf("the judge on Jev: judge %v key %v host %v model %v published %v", s["judge"], s["judge_key"], s["judge_host"], s["judge_model"], published)
	}
	var settings settingsResult
	into(t, c.ok("settings.read", map[string]any{}), &settings)
	if len(settings.Servers) != 1 || !settings.Servers[0].Judge || settings.Servers[0].Provider != "jev" {
		t.Fatalf("the provider list: %+v", settings.Servers)
	}
	into(t, c.ok("credentials.list", map[string]any{}), &list)
	if !slices.Contains(list.Credentials[0].UsedBy, "the judge") {
		t.Fatalf("the Jev key's users: %v", list.Credentials[0].UsedBy)
	}

	for name, tc := range map[string]struct {
		method string
		params map[string]any
		want   string
	}{
		"removing the judge's provider": {"settings.provider.remove", map[string]any{"name": "jev"}, "still used by the judge"},
		"a model on Jev":                {"settings.model.write", map[string]any{"server": "jev", "model": "jev-latest"}, "no model can be on it"},
		"a Jev dialect":                 {"settings.provider.write", map[string]any{"name": "jev-2", "provider": "jev", "url": "https://api.typesafe.ai", "dialect": "responses"}, "takes no dialect"},
		"a judge on no provider":        {"settings.write", map[string]any{"set": map[string]any{"judge.server": "nowhere"}}, "not configured"},
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
}

// A judge on an OpenAI server sends that server's key, already stored for
// its models, and is ready only once the key is allowed for the server's
// host, as Jev's was: a key allowed elsewhere is refused by the guard on the
// judge's first request. It asks gpt-6-luna and is priced at the Decisions
// API's input price, with no output price. The settings are read as JSON,
// as the console reads them.
func TestTheJudgeOnOpenAIUsesItsServersKey(t *testing.T) {
	o := options(t)
	raw := "[servers.openai-api]\nprovider = \"openai\"\nurl = \"https://api.openai.com/v1\"\ncredential = \"model-api-openai-com\"\n\n[judge]\nserver = \"openai-api\"\n"
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
	current := func() map[string]any {
		var settings map[string]any
		if err := json.Unmarshal(c.ok("settings.read", map[string]any{}), &settings); err != nil {
			t.Fatal(err)
		}
		return settings
	}
	if s := current(); s["judge"] != false || s["judge_key"] != "needs_key" {
		t.Fatalf("no key: judge %v, judge_key %v", s["judge"], s["judge_key"])
	}
	var stored credentialsResult
	into(t, c.ok("credentials.add", map[string]any{"class": "model-api-openai-com", "value": "sk-openai-judge-0002",
		"dest": "elsewhere.example", "methods": []string{"POST"}, "position": "header:Authorization:Bearer"}), &stored)
	if s := current(); s["judge"] != false || s["judge_key"] != "unbound" || !strings.Contains(fmt.Sprint(s["judge_key_detail"]), "api.openai.com") {
		t.Fatalf("a key for another host: judge %v, judge_key %v (%v)", s["judge"], s["judge_key"], s["judge_key_detail"])
	}
	c.ok("credentials.bind", map[string]any{"id": stored.Credentials[0].ID, "dest": "api.openai.com", "methods": []string{"POST"}, "position": "header:Authorization:Bearer"})
	s := current()
	published, _ := s["judge_published"].(map[string]any)
	if s["judge"] != true || s["judge_key"] != "ready" || s["judge_host"] != "api.openai.com" || s["judge_model"] != "gpt-6-luna" || published["price_input"] != 0.1 || published["price_output"] != 0.0 {
		t.Fatalf("the judge on OpenAI: judge %v key %v host %v model %v published %v", s["judge"], s["judge_key"], s["judge_host"], s["judge_model"], published)
	}
	var list credentialsResult
	into(t, c.ok("credentials.list", map[string]any{}), &list)
	if len(list.Credentials) != 1 || !slices.Equal(list.Credentials[0].UsedBy, []string{"server openai-api", "the judge"}) {
		t.Fatalf("the OpenAI key's users: %+v", list.Credentials)
	}
}
