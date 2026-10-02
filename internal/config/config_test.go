package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/fclaude/nodwyn/internal/budget"
)

func TestDefaultIsValid(t *testing.T) {
	if err := Default().Validate(); err != nil {
		t.Fatal(err)
	}
}

// [docs] names the google credential class and Drive's upload endpoint;
// the endpoint may be plain http only on this machine (a test's fake).
func TestDocsSettings(t *testing.T) {
	if d := Default().Docs; d.GoogleCredential != "google" || d.GoogleUploadURL != "https://www.googleapis.com/upload/drive/v3/files" {
		t.Fatalf("defaults: %+v", d)
	}
	c, err := Parse([]byte("[docs]\ngoogle_credential = \"work-google\"\ngoogle_upload_url = \"http://127.0.0.1:8123/upload/drive/v3/files\"\n"))
	if err != nil || c.Docs.GoogleCredential != "work-google" || c.Docs.GoogleUploadURL != "http://127.0.0.1:8123/upload/drive/v3/files" {
		t.Fatalf("%+v %v", c.Docs, err)
	}
	if c, err := Parse([]byte("[docs]\ngoogle_credential = \"\"\n")); err != nil || c.Docs.GoogleCredential != "" {
		t.Fatalf("no google tool: %+v %v", c.Docs, err)
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
provider = "ollama"
url = "http://localhost:11434"
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
	if c.Window != Default().Window {
		t.Fatal("an unset table should keep its defaults")
	}
	if c.Policy.Unbound != "confirm" {
		t.Fatal("policy override")
	}
}

func TestLoadRejectsBadValues(t *testing.T) {
	cases := map[string]string{
		"bad policy":       "[policy]\nunbound = \"allow\"\n",
		"unknown key":      "[control]\napikey = \"x\"\n",
		"budget > ctx":     "[control]\nprovider = \"ollama\"\nurl = \"http://localhost:11434\"\nmodel = \"m\"\nnum_ctx = 32768\n[window]\nhigh_water = 200000\n",
		"low >= high":      "[window]\nlow_water = 70000\n",
		"unknown provider": "[control]\nprovider = \"anthropic\"\nurl = \"https://a.test\"\nmodel = \"m\"\nnum_ctx = 8192\n",
		"partial control":  "[control]\nmodel = \"m\"\n",
		"models.control":   "[models.control]\nprovider = \"ollama\"\nurl = \"http://localhost:11434\"\nmodel = \"m\"\n",
		"docs plain http":  "[docs]\ngoogle_upload_url = \"http://upload.example.com/files\"\n",
		"docs query":       "[docs]\ngoogle_upload_url = \"https://www.googleapis.com/upload/drive/v3/files?uploadType=media\"\n",
		"docs relative":    "[docs]\ngoogle_upload_url = \"/upload/drive/v3/files\"\n",
		"tasks mode":       "[tasks]\nmode = \"background\"\n",
		"tasks approval":   "[tasks]\napproval = \"always\"\n",
		"tasks no turns":   "[tasks]\nturns = 0\n",
		"tasks many turns": "[tasks]\nturns = 10001\n",
		"tasks wall":       "[tasks]\nwall = \"forever\"\n",
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
	p := PathsUnder("/c/nodwyn", "/d/nodwyn")
	if p.SessionsDir != "/d/nodwyn/sessions" || p.Keychain != "/c/nodwyn/keychain" {
		t.Fatalf("%+v", p)
	}
	t.Setenv("XDG_CONFIG_HOME", "/xc")
	t.Setenv("XDG_DATA_HOME", "/xd")
	dp, err := DefaultPaths()
	if err != nil || dp.ConfigFile != "/xc/nodwyn/config.toml" || dp.SessionsDir != "/xd/nodwyn/sessions" {
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
		"[budget]\ncost_soft=1.0\n",
		"[budget]\ncost_soft=3.0\ncost_hard=2.0\n",
		"[budget]\ncost_hard=-1.0\n",
		"[budget]\ncost_hard=nan\n",
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
	if err != nil || limits.ToolCalls.Soft != 50 || limits.ToolCalls.Hard != 500 || limits.Cost != (budget.Limit{}) {
		t.Fatalf("defaults: %+v %v", limits, err)
	}
	// A spend limit, in dollars; soft unset is the hard limit.
	for body, want := range map[string]budget.Limit{
		"[budget]\ncost_soft=0.5\ncost_hard=2\n": {Soft: 500_000, Hard: 2_000_000},
		"[budget]\ncost_hard=2.25\n":             {Soft: 2_250_000, Hard: 2_250_000},
	} {
		os.WriteFile(path, []byte(body), 0600)
		cfg, err := Load(path)
		if err != nil {
			t.Fatal(err)
		}
		if limits, err := cfg.Budget.Limits(0); err != nil || limits.Cost != want {
			t.Fatalf("%s: %+v %v", body, limits.Cost, err)
		}
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
max_output = 32768
output_field = "max_tokens"

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
	if m := c.Models["impl"]; m.Provider != "openai" || m.Credential != "example-key" || m.Model != "coder-large" || m.MaxOutput != 32768 || m.OutputField != "max_tokens" {
		t.Fatalf("impl: %+v", m)
	}
	if c.Models["local"].NumCtx != 32768 {
		t.Fatalf("local: %+v", c.Models["local"])
	}
	for _, bad := range []string{
		"[models.x]\nprovider = \"gpt\"\nurl = \"https://a.test\"\nmodel = \"m\"\n",
		"[models.x]\nprovider = \"openai\"\nurl = \"ftp://a.test\"\nmodel = \"m\"\n",
		"[models.x]\nprovider = \"openai\"\nurl = \"https://a.test\"\n",
		"[models.x]\nprovider = \"openai\"\nurl = \"https://a.test\"\nmodel = \"m\"\noutput_field = \"n_predict\"\n",
		"[models.x]\nprovider = \"ollama\"\nurl = \"http://a.test\"\nmodel = \"m\"\noutput_field = \"max_tokens\"\n",
		"[models.x]\nprovider = \"openai\"\nurl = \"https://a.test\"\nmodel = \"m\"\nmax_output = -1\n",
		"[models.x]\nprovider = \"openai\"\nurl = \"https://a.test\"\nmodel = \"m\"\ntemperature = 2.5\n",
		"[models.x]\nprovider = \"ollama\"\nurl = \"http://a.test\"\nmodel = \"m\"\ntemperature = -0.1\n",
	} {
		os.WriteFile(path, []byte(bad), 0o600)
		if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "models.x") {
			t.Fatalf("accepted %q: %v", bad, err)
		}
	}
}

// There is no built-in control model: nodwyn names the model it talks to only
// once the user has, and a keyed provider may be the control model.
func TestControlIsConfiguredNotBuiltIn(t *testing.T) {
	if Default().Control != (Model{}) {
		t.Fatalf("a built-in control model: %+v", Default().Control)
	}
	err := MissingControl("/home/u/.config/nodwyn/config.toml")
	if !strings.Contains(err.Error(), "/home/u/.config/nodwyn/config.toml") || !strings.Contains(err.Error(), "[control]") {
		t.Fatalf("the error does not say what to add where: %v", err)
	}
	p := filepath.Join(t.TempDir(), "config.toml")
	os.WriteFile(p, []byte(`
[control]
provider = "openai"
url = "https://api.example.test/v1"
model = "coder-large"
num_ctx = 128000
credential = "example-key"
`), 0o600)
	c, err := Load(p)
	if err != nil || c.Control.Provider != "openai" || c.Control.Credential != "example-key" {
		t.Fatalf("keyed control: %+v %v", c.Control, err)
	}
}

// The window grows with a large context, unless config.toml sets its water
// marks, and the room above the high water is the ceiling.
func TestWindowSizedForTheContext(t *testing.T) {
	parse := func(src string) Window {
		t.Helper()
		c, err := Parse([]byte(src))
		if err != nil {
			t.Fatal(err)
		}
		return c.Window
	}
	def := Default().Window
	for _, tc := range []struct {
		name               string
		w                  Window
		context, reserve   int
		high, low, ceiling int
	}{
		{"unknown context", def, 0, 16384, 61440, 30720, 61440},
		{"ollama sized by the window", def, def.HighWater + def.OutputReserve, 16384, 61440, 30720, 61440},
		{"a little room", def, 81920, 16384, 61440, 30720, 65536},
		{"1M", def, 1048576, 16384, 774144, 387072, 1032192},
		{"1M, a larger reply cap", def, 1048576, 65536, 737280, 368640, 983040},
		{"1M, high water set", parse("[window]\nhigh_water = 100000\n"), 1048576, 16384, 100000, 30720, 1032192},
		{"1M, low water set", parse("[window]\nlow_water = 20000\n"), 1048576, 16384, 61440, 20000, 1032192},
		{"1M, other window keys", parse("[window]\nmax_result_tokens = 4096\n"), 1048576, 16384, 774144, 387072, 1032192},
	} {
		w, ceiling := tc.w.Sized(tc.context, tc.reserve)
		if w.HighWater != tc.high || w.LowWater != tc.low || ceiling != tc.ceiling {
			t.Errorf("%s: high %d, low %d, ceiling %d; want %d, %d, %d", tc.name, w.HighWater, w.LowWater, ceiling, tc.high, tc.low, tc.ceiling)
		}
	}
}

func TestModelPrices(t *testing.T) {
	cfg := Default()
	neg := -1.0
	cfg.Control = Model{Provider: "openai", URL: "https://openrouter.ai/api/v1", Model: "m", PriceInput: &neg}
	if err := cfg.Validate(); err == nil {
		t.Fatal("a negative price was accepted")
	}
	in, out := 3.0, 15.0
	m := Model{Provider: "openai", URL: "https://openrouter.ai/api/v1", Model: "m", PriceInput: &in, PriceOutput: &out}
	raw, parsed, err := SetControl(nil, m)
	if err != nil || parsed.Control.PriceInput == nil || *parsed.Control.PriceInput != 3 || parsed.Control.PriceCachedInput != nil {
		t.Fatalf("%s %+v %v", raw, parsed.Control, err)
	}
}
