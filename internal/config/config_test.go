package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestDefaultIsValid(t *testing.T) {
	if err := Default().Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestLoadMissingIsDefault(t *testing.T) {
	c, err := Load(filepath.Join(t.TempDir(), "nope.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(c, Default()) {
		t.Fatalf("missing file should yield defaults, got %+v", c)
	}
}

func TestLoadOverridesAndValidates(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.toml")
	os.WriteFile(p, []byte(`
[control]
model = "qwen3.6-27b-150k:latest"
num_ctx = 150000

[policy]
unbound = "confirm"
`), 0o600)
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if c.Control.Model != "qwen3.6-27b-150k:latest" || c.Control.NumCtx != 150000 {
		t.Fatalf("override not applied: %+v", c.Control)
	}
	if c.Control.URL != Default().Control.URL {
		t.Fatal("unset key should keep default")
	}
	if c.Policy.Unbound != "confirm" {
		t.Fatal("policy override")
	}
}

func TestLoadRejectsBadValues(t *testing.T) {
	cases := map[string]string{
		"bad policy":       "[policy]\nunbound = \"allow\"\n",
		"unknown key":      "[control]\napikey = \"x\"\n",
		"budget > ctx":     "[window]\nhigh_water = 200000\n",
		"low >= high":      "[window]\nlow_water = 70000\n",
		"unknown provider": "[control]\nprovider = \"anthropic\"\n",
	}
	for name, body := range cases {
		p := filepath.Join(t.TempDir(), "config.toml")
		os.WriteFile(p, []byte(body), 0o600)
		if _, err := Load(p); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestPathsUnder(t *testing.T) {
	p := PathsUnder("/c/nibb", "/d/nibb")
	if p.SessionsDir != "/d/nibb/sessions" || p.Keychain != "/c/nibb/keychain" {
		t.Fatalf("%+v", p)
	}
	t.Setenv("XDG_CONFIG_HOME", "/xc")
	t.Setenv("XDG_DATA_HOME", "/xd")
	dp, err := DefaultPaths()
	if err != nil || dp.ConfigFile != "/xc/nibb/config.toml" || dp.SessionsDir != "/xd/nibb/sessions" {
		t.Fatalf("%v %+v", err, dp)
	}
}

func TestBudgetConfigurationAndLegacyOverride(t *testing.T) {
	for _, body := range []string{
		"[budget]\nwall_hard=\"bad\"\n",
		"[budget]\ntokens_soft=0\n",
		"[budget]\nactions_soft=10\nactions_hard=2\n",
		"[loop]\nmax_tool_calls=0\n",
		"[loop]\nmax_tool_calls=3\n[budget]\ntool_calls_hard=4\n",
	} {
		path := filepath.Join(t.TempDir(), "config.toml")
		os.WriteFile(path, []byte(body), 0600)
		if _, err := Load(path); err == nil {
			t.Fatalf("accepted invalid config %s", body)
		}
	}
	path := filepath.Join(t.TempDir(), "config.toml")
	os.WriteFile(path, []byte("[loop]\nmax_tool_calls=3\n"), 0600)
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	limits, err := cfg.Budget.Limits(cfg.Loop.MaxToolCalls)
	if err != nil || limits.ToolCalls.Hard != 3 || limits.ToolCalls.Soft != 3 {
		t.Fatalf("legacy limit: %+v %v", limits, err)
	}
	defaults := Default()
	limits, err = defaults.Budget.Limits(defaults.Loop.MaxToolCalls)
	if err != nil || limits.ToolCalls.Soft != 50 || limits.ToolCalls.Hard != 500 {
		t.Fatalf("defaults: %+v %v", limits, err)
	}
}

func TestModelsRegistry(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	os.WriteFile(path, []byte(`
[models.impl]
provider = "openai"
url = "https://api.example.test/v1"
model = "coder-large"
credential = "example-key"

[models.local]
provider = "ollama"
url = "http://localhost:11434"
model = "qwen:small"
num_ctx = 32768
`), 0o600)
	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if m := c.Models["impl"]; m.Provider != "openai" || m.Credential != "example-key" || m.Model != "coder-large" {
		t.Fatalf("impl: %+v", m)
	}
	if c.Models["local"].NumCtx != 32768 {
		t.Fatalf("local: %+v", c.Models["local"])
	}
	for _, bad := range []string{
		"[models.x]\nprovider = \"gpt\"\nurl = \"https://a.test\"\nmodel = \"m\"\n",
		"[models.x]\nprovider = \"openai\"\nurl = \"ftp://a.test\"\nmodel = \"m\"\n",
		"[models.x]\nprovider = \"openai\"\nurl = \"https://a.test\"\n",
	} {
		os.WriteFile(path, []byte(bad), 0o600)
		if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "models.x") {
			t.Fatalf("accepted %q: %v", bad, err)
		}
	}
}
