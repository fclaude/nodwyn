package rpc

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/fclaude/nodwyn/internal/config"
)

func TestKeychainCreateUnlockOverTheSocket(t *testing.T) {
	o := options(t)
	path, stop := start(t, o)
	c := connect(t, path)
	c.hello()
	var status keychainStatus
	json.Unmarshal(c.ok("keychain.status", map[string]any{}), &status)
	if status.Exists || status.Unlocked {
		t.Fatalf("fresh: %+v", status)
	}
	if r := c.call("keychain.unlock", map[string]any{"passphrase": "whatever1"}); r.Error == nil {
		t.Fatal("unlocked a keychain that does not exist")
	}
	if r := c.call("keychain.create", map[string]any{"passphrase": "short"}); r.Error == nil {
		t.Fatal("accepted a short passphrase")
	}
	json.Unmarshal(c.ok("keychain.create", map[string]any{"passphrase": "correct horse"}), &status)
	if !status.Exists || !status.Unlocked {
		t.Fatalf("after create: %+v", status)
	}
	if r := c.call("keychain.create", map[string]any{"passphrase": "correct horse"}); r.Error == nil {
		t.Fatal("created twice")
	}
	stop()

	// A new daemon starts locked; the wrong passphrase fails, the right one opens.
	path, _ = start(t, o)
	c = connect(t, path)
	c.hello()
	if r := c.call("keychain.unlock", map[string]any{"passphrase": "wrong horse"}); r.Error == nil || strings.Contains(r.Error.Message, "horse") {
		t.Fatalf("wrong passphrase: %+v", r.Error)
	}
	json.Unmarshal(c.ok("keychain.unlock", map[string]any{"passphrase": "correct horse"}), &status)
	if !status.Unlocked {
		t.Fatalf("after unlock: %+v", status)
	}
}

func TestSettingsWriteControlAndKey(t *testing.T) {
	o := options(t)
	os.MkdirAll(o.Paths.ConfigDir, 0o700)
	os.WriteFile(o.Paths.ConfigFile, []byte("# mine\n[window]\nhigh_water = 50000\n"), 0o600)
	cfg, err := config.Load(o.Paths.ConfigFile)
	if err != nil {
		t.Fatal(err)
	}
	o.Config = cfg
	path, _ := start(t, o)
	c := connect(t, path)
	c.hello()
	if r := c.call("settings.control.write", map[string]any{"provider": "ollama", "url": "file:///etc", "model": "m"}); r.Error == nil {
		t.Fatal("accepted a file URL")
	}
	var settings settingsResult
	json.Unmarshal(c.ok("settings.control.write", map[string]any{"provider": "ollama", "url": "http://workshop:11434/", "model": "qwen"}), &settings)
	if settings.Control.URL != "http://workshop:11434" || settings.Control.Model != "qwen" {
		t.Fatalf("settings: %+v", settings)
	}
	raw, _ := os.ReadFile(o.Paths.ConfigFile)
	if !strings.Contains(string(raw), "# mine") || !strings.Contains(string(raw), "high_water = 50000") {
		t.Fatalf("config lost its lines:\n%s", raw)
	}
	if info, _ := os.Stat(o.Paths.ConfigFile); info.Mode().Perm() != 0o600 {
		t.Fatal(info.Mode())
	}
	// Opening a task now uses the new configuration.
	if r := c.call("session.open", map[string]any{"repo": t.TempDir()}); r.Error != nil {
		t.Fatalf("configured open: %+v", r.Error)
	}
	// A key needs the keychain, and is never written to the config file.
	if r := c.call("settings.control.write", map[string]any{"provider": "openai", "url": "https://api.example.com/v1", "model": "gpt", "api_key": "sk-test-canary-0123456789"}); r.Error == nil {
		t.Fatal("stored a key without a keychain")
	}
	c.ok("keychain.create", map[string]any{"passphrase": "correct horse"})
	json.Unmarshal(c.ok("settings.control.write", map[string]any{"provider": "openai", "url": "https://api.example.com/v1", "model": "gpt", "api_key": "sk-test-canary-0123456789"}), &settings)
	if settings.Control.Credential != "model-api-example-com" || settings.Keychain.Credentials != 1 {
		t.Fatalf("keyed settings: %+v", settings)
	}
	raw, _ = os.ReadFile(o.Paths.ConfigFile)
	bindings, _ := os.ReadFile(o.Paths.Bindings)
	if strings.Contains(string(raw), "canary") || strings.Contains(string(bindings), "canary") {
		t.Fatal("key written in plain text")
	}
	if !strings.Contains(string(bindings), `dest = "api.example.com"`) || !strings.Contains(string(bindings), "header:Authorization:Bearer") {
		t.Fatalf("binding row:\n%s", bindings)
	}
	// Changing only the model keeps the credential; a new key gets its own class.
	json.Unmarshal(c.ok("settings.control.write", map[string]any{"provider": "openai", "url": "https://api.example.com/v1", "model": "gpt-2"}), &settings)
	if settings.Control.Credential != "model-api-example-com" {
		t.Fatalf("credential dropped: %+v", settings.Control)
	}
	json.Unmarshal(c.ok("settings.control.write", map[string]any{"provider": "openai", "url": "https://api.example.com/v1", "model": "gpt-2", "api_key": "sk-test-canary-9876543210"}), &settings)
	if settings.Control.Credential != "model-api-example-com-2" {
		t.Fatalf("replacement class: %+v", settings.Control)
	}
}
