package config

import (
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSetControlKeepsTheRestOfTheFile(t *testing.T) {
	raw := []byte(`# my settings
[window]
high_water = 50000 # tuned

[control]
url = "http://old:11434"
model = "old"

[models.small]
url = "http://old:11434"
model = "tiny"
`)
	out, cfg, err := SetControl(raw, Model{Provider: "ollama", URL: "http://workshop:11434", Model: "qwen3:27b"})
	if err != nil {
		t.Fatal(err)
	}
	text := string(out)
	for _, want := range []string{"# my settings", "high_water = 50000 # tuned", "[models.small]", `model = "tiny"`, `url = "http://workshop:11434"`, `model = "qwen3:27b"`} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in:\n%s", want, text)
		}
	}
	if strings.Contains(text, "old:11434\"\nmodel = \"old\"") || strings.Count(text, "[control]") != 1 {
		t.Fatalf("old control kept:\n%s", text)
	}
	if cfg.Control.Model != "qwen3:27b" || cfg.Window.HighWater != 50000 || cfg.Models["small"].Model != "tiny" {
		t.Fatalf("parsed: %+v", cfg)
	}
}

func TestSetKeyReplacesAddsAndValidates(t *testing.T) {
	raw := []byte(`# mine
[docs]
google_upload_url = "http://127.0.0.1:9/upload" # local fake

[window]
high_water = 50000
`)
	out, cfg, err := SetKey(raw, "docs", "google_upload", "allow")
	if err != nil {
		t.Fatal(err)
	}
	want := "# mine\n[docs]\ngoogle_upload_url = \"http://127.0.0.1:9/upload\" # local fake\ngoogle_upload = \"allow\"\n\n[window]\nhigh_water = 50000\n"
	if string(out) != want || cfg.Docs.GoogleUpload != "allow" || cfg.Window.HighWater != 50000 {
		t.Fatalf("added:\n%s", out)
	}
	out, cfg, err = SetKey(out, "docs", "google_upload", "deny")
	if err != nil || strings.Count(string(out), "google_upload =") != 1 || cfg.Docs.GoogleUpload != "deny" {
		t.Fatalf("replaced:\n%s %v", out, err)
	}
	out, _, err = SetKey([]byte("[window]\nhigh_water = 50000"), "docs", "google_upload", "ask")
	if err != nil || string(out) != "[window]\nhigh_water = 50000\n\n[docs]\ngoogle_upload = \"ask\"\n" {
		t.Fatalf("new table:\n%q %v", out, err)
	}
	if out, _, err = SetKey(nil, "docs", "google_upload", "ask"); err != nil || string(out) != "[docs]\ngoogle_upload = \"ask\"\n" {
		t.Fatalf("empty file: %q %v", out, err)
	}
	if _, _, err := SetKey(nil, "docs", "google_upload", "sometimes"); err == nil {
		t.Fatal("accepted an unknown upload setting")
	}
}

func TestSetControlAppendsAndValidates(t *testing.T) {
	out, _, err := SetControl(nil, Model{URL: "http://localhost:11434", Model: "m"})
	if err != nil || !strings.HasPrefix(string(out), "[control]\n") {
		t.Fatalf("%q %v", out, err)
	}
	if _, _, err := SetControl(nil, Model{URL: "http://x", Model: "m", NumCtx: 100, MaxOutput: 100}); err == nil {
		t.Fatal("accepted a reply cap that leaves nothing of num_ctx")
	}
	if _, _, err := SetControl(nil, Model{URL: "ftp://x", Model: "m"}); err == nil {
		t.Fatal("accepted a non-http URL")
	}
	// Quoting survives values a naive writer would break.
	out, cfg, err := SetControl(nil, Model{URL: "http://x", Model: `we"ird\name`})
	if err != nil || cfg.Control.Model != `we"ird\name` {
		t.Fatalf("%s %v %+v", out, err, cfg.Control)
	}
}

func TestWriteFileIsPrivateAndAtomic(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nodwyn", "config.toml")
	if err := WriteFile(path, []byte("[control]\n")); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatal(info.Mode(), err)
	}
	if err := WriteFile("relative.toml", nil); err == nil {
		t.Fatal("relative path accepted")
	}
}

// Sampling settings are written back as they were set.
func TestSetControlWritesSampling(t *testing.T) {
	temperature, seed := 0.3, int64(11)
	raw, cfg, err := SetControl(nil, Model{Provider: "ollama", URL: "http://localhost:11434", Model: "m", Temperature: &temperature, Seed: &seed})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "temperature = 0.3\nseed = 11\n") || *cfg.Control.Temperature != 0.3 || *cfg.Control.Seed != 11 {
		t.Fatalf("%s\n%+v", raw, cfg.Control)
	}
}

