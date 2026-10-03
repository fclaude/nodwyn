package rpc

import (
	"encoding/json"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/fclaude/nodwyn/internal/config"
	"github.com/fclaude/nodwyn/internal/llm"
	fake "github.com/fclaude/nodwyn/test/fakes/ollama"
)

// Settings reports the judge's host and whether a jev-key is stored, so
// its judge-key row never points at a Judge panel with no field and never
// offers to add a key that is already there.
func TestSettingsJevHost(t *testing.T) {
	o := options(t)
	o.Config.Jev.URL = "https://judge.example:8443"
	path, _ := start(t, o)
	c := connect(t, path)
	c.hello()
	c.ok("keychain.create", map[string]any{"no_passphrase": true})
	var settings settingsResult
	json.Unmarshal(c.ok("settings.read", map[string]any{}), &settings)
	if settings.JevHost != "judge.example" || settings.JevStored {
		t.Fatalf("jev before storing: %+v", settings)
	}
	c.ok("credentials.add", map[string]any{"class": "jev-key", "value": "sk-judge-canary-0001",
		"dest": "judge.example", "methods": []string{"POST"}, "position": "header:Authorization:Bearer"})
	json.Unmarshal(c.ok("settings.read", map[string]any{}), &settings)
	if !settings.JevStored {
		t.Fatalf("jev after storing: %+v", settings)
	}
}

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

