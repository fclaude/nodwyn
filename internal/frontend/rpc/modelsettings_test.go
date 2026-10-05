package rpc

import (
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/fclaude/nodwyn/internal/catalogue"
	"github.com/fclaude/nodwyn/internal/config"
)

// A model's settings are its own (ADR 41, amended 2026-10-05): what is set
// for one model in the list is not set for another, follows the model when it
// becomes the default, and stays with it when another becomes the default.
// The [control] that names the default holds none of them: a copy there would
// go stale beside the entry. A copy written there before is moved to the entry
// the first time the model is saved, and nothing set is lost.
func TestAModelsSettingsAreItsOwn(t *testing.T) {
	o := options(t)
	raw := `[servers.openai]
provider = "openai"
url = "https://api.openai.com/v1"

[control]
server = "openai"
model = "gpt-6.1-sol"
vision = false
price_output = 40.0

[models.openai-gpt-6-1-sol]
server = "openai"
model = "gpt-6.1-sol"
efforts = ["low", "high"]

[models.openai-gpt-6-luna]
server = "openai"
model = "gpt-6-luna"
`
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
	saved := func() config.Config {
		t.Helper()
		cfg, err := config.Parse([]byte(read(t, o.Paths.ConfigFile)))
		if err != nil {
			t.Fatal(err)
		}
		return cfg
	}

	var settings settingsResult
	into(t, c.ok("settings.model.write", map[string]any{"server": "openai", "model": "gpt-6.1-sol", "efforts": []string{"low", "high"},
		"settings": map[string]any{"num_ctx": 1050000, "price_input": 5.0, "price_cached_input": 0.5, "price_output": 40.0, "vision": false}}), &settings)
	sol := choice(t, settings, "control").Settings
	if sol.NumCtx != 1050000 || sol.Vision == nil || *sol.Vision || sol.PriceInput == nil || *sol.PriceInput != 5 || sol.PriceCachedInput == nil || *sol.PriceCachedInput != 0.5 || sol.PriceOutput == nil || *sol.PriceOutput != 40 {
		t.Fatalf("the default model's settings: %+v", sol)
	}
	if luna := choice(t, settings, "openai-gpt-6-luna").Settings; luna != (modelSettings{}) {
		t.Fatalf("another model took them: %+v", luna)
	}
	cfg := saved()
	if m := cfg.Models["openai-gpt-6-1-sol"]; m.NumCtx != 1050000 || m.TakesImages() || m.PriceOutput == nil {
		t.Fatalf("not saved in the model's entry:\n%s", read(t, o.Paths.ConfigFile))
	}
	if ctl := cfg.Control; !reflect.DeepEqual(ctl, ctl.Choice()) || ctl.Model != "gpt-6.1-sol" {
		t.Fatalf("[control] keeps a copy of the settings:\n%s", read(t, o.Paths.ConfigFile))
	}

	// The settings stay with the model when another becomes the default, and
	// the new default gets none of them.
	into(t, c.ok("settings.control.write", map[string]any{"name": "openai-gpt-6-luna"}), &settings)
	if s := choice(t, settings, "control").Settings; s != (modelSettings{}) {
		t.Fatalf("the new default took the old one's settings: %+v", s)
	}
	if s := choice(t, settings, "openai-gpt-6-1-sol").Settings; s.NumCtx != 1050000 || s.Vision == nil {
		t.Fatalf("the model lost its settings when another became the default: %+v", s)
	}
	// And come back with it.
	into(t, c.ok("settings.control.write", map[string]any{"name": "openai-gpt-6-1-sol"}), &settings)
	if s := choice(t, settings, "control").Settings; s.NumCtx != 1050000 || s.PriceInput == nil {
		t.Fatalf("the default does not have its entry's settings: %+v", s)
	}
	if ctl := saved().Control; !reflect.DeepEqual(ctl, ctl.Choice()) {
		t.Fatalf("choosing the default copied its settings into [control]:\n%s", read(t, o.Paths.ConfigFile))
	}

	// A setting it does not know is refused, not dropped.
	if r := c.call("settings.model.write", map[string]any{"server": "openai", "model": "gpt-6-luna", "settings": map[string]any{"context": 9}}); r.Error == nil || !strings.Contains(r.Error.Message, "context") {
		t.Fatalf("an unknown setting: %+v", r.Error)
	}
}