// Edit makes several changes and validates only the result: a low water
// mark raised past the old high one is valid once the high one is raised
// too.
func TestEditTypedValuesTogether(t *testing.T) {
	raw := []byte("# mine\n[window]\nlow_water = 10000 # low\nhigh_water = 20000\n")
	out, cfg, err := Edit(raw,
		Change{"window", "low_water", int64(50000)},
		Change{"window", "high_water", int64(100000)},
		Change{"control", "temperature", 2.0},
		Change{"control", "vision", true},
		Change{"control", "url", "https://api.example.com/v1"},
		Change{"control", "model", "m"},
		Change{"control", "provider", "openai"},
		Change{"approval", "commands", "ask"})
	if err != nil {
		t.Fatal(err)
	}
	text := string(out)
	for _, want := range []string{"# mine\n", "low_water = 50000\n", "high_water = 100000\n", "temperature = 2.0\n", "vision = true\n", "[approval]\ncommands = \"ask\"\n"} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in:\n%s", want, text)
		}
	}
	if strings.Contains(text, "# low") || cfg.Window.LowWater != 50000 || cfg.Window.HighWater != 100000 || cfg.Control.Temperature == nil || *cfg.Control.Temperature != 2 || cfg.Control.Vision == nil || !*cfg.Control.Vision || cfg.Approval.Commands != "ask" {
		t.Fatalf("parsed %+v from:\n%s", cfg, text)
	}
	// Each change alone is checked only with the others.
	if _, _, err := Edit(raw, Change{"window", "low_water", int64(50000)}); err == nil {
		t.Fatal("accepted low water above high water")
	}
}

// A nil value removes the key, so its default applies again; removing one
// that is not there changes nothing.
func TestEditRemovesKeys(t *testing.T) {
	raw := []byte("[window]\nhigh_water = 50000 # fixed\nlow_water = 20000\n\n[control]\nurl = \"http://x\"\nmodel = \"m\"\ntemperature = 0.2\n")
	out, cfg, err := Edit(raw, Change{"window", "high_water", nil}, Change{"window", "low_water", nil}, Change{"control", "temperature", nil}, Change{"judge", "timeout", nil})
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != "[window]\n\n[control]\nurl = \"http://x\"\nmodel = \"m\"\n" {
		t.Fatalf("removed:\n%q", out)
	}
	if cfg.Window.Fixed || cfg.Window.HighWater != Default().Window.HighWater || cfg.Control.Temperature != nil {
		t.Fatalf("defaults not back: %+v", cfg)
	}
}

func TestEditRefusesWhatItCannotWrite(t *testing.T) {
	for _, c := range []Change{
		{"control", "temperature", math.NaN()},
		{"window", "high_water", []string{"x"}},
		{"window]\n[control", "url", "http://x"},
		{"window", "high_water = 1\nx", int64(1)},
		{"window", "nonsense", int64(1)},
		{"budget", "cost_hard", 1.0},
	} {
		if out, _, err := Edit(nil, c); err == nil {
			t.Errorf("%+v wrote:\n%s", c, out)
		}
	}
}

func TestKeysAndValues(t *testing.T) {
	raw := []byte("[window]\nhigh_water = 90000\n[control]\nurl = \"http://x\"\nmodel = \"m\"\nseed = 7\n")
	set, err := Keys(raw)
	if err != nil || !set["window.high_water"] || !set["control.seed"] || set["window.low_water"] {
		t.Fatalf("%v %v", set, err)
	}
	cfg, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		table, key string
		want       any
	}{
		{"window", "high_water", int64(90000)}, {"window", "low_water", int64(30 * 1024)}, {"loop", "shell_timeout", "10m"},
		{"control", "seed", int64(7)}, {"control", "temperature", nil}, {"control", "vision", nil},
		{"approval", "writes", "allow"}, {"approval", "commands", "judge"}, {"policy.l1", "enabled", true},
	} {
		if got, ok := cfg.Value(c.table, c.key); !ok || got != c.want {
			t.Errorf("%s.%s = %#v %v, want %#v", c.table, c.key, got, ok, c.want)
		}
	}
	for _, missing := range [][2]string{{"budget", "cost_hard"}, {"window", "nope"}, {"nope", "x"}, {"policy", "allow_commands"}, {"control", "model.x"}} {
		if v, ok := cfg.Value(missing[0], missing[1]); ok {
			t.Errorf("%v: %v", missing, v)
		}
	}
}

// A server's cache_key is written back as it was set.
func TestSetControlWritesCacheKey(t *testing.T) {
	raw, cfg, err := SetControl(nil, Model{Provider: "openai", URL: "https://api.example.test/v1", Model: "m", CacheKey: CacheKeyHeader})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "cache_key = \"header\"\n") || cfg.Control.CacheKey != CacheKeyHeader {
		t.Fatalf("%s\n%+v", raw, cfg.Control)
	}
}

// The comment above a table is that table's: replacing or removing the
// table before it used to take the comment with it.
func TestReplacingATableKeepsTheNextTablesComment(t *testing.T) {
	raw := []byte(`[control]
url = "http://old:11434"
model = "old"
# old control's trailing note

# --- Window: keep this low ---
[window]
high_water = 50000
`)
	out, _, err := SetControl(raw, Model{Provider: "ollama", URL: "http://new:11434", Model: "new"})
	if err != nil {
		t.Fatal(err)
	}
	text := string(out)
	if !strings.Contains(text, "# --- Window: keep this low ---\n[window]") {
		t.Fatalf("the next table's comment went:\n%s", text)
	}
	if strings.Contains(text, "old:11434") {
		t.Fatalf("the old table stayed:\n%s", text)
	}
	out, _, err = SetControl(out, Model{})
	if err == nil && !strings.Contains(string(out), "# --- Window: keep this low ---\n[window]") {
		t.Fatalf("removing the table took the next one's comment:\n%s", out)
	}
}
