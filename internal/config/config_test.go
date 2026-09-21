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
		"budget > ctx":     "[window]\ninput_budget = 200000\n",
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

func TestBindings(t *testing.T) {
	dir := t.TempDir()
	if b, err := LoadBindings(filepath.Join(dir, "none.toml")); err != nil || len(b.Bind) != 0 {
		t.Fatalf("missing bindings: %v %+v", err, b)
	}
	p := filepath.Join(dir, "bindings.toml")
	os.WriteFile(p, []byte(`
[[bind]]
class = "jev-key"
dest = "api.typesafe.ai"
positions = ["header:Authorization"]
methods = ["POST"]
`), 0o600)
	b, err := LoadBindings(p)
	if err != nil || len(b.Bind) != 1 || b.Bind[0].Class != "jev-key" {
		t.Fatalf("bindings: %v %+v", err, b)
	}
	os.WriteFile(p, []byte("[[bind]]\nclass = \"x\"\n"), 0o600)
	if _, err := LoadBindings(p); err == nil || !strings.Contains(err.Error(), "required") {
		t.Fatalf("incomplete row accepted: %v", err)
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
