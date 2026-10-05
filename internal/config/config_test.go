package config

import (
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/fclaude/nodwyn/internal/budget"

	"github.com/fclaude/nodwyn/internal/catalogue"
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
		"bad policy":        "[policy]\nunbound = \"allow\"\n",
		"unknown key":       "[control]\napikey = \"x\"\n",
		"reply cap >= ctx":  "[control]\nprovider = \"ollama\"\nurl = \"http://localhost:11434\"\nmodel = \"m\"\nnum_ctx = 32768\nmax_output = 32768\n",
		"low >= high":       "[window]\nlow_water = 70000\n",
		"unknown provider":  "[control]\nprovider = \"anthropic\"\nurl = \"https://a.test\"\nmodel = \"m\"\nnum_ctx = 8192\n",
		"partial control":   "[control]\nmodel = \"m\"\n",
		"models.control":    "[models.control]\nprovider = \"ollama\"\nurl = \"http://localhost:11434\"\nmodel = \"m\"\n",
		"docs plain http":   "[docs]\ngoogle_upload_url = \"http://upload.example.com/files\"\n",
		"docs query":        "[docs]\ngoogle_upload_url = \"https://www.googleapis.com/upload/drive/v3/files?uploadType=media\"\n",
		"docs relative":     "[docs]\ngoogle_upload_url = \"/upload/drive/v3/files\"\n",
		"approval writes":   "[approval]\nwrites = \"judge\"\n",
		"approval commands": "[approval]\ncommands = \"yes\"\n",
		"approval unknown":  "[approval]\nreads = \"ask\"\n",
	}
	for name, body := range cases {
		p := filepath.Join(t.TempDir(), "config.toml")
		os.WriteFile(p, []byte(body), 0o600)
		if _, err := Load(p); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// [tasks] chose how the console started a task; a file that still has it
// loads, and the table is ignored (ADR 35).
func TestTasksTableIsIgnored(t *testing.T) {
	cfg, err := Parse([]byte("[tasks]\nmode = \"unattended\"\napproval = \"yes\"\nturns = 20\nwall = \"2h\"\n"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Approval != Default().Approval {
		t.Fatalf("approval changed by [tasks]: %+v", cfg.Approval)
	}
	if _, err := Parse([]byte("[tasks]\nmode = \"chat\"\n[policy]\nnonsense = 1\n")); err == nil {
		t.Fatal("an unknown key outside [tasks] was accepted")
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
	if err != nil || limits.ToolCalls.Soft != 500 || limits.ToolCalls.Hard != 500 || limits.Cost != (budget.Limit{}) {
		t.Fatalf("defaults: %+v %v", limits, err)
	}
	// A soft limit left unset follows its hard one (ADR 35): lowering a
	// hard limit is valid on its own, and no checkpoint comes before it.
	os.WriteFile(path, []byte("[budget]\ntool_calls_hard=100\nwall_hard=\"1h\"\nactions_soft=20\n"), 0600)
	if cfg, err = Load(path); err != nil {
		t.Fatal(err)
	}
	limits, err = cfg.Budget.Limits(0)
	if err != nil || limits.ToolCalls != (budget.Limit{Soft: 100, Hard: 100}) || limits.Wall != (budget.Limit{Soft: int64(time.Hour), Hard: int64(time.Hour)}) || limits.Actions != (budget.Limit{Soft: 20, Hard: 200}) {
		t.Fatalf("soft follows hard: %+v %v", limits, err)
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
cache_key = "header"

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
	if m := c.Models["impl"]; m.Provider != "openai" || m.Credential != "example-key" || m.Model != "coder-large" || m.MaxOutput != 32768 || m.OutputField != "max_tokens" || m.CacheKey != "header" {
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
		"[models.x]\nprovider = \"openai\"\nurl = \"https://a.test\"\nmodel = \"m\"\ncache_key = \"body\"\n",
		"[models.x]\nprovider = \"ollama\"\nurl = \"http://a.test\"\nmodel = \"m\"\ncache_key = \"off\"\n",
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
	if !Default().Control.isZero() {
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
// marks, shrinks to fit a small one whether they are set or not, and the
// room above the high water is the ceiling.
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
		{"32K", def, 32768, 8192, 18432, 9216, 24576},
		{"32K, water marks set", parse("[window]\nhigh_water = 50000\nlow_water = 20000\n"), 32768, 8192, 18432, 9216, 24576},
		{"no room beside the reply", def, 16384, 16384, 61440, 30720, 61440},
	} {
		w, ceiling := tc.w.Sized(tc.context, tc.reserve)
		if w.HighWater != tc.high || w.LowWater != tc.low || ceiling != tc.ceiling {
			t.Errorf("%s: high %d, low %d, ceiling %d; want %d, %d, %d", tc.name, w.HighWater, w.LowWater, ceiling, tc.high, tc.low, tc.ceiling)
		}
	}
}

// A model's reply cap is its own max_output, else the window's
// output_reserve, but no more than a quarter of a context too small for the
// window and that reply; one that leaves nothing of the context is refused.
func TestReplyCap(t *testing.T) {
	w := Default().Window
	small := w
	small.HighWater, small.LowWater = 9000, 4500
	for _, tc := range []struct {
		w                        Window
		context, maxOutput, want int
	}{
		{w, 0, 0, 16384},
		{w, 1050000, 0, 16384},
		{w, 32768, 0, 8192},
		{w, 50000, 0, 12500},
		{w, 1050000, 128000, 128000},
		{w, 32768, 20000, 20000},
		// An Ollama model asked for the window and a reply keeps the reply
		// it was asked for, however small the window was set.
		{small, small.HighWater + small.OutputReserve, 0, 16384},
	} {
		if got := tc.w.ReplyCap(tc.context, tc.maxOutput); got != tc.want {
			t.Errorf("context %d, max_output %d: cap %d, want %d", tc.context, tc.maxOutput, got, tc.want)
		}
	}
	if got := Default().WindowNumCtx(128000); got != 61440+128000 {
		t.Errorf("an unsized ollama model with a 128000-token cap is asked for %d tokens", got)
	}
	c := Default()
	c.Models = map[string]Model{"small": {URL: "http://localhost:11434", Model: "m", NumCtx: 32768, MaxOutput: 32768}}
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "max_output (32768) must be below num_ctx (32768)") {
		t.Fatalf("a cap that leaves nothing: %v", err)
	}
	// A context smaller than the window is no longer refused: the window
	// shrinks to fit it (Sized).
	c.Models = nil
	c.Control = Model{URL: "http://localhost:11434", Model: "m", NumCtx: 32768}
	if err := c.Validate(); err != nil {
		t.Fatalf("a 32K control model: %v", err)
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

// What a model's entry leaves unset is what its provider publishes: its
// context, its reply cap (the published limit, at most a quarter of the
// context), whether it reads images, and its efforts. What the entry sets
// stands, and a model at an address the catalogue does not describe, or on
// Ollama, borrows nothing.
func TestAModelHasWhatItsProviderPublishes(t *testing.T) {
	c, err := Parse([]byte(`
[servers.openai]
provider = "openai"
url = "https://api.openai.com/v1"

[servers.local]
url = "http://localhost:11434"

[models.sol]
server = "openai"
model = "sol"

[models.small-sol]
server = "openai"
model = "sol"
num_ctx = 32768

[models.blind]
server = "openai"
model = "blind"

[models.local]
server = "local"
model = "sol"
`))
	if err != nil {
		t.Fatal(err)
	}
	c.Catalogue = catalogue.New(map[string]map[string]catalogue.Model{"api.openai.com": {
		"sol":   {Context: 1050000, Output: 128000, Efforts: []string{"low", "medium", "high", "xhigh", "max"}, Images: true},
		"blind": {Context: 131072, Output: 131072, Efforts: []string{"low", "high"}},
	}})
	sol := c.WithPublished(c.Models["sol"])
	if sol.NumCtx != 1050000 || sol.MaxOutput != 128000 || !sol.TakesImages() || !slices.Equal(c.Efforts(c.Models["sol"]), []string{"low", "medium", "high", "xhigh", "max"}) {
		t.Errorf("sol: %+v, efforts %v", sol, c.Efforts(c.Models["sol"]))
	}
	small := c.WithPublished(c.Models["small-sol"])
	if small.NumCtx != 32768 || small.MaxOutput != 8192 {
		t.Errorf("an entry's own context: %+v", small)
	}
	if got := c.Efforts(Model{Server: "openai", Model: "sol", Efforts: []string{"high"}}); !slices.Equal(got, []string{"high"}) {
		t.Errorf("a model's own efforts: %v", got)
	}
	blind := c.WithPublished(c.Models["blind"])
	if blind.MaxOutput != 131072/4 || blind.TakesImages() {
		t.Errorf("a model whose reply limit is its whole context, and that reads no images: %+v", blind)
	}
	// An effort is checked against the published efforts.
	blindEntry := c.Models["blind"]
	blindEntry.Effort = "low"
	c.Models["blind"] = blindEntry
	if err := c.Validate(); err != nil {
		t.Fatalf("an effort the provider publishes: %v", err)
	}
	blindEntry.Effort = "xhigh"
	c.Models["blind"] = blindEntry
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "not one the model takes (low, high)") {
		t.Fatalf("an effort the provider does not publish: %v", err)
	}
	delete(c.Models, "blind")
	if local := c.WithPublished(c.Models["local"]); local.NumCtx != 0 || c.Efforts(c.Models["local"]) != nil {
		t.Errorf("an Ollama model borrowed from the catalogue: %+v", local)
	}
	if _, ok := c.Published(Model{Provider: "openai", URL: "https://openai.example.com/v1", Model: "sol"}); ok {
		t.Error("another host was described")
	}
}

// Each of the judge's prices is [jev]'s when it sets it, else what its
// provider publishes for jev.model at jev.url; with either known to neither,
// none: its requests are then counted, not priced. A price set alone was
// dropped for the published pair, though Settings says an empty field is
// the published price.
func TestJevPrices(t *testing.T) {
	c := Default()
	c.Catalogue = catalogue.New(map[string]map[string]catalogue.Model{"api.typesafe.ai": {"jev-latest": {Price: &catalogue.Price{Input: 0.042}}}})
	if in, out := c.JevPrices(); in == nil || *in != 0.042 || out == nil || *out != 0 {
		t.Fatalf("published: %v %v", in, out)
	}
	five, six := 5.0, 6.0
	c.Jev.PriceInput, c.Jev.PriceOutput = &five, &six
	if in, out := c.JevPrices(); in != &five || out != &six {
		t.Fatalf("[jev]'s own: %v %v", in, out)
	}
	c.Jev.PriceOutput = nil
	if in, out := c.JevPrices(); in != &five || out == nil || *out != 0 {
		t.Fatalf("[jev]'s input alone: %v %v", in, out)
	}
	c.Jev.PriceInput, c.Jev.PriceOutput = nil, nil
	c.Jev.Model = "jev-9"
	if in, out := c.JevPrices(); in != nil || out != nil {
		t.Fatalf("a model nothing is published of: %v %v", in, out)
	}
	c.Jev.PriceOutput = &six
	if in, out := c.JevPrices(); in != nil || out != nil {
		t.Fatalf("an output price alone, nothing published: %v %v", in, out)
	}
}

// A model's rates are each of its entry's prices when set, else the
// published one; the published cache writes and long-prompt tiers apply only
// while the entry sets no price.
func TestModelRates(t *testing.T) {
	c := Default()
	read, write := 0.25, 3.0
	c.Catalogue = catalogue.New(map[string]map[string]catalogue.Model{"api.openai.com": {"sol": {
		Price: &catalogue.Price{Input: 2, Output: 10, CacheRead: &read, CacheWrite: &write, Tiers: []catalogue.Tier{{Above: 272000, Input: 4, Output: 15}}},
	}}})
	m := Model{Provider: "openai", URL: "https://api.openai.com/v1", Model: "sol"}
	if r := c.Rates(m); *r.Input != 2 || *r.Output != 10 || *r.CachedInput != 0.25 || *r.CacheWrite != 3 || len(r.Tiers) != 1 {
		t.Fatalf("as published: %+v", r)
	}
	in := 1.5
	m.PriceInput = &in
	if r := c.Rates(m); *r.Input != 1.5 || *r.Output != 10 || *r.CachedInput != 0.25 || r.CacheWrite != nil || r.Tiers != nil {
		t.Fatalf("an input price set alone: %+v", r)
	}
	m.URL = "https://example.com/v1"
	if r := c.Rates(m); r.Known() {
		t.Fatalf("an input price alone, nothing published: %+v", r)
	}
}