// A server is set up once, with its key, and any model it offers is then
// chosen by the server's name: the key is neither asked for again nor
// stored twice.
func TestSettingsControlOnAServer(t *testing.T) {
	o := options(t)
	os.MkdirAll(o.Paths.ConfigDir, 0o700)
	os.WriteFile(o.Paths.ConfigFile, []byte("[models.coder]\nprovider = \"openai\"\nurl = \"https://openrouter.ai/api/v1\"\nmodel = \"c\"\n"), 0o600)
	cfg, err := config.Load(o.Paths.ConfigFile)
	if err != nil {
		t.Fatal(err)
	}
	o.Config = cfg
	path, _ := start(t, o)
	c := connect(t, path)
	c.hello()
	c.ok("keychain.create", map[string]any{"passphrase": "correct horse"})
	var settings settingsResult
	json.Unmarshal(c.ok("settings.control.write", map[string]any{"provider": "openai", "url": "https://openrouter.ai/api/v1/", "model": "a", "api_key": "sk-test-canary-0123456789"}), &settings)
	want := []modelServer{{Name: "openrouter", Provider: "openai", URL: "https://openrouter.ai/api/v1", Credential: "model-openrouter-ai"}}
	if !reflect.DeepEqual(settings.Servers, want) || settings.Control.Server != "openrouter" || settings.Control.Credential != "model-openrouter-ai" {
		t.Fatalf("settings: %+v %+v", settings.Servers, settings.Control)
	}
	// Another model on the same server: by name, with no key.
	json.Unmarshal(c.ok("settings.control.write", map[string]any{"server": "openrouter", "model": "b"}), &settings)
	if settings.Control.Model != "b" || settings.Control.Credential != "model-openrouter-ai" || settings.Keychain.Credentials != 1 {
		t.Fatalf("by name: %+v, %d keys", settings.Control, settings.Keychain.Credentials)
	}
	raw, _ := os.ReadFile(o.Paths.ConfigFile)
	if want := "[control]\nserver = \"openrouter\"\nmodel = \"b\"\n"; !strings.Contains(string(raw), want) || strings.Count(string(raw), "credential") != 1 {
		t.Fatalf("config.toml:\n%s", raw)
	}
	for _, bad := range []map[string]any{
		{"server": "nowhere", "model": "b"},
		{"server": "openrouter", "url": "https://openrouter.ai/api/v1", "model": "b"},
		{"server": "openrouter", "model": "b", "api_key": "sk-test-canary-0123456789"},
		{"server": "openrouter", "model": " "},
	} {
		if r := c.call("settings.control.write", bad); r.Error == nil {
			t.Errorf("accepted %v", bad)
		}
	}
	// A server added to config.toml by hand since the daemon started is
	// found by its address, and one of the same name is never written over.
	os.WriteFile(o.Paths.ConfigFile, append(raw, "\n[servers.workshop]\nurl = \"http://workshop:11434\"\n\n[servers.localhost]\nprovider = \"openai\"\nurl = \"http://localhost:8000/v1\"\n"...), 0o600)
	json.Unmarshal(c.ok("settings.control.write", map[string]any{"provider": "ollama", "url": "http://workshop:11434", "model": "qwen"}), &settings)
	if settings.Control.Server != "workshop" || len(settings.Servers) != 3 {
		t.Fatalf("hand-written server: %+v %+v", settings.Control, settings.Servers)
	}
	// The key stays a model key while its server is configured, though no
	// model names the server now.
	var creds credentialsResult
	json.Unmarshal(c.ok("credentials.list", map[string]any{}), &creds)
	if len(creds.Credentials) != 1 || !creds.Credentials[0].Model || !slices.Equal(creds.Credentials[0].UsedBy, []string{"server openrouter"}) {
		t.Fatalf("the server's key: %+v", creds.Credentials)
	}
	json.Unmarshal(c.ok("settings.control.write", map[string]any{"provider": "ollama", "url": "http://localhost:11434", "model": "qwen"}), &settings)
	raw, _ = os.ReadFile(o.Paths.ConfigFile)
	if settings.Control.Server != "localhost-2" || !strings.Contains(string(raw), "url = \"http://localhost:8000/v1\"") {
		t.Fatalf("a server of the same name: %+v\n%s", settings.Control, raw)
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

// Settings changes what tasks opened afterwards get, keeping config.toml's
// other lines; a task already open keeps the limits it started with.
func TestSettingsWriteAppliesToNewTasks(t *testing.T) {
	model := fake.New()
	defer model.Close()
	o := options(t)
	o.Provider = llm.NewOllama(model.URL)
	os.MkdirAll(o.Paths.ConfigDir, 0o700)
	os.WriteFile(o.Paths.ConfigFile, []byte("# mine\n[budget]\ntokens_soft = 100\ntokens_hard = 200 # tight\n"), 0o644)
	cfg, err := config.Load(o.Paths.ConfigFile)
	if err != nil {
		t.Fatal(err)
	}
	o.Config = cfg
	path, _ := start(t, o)
	c := connect(t, path)
	c.hello()
	var before sessionDetails
	json.Unmarshal(c.ok("session.open", map[string]any{"repo": t.TempDir()}), &before)

	var settings settingsResult
	json.Unmarshal(c.ok("settings.read", map[string]any{}), &settings)
	if v := settings.Values["budget.tokens_hard"]; v.Value != float64(200) || v.Default != float64(10_000_000) || !v.Set {
		t.Fatalf("tokens_hard read: %+v", v)
	}
	if v := settings.Values["control.temperature"]; v.Value != nil || v.Set {
		t.Fatalf("unset temperature: %+v", v)
	}
	json.Unmarshal(c.ok("settings.write", map[string]any{"set": map[string]any{
		"budget.tokens_soft": 500, "budget.tokens_hard": 1000, "budget.cost_hard": 2.5,
		"approval.commands": "ask", "approval.writes": "ask", "loop.shell_timeout": "5m",
		"jev.price_input": 0.5, "jev.price_output": 2,
	}}), &settings)
	if v := settings.Values["budget.tokens_hard"]; v.Value != float64(1000) || !v.Set {
		t.Fatalf("tokens_hard written: %+v", v)
	}
	if v := settings.Values["approval.commands"]; v.Value != "ask" || v.Default != "judge" {
		t.Fatalf("approval written: %+v", v)
	}
	raw, _ := os.ReadFile(o.Paths.ConfigFile)
	for _, want := range []string{"# mine\n", "tokens_hard = 1000\n", "cost_hard = 2.5\n", "[approval]\n", "commands = \"ask\"\nwrites = \"ask\"\n", "[loop]\nshell_timeout = \"5m\"\n", "price_input = 0.5\n", "price_output = 2"} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("config.toml lacks %q:\n%s", want, raw)
		}
	}
	if info, _ := os.Stat(o.Paths.ConfigFile); info.Mode().Perm() != 0o600 {
		t.Fatal(info.Mode())
	}

	var info, after sessionDetails
	json.Unmarshal(c.ok("session.info", map[string]any{"id": before.ID}), &info)
	if info.Budgets.Tokens.Hard != 200 || info.Budgets.Cost.Hard != 0 {
		t.Fatalf("an open task took the new limits: %+v", info.Budgets)
	}
	json.Unmarshal(c.ok("session.open", map[string]any{"repo": t.TempDir()}), &after)
	if after.Budgets.Tokens.Hard != 1000 || after.Budgets.Cost.Hard != 2_500_000 {
		t.Fatalf("a new task kept the old limits: %+v", after.Budgets)
	}

	// null removes a setting, so its default applies again.
	json.Unmarshal(c.ok("settings.write", map[string]any{"set": map[string]any{"budget.cost_hard": nil, "approval.writes": nil}}), &settings)
	raw, _ = os.ReadFile(o.Paths.ConfigFile)
	if strings.Contains(string(raw), "cost_hard") || strings.Contains(string(raw), "writes") || settings.Values["budget.cost_hard"].Value != float64(0) || settings.Values["approval.writes"].Set || settings.Values["approval.writes"].Value != "allow" {
		t.Fatalf("not removed:\n%s\n%+v", raw, settings.Values["budget.cost_hard"])
	}

	// What Settings may not change, or values nodwyn would not load, leave
	// the file as it was.
	for _, set := range []map[string]any{
		{},
		{"control.url": "http://elsewhere:11434"},
		{"policy.unbound": "confirm"},
		{"jev.url": "https://judge.example"},
		{"budget.tokens_hard": "lots"},
		{"budget.tokens_hard": 1.5},
		{"budget.tokens_hard": 50}, // below the soft limit
		{"approval.commands": "sometimes"},
		{"approval.writes": ""},
		{"tasks.mode": "chat"},
		{"control.vision": "yes"},
		{"jev.price_output": -1},
	} {
		if r := c.call("settings.write", map[string]any{"set": set}); r.Error == nil {
			t.Errorf("accepted %v", set)
		}
	}
	if now, _ := os.ReadFile(o.Paths.ConfigFile); string(now) != string(raw) {
		t.Fatalf("a refused write changed the file:\n%s", now)
	}
}

