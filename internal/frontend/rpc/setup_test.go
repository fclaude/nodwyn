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

// A task gets the keychain only when it is built, so one opened while the
// model's key is locked away would fail every request even after an
// unlock. The daemon refuses to open or resume it until the unlock.
func TestLockedKeychainRefusesTasksUntilUnlocked(t *testing.T) {
	o := options(t)
	repo := t.TempDir()
	path, stop := start(t, o)
	c := connect(t, path)
	c.hello()
	c.ok("keychain.create", map[string]any{"passphrase": "correct horse"})
	c.ok("settings.control.write", map[string]any{"provider": "openai", "url": "https://api.example.com/v1", "model": "m", "api_key": "sk-test-canary"})
	var opened sessionDetails
	json.Unmarshal(c.ok("session.open", map[string]any{"repo": repo}), &opened)
	c.ok("session.close", map[string]any{"id": opened.ID})
	stop()

	cfg, err := config.Load(o.Paths.ConfigFile)
	if err != nil {
		t.Fatal(err)
	}
	o.Config = cfg
	path, _ = start(t, o)
	c = connect(t, path)
	c.hello()
	for _, call := range []struct {
		method string
		params map[string]any
	}{
		{"session.open", map[string]any{"repo": repo}},
		{"session.resume", map[string]any{"id": opened.ID}},
		{"run.start", map[string]any{"repo": repo, "task": "t", "wall": "1m", "max_turns": 1, "auto_approve": false}},
	} {
		if r := c.call(call.method, call.params); r.Error == nil || !strings.Contains(r.Error.Message, "Unlock the keychain first") {
			t.Fatalf("%s with the key locked: %+v", call.method, r.Error)
		}
	}
	c.ok("keychain.unlock", map[string]any{"passphrase": "correct horse"})
	c.ok("session.resume", map[string]any{"id": opened.ID})
}

// The upload setting reads as config.toml has it now, and Settings saves it
// there, keeping the file's other lines.
func TestSettingsDocsWrite(t *testing.T) {
	o := options(t)
	os.MkdirAll(o.Paths.ConfigDir, 0o700)
	os.WriteFile(o.Paths.ConfigFile, []byte("# mine\n[window]\nhigh_water = 50000\n"), 0o644)
	path, _ := start(t, o)
	c := connect(t, path)
	c.hello()
	var settings settingsResult
	json.Unmarshal(c.ok("settings.read", map[string]any{}), &settings)
	if settings.Docs.GoogleUpload != "" || !settings.Docs.Google {
		t.Fatalf("unset: %+v", settings.Docs)
	}
	if r := c.call("settings.docs.write", map[string]any{"google_upload": "sometimes"}); r.Error == nil {
		t.Fatal("accepted an unknown setting")
	}
	json.Unmarshal(c.ok("settings.docs.write", map[string]any{"google_upload": "deny"}), &settings)
	if settings.Docs.GoogleUpload != "deny" {
		t.Fatalf("written: %+v", settings.Docs)
	}
	raw, _ := os.ReadFile(o.Paths.ConfigFile)
	if !strings.Contains(string(raw), "# mine") || !strings.Contains(string(raw), "[docs]\ngoogle_upload = \"deny\"") {
		t.Fatalf("config.toml:\n%s", raw)
	}
	if info, _ := os.Stat(o.Paths.ConfigFile); info.Mode().Perm() != 0o600 {
		t.Fatal(info.Mode())
	}
	// A session that saved its answer: the next read sees it.
	os.WriteFile(o.Paths.ConfigFile, []byte("[docs]\ngoogle_upload = \"allow\"\n"), 0o600)
	json.Unmarshal(c.ok("settings.read", map[string]any{}), &settings)
	if settings.Docs.GoogleUpload != "allow" {
		t.Fatalf("fresh read: %+v", settings.Docs)
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

// Saving the same model again keeps what was tuned by hand in config.toml;
// choosing another model starts from its defaults.
func TestSettingsWriteKeepsTuningOfTheSameModel(t *testing.T) {
	o := options(t)
	os.MkdirAll(o.Paths.ConfigDir, 0o700)
	os.WriteFile(o.Paths.ConfigFile, []byte("[control]\nurl = \"http://workshop:11434\"\nmodel = \"qwen\"\nnum_ctx = 90000\ntemperature = 0.2\nseed = 5\n"), 0o600)
	cfg, err := config.Load(o.Paths.ConfigFile)
	if err != nil {
		t.Fatal(err)
	}
	o.Config = cfg
	path, _ := start(t, o)
	c := connect(t, path)
	c.hello()
	c.ok("settings.control.write", map[string]any{"provider": "ollama", "url": "http://workshop:11434/", "model": "qwen"})
	raw, _ := os.ReadFile(o.Paths.ConfigFile)
	for _, want := range []string{"num_ctx = 90000", "temperature = 0.2", "seed = 5"} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("lost %q:\n%s", want, raw)
		}
	}
	c.ok("settings.control.write", map[string]any{"provider": "ollama", "url": "http://workshop:11434/", "model": "llama"})
	raw, _ = os.ReadFile(o.Paths.ConfigFile)
	if strings.Contains(string(raw), "temperature") || strings.Contains(string(raw), "num_ctx") {
		t.Fatalf("another model kept the old one's tuning:\n%s", raw)
	}
}
