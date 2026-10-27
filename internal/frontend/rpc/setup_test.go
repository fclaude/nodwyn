package rpc

import (
	"context"
	"encoding/json"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/fclaude/nodwyn/internal/config"
	"github.com/fclaude/nodwyn/internal/llm"
	"github.com/fclaude/nodwyn/internal/policyfile"
	"github.com/fclaude/nodwyn/test/fakes/must"
	fake "github.com/fclaude/nodwyn/test/fakes/ollama"
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
	c.ok("settings.provider.write", map[string]any{"name": "api", "provider": "openai", "url": "https://api.example.com/v1", "api_key": "sk-test-canary"})
	c.ok("settings.control.write", map[string]any{"server": "api", "model": "m"})
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
	if r := c.call("settings.provider.write", map[string]any{"name": "far", "provider": "ollama", "url": "file:///etc"}); r.Error == nil {
		t.Fatal("accepted a file URL")
	}
	var settings settingsResult
	c.ok("settings.provider.write", map[string]any{"name": "workshop", "provider": "ollama", "url": "http://workshop:11434/"})
	json.Unmarshal(c.ok("settings.control.write", map[string]any{"server": "workshop", "model": "qwen"}), &settings)
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
	keyed := map[string]any{"name": "api", "provider": "openai", "url": "https://api.example.com/v1", "api_key": "sk-test-canary-0123456789"}
	if r := c.call("settings.provider.write", keyed); r.Error == nil {
		t.Fatal("stored a key without a keychain")
	}
	c.ok("keychain.create", map[string]any{"passphrase": "correct horse"})
	c.ok("settings.provider.write", keyed)
	json.Unmarshal(c.ok("settings.control.write", map[string]any{"server": "api", "model": "gpt"}), &settings)
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
	// Changing only the model keeps the credential; a new key replaces it in
	// place, so no old one is left bound for the host with nothing using it.
	json.Unmarshal(c.ok("settings.control.write", map[string]any{"server": "api", "model": "gpt-2"}), &settings)
	if settings.Control.Credential != "model-api-example-com" {
		t.Fatalf("credential dropped: %+v", settings.Control)
	}
	keyed["api_key"] = "sk-test-canary-9876543210"
	json.Unmarshal(c.ok("settings.provider.write", keyed), &settings)
	if settings.Control.Credential != "model-api-example-com" || settings.Keychain.Credentials != 1 {
		t.Fatalf("rotated key: %+v, %d credentials", settings.Control, settings.Keychain.Credentials)
	}
}

// The default model is chosen, never given a provider or a key: those are
// saved by settings.provider.write, which replaces a key in place, so
// settings.control.write cannot leave an old key behind (P2-009).
func TestControlWriteTakesNoProviderOrKey(t *testing.T) {
	o := options(t)
	path, _ := start(t, o)
	c := connect(t, path)
	c.hello()
	c.ok("keychain.create", map[string]any{"passphrase": "correct horse"})
	for _, bad := range []map[string]any{
		{"provider": "openai", "url": "https://api.example.com/v1", "model": "gpt", "api_key": "sk-test-canary-0123456789"},
		{"server": "api", "model": "gpt", "api_key": "sk-test-canary-0123456789"},
		{"provider": "ollama", "url": "http://workshop:11434", "model": "qwen"},
	} {
		if r := c.call("settings.control.write", bad); r.Error == nil || r.Error.Code != -32602 {
			t.Errorf("accepted %v: %+v", bad, r.Error)
		}
	}
	var creds credentialsResult
	json.Unmarshal(c.ok("credentials.list", map[string]any{}), &creds)
	if len(creds.Credentials) != 0 {
		t.Fatalf("a refused call stored a key: %+v", creds.Credentials)
	}
}