// A [control] that holds settings of its own, as one the console's former
// Default model tuning wrote, gives them to the model's entry the first time
// the model is saved, filling only what the entry leaves unset.
func TestTheDefaultsOwnSettingsMoveToItsEntry(t *testing.T) {
	o := options(t)
	raw := `[servers.openai]
provider = "openai"
url = "https://api.openai.com/v1"

[control]
server = "openai"
model = "gpt-6.1-sol"
vision = false
num_ctx = 200000
price_output = 40.0

[models.openai-gpt-6-1-sol]
server = "openai"
model = "gpt-6.1-sol"
num_ctx = 1050000
`
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
	var settings settingsResult
	into(t, c.ok("settings.model.write", map[string]any{"server": "openai", "model": "gpt-6.1-sol"}), &settings)
	cfg, err := config.Parse([]byte(read(t, o.Paths.ConfigFile)))
	if err != nil {
		t.Fatal(err)
	}
	if m := cfg.Models["openai-gpt-6-1-sol"]; m.TakesImages() || m.NumCtx != 1050000 || m.PriceOutput == nil || *m.PriceOutput != 40 {
		t.Fatalf("the entry after the move: %+v\n%s", m, read(t, o.Paths.ConfigFile))
	}
	if !reflect.DeepEqual(cfg.Control, cfg.Control.Choice()) {
		t.Fatalf("[control] kept its copy:\n%s", read(t, o.Paths.ConfigFile))
	}
	if s := choice(t, settings, "control").Settings; s.Vision == nil || *s.Vision || s.NumCtx != 1050000 || s.PriceOutput == nil {
		t.Fatalf("the default's settings after the move: %+v", s)
	}
}

// Choosing another default keeps what [control] held for the one it replaces,
// in that model's entry, where only what the entry leaves unset is filled.
func TestChoosingAnotherDefaultKeepsWhatTheOldOneHeld(t *testing.T) {
	o := options(t)
	raw := `[servers.cerebras]
provider = "openai"
dialect = "cerebras"
url = "https://api.cerebras.ai/v1"

[control]
server = "cerebras"
model = "qwen-3.8-27b"
num_ctx = 131072
price_input = 0.6

[models.cerebras-qwen-3-8-27b]
server = "cerebras"
model = "qwen-3.8-27b"
price_input = 0.5
price_output = 1.2

[models.cerebras-other]
server = "cerebras"
model = "other"
`
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
	var settings settingsResult
	into(t, c.ok("settings.control.write", map[string]any{"name": "cerebras-other"}), &settings)
	s := choice(t, settings, "cerebras-qwen-3-8-27b").Settings
	if s.NumCtx != 131072 || s.PriceInput == nil || *s.PriceInput != 0.5 || s.PriceOutput == nil || *s.PriceOutput != 1.2 {
		t.Fatalf("the old default's entry: %+v\n%s", s, read(t, o.Paths.ConfigFile))
	}
}

