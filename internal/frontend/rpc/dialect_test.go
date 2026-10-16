package rpc

import (
	"os"
	"strings"
	"testing"

	"github.com/fclaude/nodwyn/internal/config"
)

// Settings can select another model on a saved server without reverting its
// wire dialect, and a server at another endpoint does not inherit it.
func TestSettingsPreservesConfiguredDialect(t *testing.T) {
	o := options(t)
	raw := "[servers.fast]\nprovider = \"openai\"\ndialect = \"cerebras\"\nurl = \"https://example.test/v1\"\n[control]\nserver = \"fast\"\nmodel = \"m\"\n"
	var err error
	o.Config, err = config.Parse([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(o.Paths.ConfigDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(o.Paths.ConfigFile, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	path, _ := start(t, o)
	c := connect(t, path)
	c.hello()
	response := c.ok("settings.control.write", map[string]any{"server": "fast", "model": "other"})
	if !strings.Contains(string(response), `"dialect":"cerebras"`) {
		t.Fatalf("settings lost server dialect: %s", response)
	}
	saved, err := os.ReadFile(o.Paths.ConfigFile)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(saved), `dialect = "cerebras"`) != 1 {
		t.Fatalf("dialect lost or duplicated:\n%s", saved)
	}
	cfg, err := config.Parse(saved)
	if err != nil {
		t.Fatal(err)
	}
	m := cfg.Control
	m.Server = ""
	resolved, _, err := config.SetControl(nil, m)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(resolved), `dialect = "cerebras"`) {
		t.Fatalf("control lost dialect: %s", resolved)
	}
	// Changing endpoints must not transfer the previous dialect.
	c.ok("settings.provider.write", map[string]any{"name": "far", "provider": "openai", "url": "https://other.test/v1"})
	response = c.ok("settings.control.write", map[string]any{"server": "far", "model": "m"})
	cfg, err = config.Load(o.Paths.ConfigFile)
	if err != nil {
		t.Fatal(err)
	}
	m = cfg.Control
	m.Server = ""
	resolved, _, err = config.SetControl(nil, m)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(resolved), "dialect") {
		t.Fatalf("dialect leaked to another endpoint: %s", response)
	}
}