// settings.control.write writes config.toml under the lock the sessions use,
// so an edit another writer is making is neither lost nor overwritten
// (P2-069, P2-078).
func TestControlWriteWaitsForTheConfigLock(t *testing.T) {
	o := options(t)
	must.MkdirAll(t, o.Paths.ConfigDir, 0o700)
	raw := "[servers.workshop]\nurl = \"http://workshop:11434\"\n"
	must.WriteFile(t, o.Paths.ConfigFile, []byte(raw), 0o600)
	cfg, err := config.Load(o.Paths.ConfigFile)
	if err != nil {
		t.Fatal(err)
	}
	o.Config = cfg
	path, _ := start(t, o)
	c := connect(t, path)
	c.hello()
	held, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		done <- policyfile.Update(context.Background(), o.Paths.ConfigFile, func(raw []byte) ([]byte, error) {
			close(held)
			<-release
			return append(raw, "\n# edited elsewhere\n"...), nil
		})
	}()
	<-held
	answered := make(chan json.RawMessage, 1)
	go func() { answered <- c.ok("settings.control.write", map[string]any{"server": "workshop", "model": "qwen"}) }()
	select {
	case <-answered:
		t.Fatal("settings.control.write wrote config.toml while another writer held its lock")
	case <-time.After(300 * time.Millisecond):
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatalf("the other writer: %v", err)
	}
	<-answered
	text, _ := os.ReadFile(o.Paths.ConfigFile)
	if !strings.Contains(string(text), "# edited elsewhere") || !strings.Contains(string(text), "[control]") {
		t.Fatalf("one of the two edits was lost:\n%s", text)
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
	c.ok("settings.provider.write", map[string]any{"provider": "openai", "url": "https://openrouter.ai/api/v1/", "api_key": "sk-test-canary-0123456789"})
	json.Unmarshal(c.ok("settings.control.write", map[string]any{"server": "openrouter", "model": "a"}), &settings)
	// [models.coder] gives this address itself, so it is on the provider
	// too, though it is not sent the key.
	want := []modelServer{{Name: "openrouter", Provider: "openai", URL: "https://openrouter.ai/api/v1", Credential: "model-openrouter-ai", Host: "openrouter.ai", Key: "ready", Uses: []string{"control", "coder"}}}
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
	json.Unmarshal(c.ok("settings.control.write", map[string]any{"server": "workshop", "model": "qwen"}), &settings)
	if settings.Control.Server != "workshop" || len(settings.Servers) != 3 {
		t.Fatalf("hand-written server: %+v %+v", settings.Control, settings.Servers)
	}
	// The key stays a model key, used by the server and by the models that
	// were the default and are listed still (a person who chooses another
	// default does not lose the first).
	var creds credentialsResult
	json.Unmarshal(c.ok("credentials.list", map[string]any{}), &creds)
	if len(creds.Credentials) != 1 || !creds.Credentials[0].Model || !slices.Contains(creds.Credentials[0].UsedBy, "server openrouter") || !slices.Contains(creds.Credentials[0].UsedBy, "model openrouter-a") {
		t.Fatalf("the server's key: %+v", creds.Credentials)
	}
	c.ok("settings.provider.write", map[string]any{"provider": "ollama", "url": "http://localhost:11434"})
	json.Unmarshal(c.ok("settings.control.write", map[string]any{"server": "localhost-2", "model": "qwen"}), &settings)
	raw, _ = os.ReadFile(o.Paths.ConfigFile)
	if settings.Control.Server != "localhost-2" || !strings.Contains(string(raw), "url = \"http://localhost:8000/v1\"") {
		t.Fatalf("a server of the same name: %+v\n%s", settings.Control, raw)
	}
}

// With two servers at one address, choosing another model on the one the main
// model names keeps that server, and a provider's address is no way to choose.
func TestControlModelKeepsNamedServer(t *testing.T) {
	o := options(t)
	os.MkdirAll(o.Paths.ConfigDir, 0o700)
	os.WriteFile(o.Paths.ConfigFile, []byte("[servers.alpha]\nurl = \"http://workshop:11434\"\n\n[servers.beta]\nurl = \"http://workshop:11434\"\n\n[control]\nserver = \"beta\"\nmodel = \"qwen\"\n"), 0o600)
	cfg, err := config.Load(o.Paths.ConfigFile)
	if err != nil {
		t.Fatal(err)
	}
	o.Config = cfg
	path, _ := start(t, o)
	c := connect(t, path)
	c.hello()
	var settings settingsResult
	json.Unmarshal(c.ok("settings.control.write", map[string]any{"server": "beta", "model": "llama"}), &settings)
	if settings.Control.Server != "beta" || len(settings.Servers) != 2 {
		t.Fatalf("control: %+v, servers %+v", settings.Control, settings.Servers)
	}
}

// Saving the same model again keeps what was tuned by hand in config.toml;
// choosing another model starts from its defaults.
func TestSettingsWriteKeepsTuningOfTheSameModel(t *testing.T) {
	o := options(t)
	os.MkdirAll(o.Paths.ConfigDir, 0o700)
	must.WriteFile(t, o.Paths.ConfigFile, []byte("[servers.workshop]\nurl = \"http://workshop:11434\"\n\n[control]\nurl = \"http://workshop:11434\"\nmodel = \"qwen\"\nnum_ctx = 90000\ntemperature = 0.2\nseed = 5\n"), 0o600)
	cfg, err := config.Load(o.Paths.ConfigFile)
	if err != nil {
		t.Fatal(err)
	}
	o.Config = cfg
	path, _ := start(t, o)
	c := connect(t, path)
	c.hello()
	c.ok("settings.control.write", map[string]any{"server": "workshop", "model": "qwen"})
	raw, _ := os.ReadFile(o.Paths.ConfigFile)
	for _, want := range []string{"num_ctx = 90000", "temperature = 0.2", "seed = 5"} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("lost %q:\n%s", want, raw)
		}
	}
	c.ok("settings.control.write", map[string]any{"server": "workshop", "model": "llama"})
	raw, _ = os.ReadFile(o.Paths.ConfigFile)
	control, models, _ := strings.Cut(string(raw), "[models.")
	control, _, _ = strings.Cut(control, "[servers.")
	if strings.Contains(control, "temperature") || strings.Contains(control, "num_ctx") {
		t.Fatalf("another model kept the old one's tuning:\n%s", raw)
	}
	// The model that was the default is listed as it was, tuning and all.
	if !strings.Contains(models, "workshop-qwen]") || !strings.Contains(models, "num_ctx = 90000") {
		t.Fatalf("the previous default was not kept as it was:\n%s", raw)
	}
}