// A model listed twice, here once with a smaller context, is two sets of
// settings: the default takes the first entry's, so a change to the second
// moves nothing of [control]'s into it.
func TestASecondEntryForTheDefaultTakesNothingOfIt(t *testing.T) {
	o := options(t)
	raw := `[servers.openai]
provider = "openai"
url = "https://api.openai.com/v1"

[control]
server = "openai"
model = "gpt-6.1-sol"
price_output = 40.0

[models.a-sol]
server = "openai"
model = "gpt-6.1-sol"

[models.b-small-sol]
server = "openai"
model = "gpt-6.1-sol"
num_ctx = 100000
`
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
	c.ok("settings.model.write", map[string]any{"name": "b-small-sol", "server": "openai", "model": "gpt-6.1-sol", "settings": map[string]any{"num_ctx": 90000}})
	cfg, err := config.Parse([]byte(read(t, o.Paths.ConfigFile)))
	if err != nil {
		t.Fatal(err)
	}
	if small := cfg.Models["b-small-sol"]; small.NumCtx != 90000 || small.PriceOutput != nil {
		t.Fatalf("the second entry: %+v", small)
	}
	if cfg.Control.PriceOutput == nil || *cfg.Control.PriceOutput != 40 {
		t.Fatalf("[control] lost what it held to an entry it does not read:\n%s", read(t, o.Paths.ConfigFile))
	}
}

// Each model in settings.read carries what its provider publishes, and its
// efforts are the published ones when its entry declares none;
// models.published says the same of a model not yet in the list, for the
// form adding it, and null of one the catalogue does not describe.
func TestModelsCarryWhatTheirProviderPublishes(t *testing.T) {
	o := options(t)
	cfg, err := config.Parse([]byte(`[servers.chat]
provider = "openai"
url = "http://127.0.0.1:1/v1"

[models.chat-sol]
server = "chat"
model = "sol"

[models.chat-small]
server = "chat"
model = "sol"
num_ctx = 32768
`))
	if err != nil {
		t.Fatal(err)
	}
	read, write := 0.1, 2.5
	cfg.Catalogue = catalogue.New(map[string]map[string]catalogue.Model{"127.0.0.1": {"sol": {
		Context: 1050000, Output: 128000, Efforts: []string{"low", "medium", "high", "xhigh", "max"}, Images: true,
		Price: &catalogue.Price{Input: 2, Output: 10, CacheRead: &read, CacheWrite: &write, Tiers: []catalogue.Tier{{Above: 272000, Input: 4, Output: 15}}},
	}}})
	cfg.Approval = o.Config.Approval
	o.Config = cfg
	path, _ := start(t, o)
	c := connect(t, path)
	c.hello()
	var settings settingsResult
	into(t, c.ok("settings.read", map[string]any{}), &settings)
	byName := map[string]modelChoice{}
	for _, choice := range settings.Choices {
		byName[choice.Name] = choice
	}
	efforts := []string{"low", "medium", "high", "xhigh", "max"}
	in, out := 2.0, 10.0
	want := publishedModel{NumCtx: 1050000, MaxOutput: 128000, Efforts: efforts, Images: true, PriceInput: &in, PriceCachedInput: &read, PriceCacheWrite: &write, PriceOutput: &out, TierAbove: 272000}
	sol := byName["chat-sol"]
	if sol.Published == nil || !reflect.DeepEqual(*sol.Published, want) || !reflect.DeepEqual(sol.Efforts, efforts) || sol.Settings.NumCtx != 0 {
		t.Fatalf("chat-sol: %+v published %+v", sol, sol.Published)
	}
	// An entry with a context of its own is given a quarter of it to reply.
	if small := byName["chat-small"]; small.Published == nil || small.Published.MaxOutput != 8192 || small.Settings.NumCtx != 32768 {
		t.Fatalf("chat-small: %+v published %+v", small, small.Published)
	}
	var found *publishedModel
	into(t, c.ok("models.published", map[string]any{"server": "chat", "model": "sol"}), &found)
	if found == nil || !reflect.DeepEqual(*found, want) {
		t.Fatalf("models.published: %+v", found)
	}
	into(t, c.ok("models.published", map[string]any{"server": "chat", "model": "unknown"}), &found)
	if found != nil {
		t.Fatalf("a model nothing is published of: %+v", found)
	}
	if r := c.call("models.published", map[string]any{"server": "nowhere", "model": "sol"}); r.Error == nil {
		t.Fatal("an unconfigured server was answered")
	}
}
