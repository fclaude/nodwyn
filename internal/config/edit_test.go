package config

import (
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
	if _, _, err := SetControl([]byte("[window]\nhigh_water = 1000\nlow_water = 500\n"), Model{URL: "http://x", Model: "m", NumCtx: 100}); err == nil {
		t.Fatal("accepted a window larger than num_ctx")
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