// Settings writes config.toml, keeping its other lines. A task's limits are
// not among them: they are the task's own (ADR 49, amended 2026-10-27), and
// a [budget] a file still has loads and is not offered.
func TestSettingsWriteAppliesToNewTasks(t *testing.T) {
	model := fake.New()
	defer model.Close()
	o := options(t)
	o.Provider = llm.NewOllama(model.URL)
	must.MkdirAll(t, o.Paths.ConfigDir, 0o700)
	must.WriteFile(t, o.Paths.ConfigFile, []byte("# mine\n[budget]\ntokens_hard = 200 # retired\n[window]\nlow_water = 20000\nhigh_water = 40000 # tight\n"), 0o644)
	cfg, err := config.Load(o.Paths.ConfigFile)
	if err != nil {
		t.Fatal(err)
	}
	o.Config = cfg
	path, _ := start(t, o)
	c := connect(t, path)
	c.hello()
	var settings settingsResult
	json.Unmarshal(c.ok("settings.read", map[string]any{}), &settings)
	if v := settings.Values["window.high_water"]; v.Value != float64(40000) || v.Default != float64(60*1024) || !v.Set {
		t.Fatalf("high_water read: %+v", v)
	}
	for name := range settings.Values {
		if strings.HasPrefix(name, "control.") {
			t.Fatalf("%s is a setting of every model at once; a model's settings are its own (settings.model.write)", name)
		}
		if strings.HasPrefix(name, "budget.") {
			t.Fatalf("%s is offered; a task's limits are its own", name)
		}
	}
	json.Unmarshal(c.ok("settings.write", map[string]any{"set": map[string]any{
		"window.low_water": 50000, "window.high_water": 100000, "judge.timeout": "20s",
		"approval.commands": "ask", "approval.writes": "ask", "loop.shell_timeout": "5m",
		"judge.price_input": 0.5, "judge.price_output": 2,
	}}), &settings)
	if v := settings.Values["window.high_water"]; v.Value != float64(100000) || !v.Set {
		t.Fatalf("high_water written: %+v", v)
	}
	if v := settings.Values["approval.commands"]; v.Value != "ask" || v.Default != "judge" {
		t.Fatalf("approval written: %+v", v)
	}
	raw, _ := os.ReadFile(o.Paths.ConfigFile)
	for _, want := range []string{"# mine\n", "tokens_hard = 200 # retired\n", "low_water = 50000\n", "high_water = 100000\n", "timeout = \"20s\"\n", "[approval]\n", "commands = \"ask\"\nwrites = \"ask\"\n", "[loop]\nshell_timeout = \"5m\"\n", "price_input = 0.5\n", "price_output = 2"} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("config.toml lacks %q:\n%s", want, raw)
		}
	}
	if info, _ := os.Stat(o.Paths.ConfigFile); info.Mode().Perm() != 0o600 {
		t.Fatal(info.Mode())
	}

	var opened sessionDetails
	json.Unmarshal(c.ok("session.open", map[string]any{"repo": t.TempDir()}), &opened)
	if opened.Budgets != (budgetInfo{}) {
		t.Fatalf("a task took limits from config.toml's [budget]: %+v", opened.Budgets)
	}

	// null removes a setting, so its default applies again.
	json.Unmarshal(c.ok("settings.write", map[string]any{"set": map[string]any{"judge.timeout": nil, "approval.writes": nil}}), &settings)
	raw, _ = os.ReadFile(o.Paths.ConfigFile)
	if strings.Contains(string(raw), "20s") || strings.Contains(string(raw), "writes") || settings.Values["judge.timeout"].Value != "10s" || settings.Values["approval.writes"].Set || settings.Values["approval.writes"].Value != "allow" {
		t.Fatalf("not removed:\n%s\n%+v", raw, settings.Values["judge.timeout"])
	}

	// What Settings may not change, or values nodwyn would not load, leave
	// the file as it was.
	for _, set := range []map[string]any{
		{},
		{"control.url": "http://elsewhere:11434"},
		{"policy.unbound": "confirm"},
		{"servers.jev.url": "https://judge.example"},
		{"window.high_water": "lots"},
		{"window.high_water": 1.5},
		{"window.high_water": 40000}, // below the low water mark
		{"budget.tokens_hard": 1000}, // a task's limits are its own
		{"approval.commands": "sometimes"},
		{"approval.writes": ""},
		{"tasks.mode": "chat"},
		{"control.vision": true},
		{"control.price_input": 1.5},
		{"judge.price_output": -1},
		{"jev.content": "full"},
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
	for _, left := range []string{"control.url", "control.credential", "servers.jev.url", "servers.jev.credential", "mcp.egress", "docs.google_upload_url", "policy.unbound", "loop.max_tool_calls"} {
		if _, ok := settable[left]; ok {
			t.Errorf("%s is settable from the console", left)
		}
	}
}