// A paired device starts or resumes only runs it answers itself.
func TestPairedDeviceRunsAreAnsweredByHand(t *testing.T) {
	o := options(t)
	path, _ := start(t, o)
	phone := connect(t, path)
	phone.ok("hello", map[string]any{"protocol": 1, "via": "web:phone"})
	for _, unwatched := range []string{"auto_approve", "unattended", "judge"} {
		run := map[string]any{"repo": "/nonexistent", "task": "t", "wall": "1m", "max_turns": 2, "auto_approve": false}
		run[unwatched] = true
		if r := phone.call("run.start", run); r.Error == nil || !strings.Contains(r.Error.Message, "paired device") {
			t.Fatalf("run.start with %s from a phone: %+v", unwatched, r.Error)
		}
		if r := phone.call("run.resume", map[string]any{"id": "x", unwatched: true}); r.Error == nil || !strings.Contains(r.Error.Message, "paired device") {
			t.Fatalf("run.resume with %s from a phone: %+v", unwatched, r.Error)
		}
	}
	// One it answers gets as far as the folder check.
	run := map[string]any{"repo": "/nonexistent", "task": "t", "wall": "1m", "max_turns": 2, "auto_approve": false}
	if r := phone.call("run.start", run); r.Error == nil || !strings.Contains(r.Error.Message, "repo must be") {
		t.Fatalf("supervised run from a phone: %+v", r.Error)
	}
	desk := connect(t, path)
	desk.hello()
	run["unattended"] = true
	if r := desk.call("run.start", run); r.Error == nil || !strings.Contains(r.Error.Message, "repo must be") {
		t.Fatalf("unattended run on this computer: %+v", r.Error)
	}
}

// Every setting Settings may change is one config.toml has, and the daemon
// knows how to take its table from a written file.
func TestSettableSettingsExist(t *testing.T) {
	defaults := config.Default()
	for name := range settable {
		table, key := splitSetting(name)
		if _, ok := defaults.Value(table, key); !ok {
			t.Errorf("%s is not a setting", name)
		}
		withTables(defaults, defaults, map[string]bool{table: true})
	}
	for _, left := range []string{"control.url", "control.credential", "jev.url", "mcp.egress", "docs.google_upload_url", "policy.unbound", "loop.max_tool_calls"} {
		if _, ok := settable[left]; ok {
			t.Errorf("%s is settable from the console", left)
		}
	}
}
