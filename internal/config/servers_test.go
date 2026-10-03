package config

import (
	"strings"
	"testing"
)

// A model that names a server is reached at the server's address with the
// server's key, so the models one provider offers are a line or two each.
func TestModelsNameTheirServer(t *testing.T) {
	c, err := Parse([]byte(`
[servers.openrouter]
provider = "openai"
url = "https://openrouter.ai/api/v1"
credential = "openrouter-key"

[servers.workshop]
url = "http://workshop:11434"

[control]
server = "openrouter"
model = "meta/muse-spark-1.3"

[models.coder]
server = "openrouter"
model = "qwen/qwen3-coder"
price_input = 0.3

[models.local]
server = "workshop"
model = "qwen3:8b"
`))
	if err != nil {
		t.Fatal(err)
	}
	want := Model{Server: "openrouter", Provider: "openai", URL: "https://openrouter.ai/api/v1", Credential: "openrouter-key", Model: "meta/muse-spark-1.3"}
	if c.Control != want {
		t.Fatalf("control: %+v", c.Control)
	}
	if m := c.Models["coder"]; m.URL != want.URL || m.Credential != "openrouter-key" || m.Provider != "openai" || m.Model != "qwen/qwen3-coder" || m.PriceInput == nil {
		t.Fatalf("coder: %+v", m)
	}
	if m := c.Models["local"]; m.URL != "http://workshop:11434" || m.Provider != "" || m.Credential != "" {
		t.Fatalf("local: %+v", m)
	}
}

func TestServerRefusals(t *testing.T) {
	for name, tc := range map[string]struct{ toml, want string }{
		"unknown server": {"[control]\nserver = \"nowhere\"\nmodel = \"m\"\n", `control: server "nowhere" is not configured`},
		"url twice":      {"[servers.s]\nurl = \"http://a:1\"\n[models.x]\nserver = \"s\"\nurl = \"http://b:1\"\nmodel = \"m\"\n", `models.x: url comes from server "s"`},
		"key twice":      {"[servers.s]\nprovider = \"openai\"\nurl = \"https://a\"\n[control]\nserver = \"s\"\ncredential = \"k\"\nmodel = \"m\"\n", `control: credential comes from server "s"`},
		"unused bad url": {"[servers.s]\nurl = \"ftp://a\"\n", `servers.s: url "ftp://a" must be an http or https URL`},
		"ollama key":     {"[servers.s]\nurl = \"http://a:1\"\ncredential = \"k\"\n", "servers.s: credential is not supported for ollama"},
		"no model":       {"[servers.s]\nurl = \"http://a:1\"\n[models.x]\nserver = \"s\"\n", "models.x: model is required"},
	} {
		if _, err := Parse([]byte(tc.toml)); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// The console saves a server once and the control model by the server's
// name; a model read back from the file is written back the same way.
func TestSetServerAndControlByName(t *testing.T) {
	raw := []byte("# mine\n[servers.openrouter]\nurl = \"http://old:1\"\n\n[window]\nhigh_water = 50000\n")
	raw, _, err := SetServer(raw, "openrouter", Server{Provider: "openai", URL: "https://openrouter.ai/api/v1", Credential: "model-openrouter-ai"})
	if err != nil {
		t.Fatal(err)
	}
	raw, cfg, err := SetControl(raw, Model{Server: "openrouter", Model: "a"})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Control.URL != "https://openrouter.ai/api/v1" || cfg.Control.Credential != "model-openrouter-ai" {
		t.Fatalf("control: %+v", cfg.Control)
	}
	// What Parse copied from the server stays in the server.
	raw, cfg, err = SetControl(raw, Model{Server: cfg.Control.Server, Provider: cfg.Control.Provider, URL: cfg.Control.URL, Credential: cfg.Control.Credential, Model: "b", NumCtx: 200000})
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	want := "# mine\n[servers.openrouter]\nprovider = \"openai\"\nurl = \"https://openrouter.ai/api/v1\"\ncredential = \"model-openrouter-ai\"\n\n[window]\nhigh_water = 50000\n\n[control]\nserver = \"openrouter\"\nmodel = \"b\"\nnum_ctx = 200000\n\n"
	if text != want {
		t.Fatalf("config.toml:\n%s\nwant:\n%s", text, want)
	}
	if cfg.Control.Model != "b" || cfg.Control.URL != "https://openrouter.ai/api/v1" {
		t.Fatalf("control: %+v", cfg.Control)
	}
	if _, _, err := SetServer(nil, "two words", Server{URL: "http://a:1"}); err == nil {
		t.Fatal("accepted a server name with a space")
	}
	if _, _, err := SetServer(nil, "s", Server{URL: "http://a:1", Credential: "k"}); err == nil {
		t.Fatal("accepted a key for ollama")
	}
}
