package rpc

import (
	"encoding/json"
	"maps"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/fclaude/nodwyn/internal/config"
	"github.com/fclaude/nodwyn/internal/core"
	"github.com/fclaude/nodwyn/internal/event"
	"github.com/fclaude/nodwyn/internal/log"
	"github.com/fclaude/nodwyn/test/fakes/chat"
)

var levels = []string{"low", "medium", "high"}

// withChat is a daemon with no models configured, a keychain with no
// passphrase, and a fake chat-completions provider that wants a key.
func withChat(t *testing.T) (*client, *chat.Server, core.Options, string) {
	t.Helper()
	srv := chat.New("sk-good-key-123456",
		chat.Model{ID: "thinker", Efforts: levels},
		chat.Model{ID: "plain"},
		chat.Model{ID: "claude", Caches: true},
	)
	t.Cleanup(srv.Close)
	o := options(t)
	path, _ := start(t, o)
	c := connect(t, path)
	c.hello()
	c.ok("keychain.create", map[string]any{"no_passphrase": true})
	return c, srv, o, path
}

// into decodes an answer into v, which it empties first: decoding into a
// value that was decoded into before keeps what the new answer leaves out
// (an omitted field, a slice's old elements), which would hide a field that
// went away.
func into(t *testing.T, raw json.RawMessage, v any) {
	t.Helper()
	reflect.ValueOf(v).Elem().SetZero()
	if err := json.Unmarshal(raw, v); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// A provider is added with its key, found wrong by a test request that says
// what the server answered, replaced in place, and found right: the key goes
// to the keychain and its binding rows, never into config.toml, and never
// back out in an answer.
func TestProviderSavedWithItsKeyThenTested(t *testing.T) {
	c, srv, o, _ := withChat(t)
	configFile := o.Paths.ConfigFile
	var settings settingsResult
	into(t, c.ok("settings.provider.write", map[string]any{"provider": "openai", "url": srv.V1() + "/", "api_key": "sk-wrong-key-000000"}), &settings)
	if len(settings.Servers) != 1 {
		t.Fatalf("servers: %+v", settings.Servers)
	}
	p := settings.Servers[0]
	if p.Name != "127-0-0-1" || p.Provider != "openai" || p.URL != srv.V1() || p.Credential != "model-127-0-0-1" || p.Key != "ready" || p.Inline || len(p.Uses) != 0 {
		t.Fatalf("provider: %+v", p)
	}
	cfg := read(t, configFile)
	if !strings.Contains(cfg, "[servers.127-0-0-1]\nprovider = \"openai\"\nurl = \""+srv.V1()+"\"\ncredential = \"model-127-0-0-1\"\n") || strings.Contains(cfg, "sk-") {
		t.Fatalf("config.toml:\n%s", cfg)
	}
	var result testResult
	into(t, c.ok("models.test", map[string]any{"server": "127-0-0-1", "model": "thinker"}), &result)
	if result.OK || result.State != "rejected" || result.Status != 401 || !strings.Contains(result.Detail, "did not accept the key") || strings.Contains(result.Detail, "sk-wrong") {
		t.Fatalf("a wrong key: %+v", result)
	}
	// Replaced, the key is the same credential: one in the keychain, one row.
	into(t, c.ok("settings.provider.write", map[string]any{"name": "127-0-0-1", "provider": "openai", "url": srv.V1(), "api_key": "sk-good-key-123456"}), &settings)
	if settings.Keychain.Credentials != 1 || settings.Servers[0].Credential != "model-127-0-0-1" {
		t.Fatalf("after replacing the key: %+v", settings)
	}
	// Its rows allow it to POST to the model and GET the model list, there
	// and nowhere else.
	bindings := read(t, o.Paths.Bindings)
	if strings.Count(bindings, "[[bind]]") != 2 || strings.Count(bindings, `dest = "127.0.0.1"`) != 2 || strings.Contains(bindings, "sk-") || !strings.Contains(bindings, `methods = ["POST"]`) || !strings.Contains(bindings, `methods = ["GET"]`) {
		t.Fatalf("bindings:\n%s", bindings)
	}
	into(t, c.ok("models.test", map[string]any{"server": "127-0-0-1", "model": "thinker"}), &result)
	if !result.OK || result.State != "ok" || result.Model != "thinker" {
		t.Fatalf("the right key: %+v", result)
	}
	// A model the provider does not have is said so, not as a bad key.
	into(t, c.ok("models.test", map[string]any{"server": "127-0-0-1", "model": "absent"}), &result)
	if result.OK || result.State != "no_model" || result.Status != 404 {
		t.Fatalf("a missing model: %+v", result)
	}
	// What the provider saw: the key it wanted, on every request, and no effort
	// nobody chose.
	for i, r := range srv.Requests() {
		if r.HasEffort {
			t.Errorf("request %d carried an effort: %+v", i, r)
		}
	}
	if got := srv.Requests(); len(got) != 3 || got[0].Authenticated || !got[1].Authenticated {
		t.Fatalf("requests: %+v", got)
	}
}

// An effort the model takes is tried with it; one the provider refuses is
// said as a refusal that names the effort, and one nobody declared for the
// model is refused before anything is sent.
func TestModelTestTriesTheEffort(t *testing.T) {
	c, srv, _, _ := withChat(t)
	c.ok("settings.provider.write", map[string]any{"provider": "openai", "url": srv.V1(), "api_key": "sk-good-key-123456"})
	c.ok("settings.model.write", map[string]any{"server": "127-0-0-1", "model": "thinker", "efforts": levels, "effort": "medium"})
	// "plain" is declared to take an effort the provider does not know.
	c.ok("settings.model.write", map[string]any{"server": "127-0-0-1", "model": "plain", "efforts": []string{"high"}})
	var result testResult
	into(t, c.ok("models.test", map[string]any{"name": "127-0-0-1-thinker", "effort": "high"}), &result)
	if !result.OK || result.Effort != "high" {
		t.Fatalf("an effort the model takes: %+v", result)
	}
	into(t, c.ok("models.test", map[string]any{"name": "127-0-0-1-plain", "effort": "high"}), &result)
	if result.OK || result.State != "refused" || result.Status != 400 || !strings.Contains(result.Detail, `effort "high"`) {
		t.Fatalf("an effort the provider refuses: %+v", result)
	}
	if r := c.call("models.test", map[string]any{"server": "127-0-0-1", "model": "thinker", "effort": "extreme"}); r.Error == nil || !strings.Contains(r.Error.Message, "does not take the effort") {
		t.Fatalf("an effort nobody declared: %+v", r.Error)
	}
	got := srv.Requests()
	if len(got) != 2 || got[0].Effort != "high" || got[0].Answered != 200 || got[1].Answered != 400 {
		t.Fatalf("the provider saw %+v", got)
	}
}

// Readiness says what to fix: a key that is not stored, one that is locked
// away, one stored but not allowed for the host, and a server that does not
// answer, each in its own state with what to do.
func TestModelsStatusSaysWhatToFix(t *testing.T) {
	srv := chat.New("sk-good-key-123456", chat.Model{ID: "thinker", Efforts: levels})
	defer srv.Close()
	o := options(t)
	raw := "[servers.keyed]\nprovider = \"openai\"\nurl = \"" + srv.V1() + "\"\ncredential = \"keyed-key\"\n\n" +
		"[servers.down]\nprovider = \"openai\"\nurl = \"http://127.0.0.1:1/v1\"\n\n" +
		"[control]\nserver = \"keyed\"\nmodel = \"thinker\"\n\n[models.gone]\nserver = \"down\"\nmodel = \"x\"\n"
	if err := os.MkdirAll(o.Paths.ConfigDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(o.Paths.ConfigFile, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	var err error
	if o.Config, err = config.Parse([]byte(raw)); err != nil {
		t.Fatal(err)
	}
	path, stop := start(t, o)
	c := connect(t, path)
	c.hello()
	states := func() map[string]modelStatus {
		forgetProbes()
		var status []modelStatus
		into(t, c.ok("models.status", map[string]any{}), &status)
		out := map[string]modelStatus{}
		for _, s := range status {
			out[s.Name] = s
		}
		return out
	}
	// No keychain at all: the key was never stored.
	if got := states(); got["control"].State != "needs_key" || !strings.Contains(got["control"].Detail, "add this provider's API key") || got["gone"].State != "unreachable" {
		t.Fatalf("no keychain: %+v", got)
	}
	c.ok("keychain.create", map[string]any{"no_passphrase": true})
	if got := states(); got["control"].State != "needs_key" {
		t.Fatalf("a keychain with no key: %+v", got["control"])
	}
	var stored credentialsResult
	into(t, c.ok("credentials.add", map[string]any{"class": "keyed-key", "value": "sk-good-key-123456"}), &stored)
	if got := states(); got["control"].State != "unbound" || !strings.Contains(got["control"].Detail, "Connections › Credentials") {
		t.Fatalf("a key nothing allows: %+v", got["control"])
	}
	c.ok("credentials.bind", map[string]any{"id": stored.Credentials[0].ID, "dest": "127.0.0.1", "methods": []string{"POST"}, "position": "header:Authorization:Bearer"})
	if got := states(); got["control"].State != "ready" {
		t.Fatalf("a key allowed for the host: %+v", got["control"])
	}
	stop()
	// A daemon started with the keychain locked says so.
	path, _ = start(t, o)
	c = connect(t, path)
	c.hello()
	if got := states(); got["control"].State != "locked" || !strings.Contains(got["control"].Detail, "unlock") {
		t.Fatalf("a locked keychain: %+v", got["control"])
	}
}

// The refusals each say why, and a provider's key never follows it to a
// new host.
func TestProviderWriteRefusals(t *testing.T) {
	c, srv, o, _ := withChat(t)
	configFile := o.Paths.ConfigFile
	c.ok("settings.provider.write", map[string]any{"provider": "openai", "url": srv.V1(), "api_key": "sk-good-key-123456"})
	for name, tc := range map[string]struct {
		method string
		params map[string]any
		want   string
	}{
		"the same address twice":  {"settings.provider.write", map[string]any{"provider": "openai", "url": srv.V1()}, `already configured, as "127-0-0-1"`},
		"a provider kind":         {"settings.provider.write", map[string]any{"provider": "gemini", "url": "http://h"}, "provider must be"},
		"not an address":          {"settings.provider.write", map[string]any{"provider": "openai", "url": "ftp://h"}, "http or https"},
		"a key for Ollama":        {"settings.provider.write", map[string]any{"provider": "ollama", "url": "http://workshop:11434", "api_key": "sk-x"}, "takes no dialect and no key"},
		"a key over plain http":   {"settings.provider.write", map[string]any{"provider": "openai", "url": "http://api.example.test/v1", "api_key": "sk-x"}, "over https"},
		"a dialect nobody knows":  {"settings.provider.write", map[string]any{"provider": "openai", "url": "https://other.example/v1", "dialect": "bing"}, "dialect"},
		"a provider not there":    {"settings.provider.write", map[string]any{"name": "nowhere", "provider": "openai", "url": "https://x.example/v1"}, `No provider named "nowhere"`},
		"removing one not there":  {"settings.provider.remove", map[string]any{"name": "nowhere"}, `No provider named "nowhere"`},
		"a model on no provider":  {"settings.model.write", map[string]any{"server": "nowhere", "model": "m"}, `No provider named "nowhere"`},
		"a model with no id":      {"settings.model.write", map[string]any{"server": "127-0-0-1", "model": " "}, "model is required"},
		"a default not in effort": {"settings.model.write", map[string]any{"server": "127-0-0-1", "model": "m", "efforts": levels, "effort": "max"}, "not one the model takes (low, medium, high)"},
		"an effort with no list":  {"settings.model.write", map[string]any{"server": "127-0-0-1", "model": "m", "effort": "low"}, "declares no efforts"},
		"a badly spelled effort":  {"settings.model.write", map[string]any{"server": "127-0-0-1", "model": "m", "efforts": []string{"Very High"}}, "lower-case word"},
		"removing the default":    {"settings.model.remove", map[string]any{"name": "control"}, "default model"},
		"removing a model absent": {"settings.model.remove", map[string]any{"name": "absent"}, `No model named "absent"`},
	} {
		before := read(t, configFile)
		r := c.call(tc.method, tc.params)
		if r.Error == nil || !strings.Contains(r.Error.Message, tc.want) {
			t.Errorf("%s: %+v, want an error saying %q", name, r.Error, tc.want)
		}
		if read(t, configFile) != before {
			t.Errorf("%s: config.toml changed", name)
		}
	}
	// A provider models are on is not removed from under them; once they are
	// gone it is, and its key stays in the keychain.
	c.ok("settings.model.write", map[string]any{"server": "127-0-0-1", "model": "thinker"})
	c.ok("settings.control.write", map[string]any{"name": "127-0-0-1-thinker"})
	r := c.call("settings.provider.remove", map[string]any{"name": "127-0-0-1"})
	if r.Error == nil || !strings.Contains(r.Error.Message, "still used by the default model, 127-0-0-1-thinker") {
		t.Fatalf("removing a provider in use: %+v", r.Error)
	}
	// The key follows a change of path, not of host.
	var settings settingsResult
	into(t, c.ok("settings.provider.write", map[string]any{"name": "127-0-0-1", "provider": "openai", "url": srv.V1() + "/beta"}), &settings)
	if settings.Servers[0].Credential != "model-127-0-0-1" || settings.Servers[0].Key != "ready" {
		t.Fatalf("a new path: %+v", settings.Servers[0])
	}
	into(t, c.ok("settings.provider.write", map[string]any{"name": "127-0-0-1", "provider": "openai", "url": "http://localhost:" + strings.Split(srv.URL, ":")[2] + "/v1"}), &settings)
	if p := settings.Servers[0]; p.Credential != "" || p.Key != "" || settings.Keychain.Credentials != 1 {
		t.Fatalf("a new host kept the old host's key: %+v", p)
	}
}

// The models a person can choose are the list's, each with the efforts it
// takes; [control] takes its efforts from the entry for the same model, and
// the same model is not offered twice.
func TestModelsAreListedWithTheirEfforts(t *testing.T) {
	c, srv, o, _ := withChat(t)
	configFile := o.Paths.ConfigFile
	c.ok("settings.provider.write", map[string]any{"provider": "openai", "url": srv.V1(), "api_key": "sk-good-key-123456"})
	var settings settingsResult
	into(t, c.ok("settings.model.write", map[string]any{"server": "127-0-0-1", "model": "thinker", "efforts": levels, "effort": "medium"}), &settings)
	into(t, c.ok("settings.model.write", map[string]any{"server": "127-0-0-1", "model": "plain"}), &settings)
	if len(settings.Choices) != 2 || settings.Choices[0].Default {
		t.Fatalf("with no default yet: %+v", settings.Choices)
	}
	// Choosing the default by name, with an effort of its own.
	into(t, c.ok("settings.control.write", map[string]any{"name": "127-0-0-1-thinker", "effort": "high"}), &settings)
	want := []modelChoice{
		{Name: "control", Server: "127-0-0-1", Provider: "openai", Model: "thinker", Host: strings.TrimPrefix(srv.URL, "http://"), Efforts: levels, Effort: "high", Default: true},
		{Name: "127-0-0-1-plain", Server: "127-0-0-1", Provider: "openai", Model: "plain", Host: strings.TrimPrefix(srv.URL, "http://")},
	}
	if !reflect.DeepEqual(settings.Choices, want) {
		t.Fatalf("choices:\n got %+v\nwant %+v", settings.Choices, want)
	}
	cfg := read(t, configFile)
	// The default lists no efforts of its own: they are the entry's, found
	// by the model it is, so they cannot go stale beside it.
	if !strings.Contains(cfg, "[control]\nserver = \"127-0-0-1\"\nmodel = \"thinker\"\neffort = \"high\"\n") {
		t.Fatalf("config.toml:\n%s", cfg)
	}
	if r := c.call("settings.model.remove", map[string]any{"name": "127-0-0-1-thinker"}); r.Error == nil || !strings.Contains(r.Error.Message, "is the default model") {
		t.Fatalf("removing the default's entry: %+v", r.Error)
	}
	// The default's effort alone, left out the model's own, or cleared.
	into(t, c.ok("settings.control.write", map[string]any{"name": "127-0-0-1-thinker"}), &settings)
	if settings.Choices[0].Effort != "medium" {
		t.Fatalf("the entry's own default: %+v", settings.Choices[0])
	}
	into(t, c.ok("settings.control.write", map[string]any{"server": "127-0-0-1", "model": "thinker", "effort": ""}), &settings)
	if settings.Choices[0].Effort != "" || !reflect.DeepEqual(settings.Choices[0].Efforts, levels) {
		t.Fatalf("a default cleared: %+v", settings.Choices[0])
	}
	if r := c.call("settings.control.write", map[string]any{"name": "127-0-0-1-plain", "effort": "low"}); r.Error == nil || !strings.Contains(r.Error.Message, "declares no efforts") {
		t.Fatalf("an effort for a model that takes none: %+v", r.Error)
	}
	// A model that is removed is no longer offered.
	into(t, c.ok("settings.model.remove", map[string]any{"name": "127-0-0-1-plain"}), &settings)
	if len(settings.Choices) != 1 || strings.Contains(read(t, configFile), "plain") {
		t.Fatalf("after removing: %+v", settings.Choices)
	}
}

// A task starts on the model and effort chosen for it, an effort alone is
// changed between turns, and one chosen while a response runs waits for its
// end: the provider sees each effort on the request it was meant for.
func TestSessionEffortFromOpenToQueuedChange(t *testing.T) {
	c, srv, _, path := withChat(t)
	c.ok("settings.provider.write", map[string]any{"provider": "openai", "url": srv.V1(), "api_key": "sk-good-key-123456"})
	c.ok("settings.model.write", map[string]any{"server": "127-0-0-1", "model": "thinker", "efforts": levels, "effort": "medium"})
	c.ok("settings.model.write", map[string]any{"server": "127-0-0-1", "model": "plain"})
	c.ok("settings.control.write", map[string]any{"name": "127-0-0-1-plain"})
	type state struct {
		Name    string   `json:"name"`
		Model   string   `json:"model"`
		Effort  string   `json:"effort"`
		Efforts []string `json:"efforts"`
		Queued  *state   `json:"queued"`
		Notice  string   `json:"notice"`
	}
	var info struct {
		ID    string `json:"id"`
		Model state  `json:"model"`
	}
	if r := c.call("session.open", map[string]any{"repo": t.TempDir(), "name": "127-0-0-1-plain", "effort": "high"}); r.Error == nil || !strings.Contains(r.Error.Message, "takes no effort setting") {
		t.Fatalf("an effort for a model that takes none: %+v", r.Error)
	}
	into(t, c.ok("session.open", map[string]any{"repo": t.TempDir(), "name": "127-0-0-1-thinker", "effort": "high"}), &info)
	if info.Model.Name != "127-0-0-1-thinker" || info.Model.Effort != "high" || !reflect.DeepEqual(info.Model.Efforts, levels) {
		t.Fatalf("opened on %+v", info.Model)
	}
	id := info.ID
	subscribed(t, c, id, 0, 100)
	c.ok("session.send", map[string]any{"id": id, "text": "first"})
	c.until(string(log.KindTurnEnd))

	var set state
	into(t, c.ok("session.model.set", map[string]any{"id": id, "name": "127-0-0-1-thinker", "effort": "low"}), &set)
	if set.Effort != "low" || set.Model != "thinker" {
		t.Fatalf("an effort alone: %+v", set)
	}
	// A turn that is running: the choice waits, and says so.
	srv.Hold()
	c.ok("session.send", map[string]any{"id": id, "text": "second"})
	for i := 0; srv.Waiting() == 0; i++ {
		if i > 500 {
			t.Fatal("the request never reached the provider")
		}
		time.Sleep(10 * time.Millisecond)
	}
	into(t, c.ok("session.model.set", map[string]any{"id": id, "name": "127-0-0-1-thinker", "effort": "medium"}), &set)
	if set.Effort != "low" || set.Queued == nil || set.Queued.Effort != "medium" {
		t.Fatalf("a choice while a response runs: %+v", set)
	}
	into(t, c.ok("session.info", map[string]any{"id": id}), &info)
	if info.Model.Queued == nil || info.Model.Queued.Effort != "medium" {
		t.Fatalf("session.info: %+v", info.Model)
	}
	srv.Release()
	c.until(string(log.KindTurnEnd))
	// The choice applies as the turn ends, before it is idle: until then it is
	// still the one that is queued.
	c.until(event.Status)
	into(t, c.ok("session.info", map[string]any{"id": id}), &info)
	if info.Model.Effort != "medium" || info.Model.Queued != nil {
		t.Fatalf("after the response: %+v", info.Model)
	}
	c.ok("session.send", map[string]any{"id": id, "text": "third"})
	c.until(string(log.KindTurnEnd))
	var seen []string
	for _, r := range srv.Requests() {
		seen = append(seen, r.Model+":"+r.Effort)
	}
	if want := []string{"thinker:high", "thinker:low", "thinker:medium"}; !reflect.DeepEqual(seen[len(seen)-3:], want) {
		t.Fatalf("the provider saw %v, want it to end with %v", seen, want)
	}
	// Choosing is this computer's act: a paired device may not even change
	// the effort of a conversation (ADR 30, ADR 40).
	phone := connect(t, path)
	phone.ok("hello", map[string]any{"protocol": 1, "via": "web:phone"})
	if r := phone.call("session.model.set", map[string]any{"id": id, "name": "127-0-0-1-thinker", "effort": "high"}); r.Error == nil || !strings.Contains(r.Error.Message, "paired device") {
		t.Fatalf("a device changed the effort: %+v", r)
	}
	if r := phone.call("session.open", map[string]any{"repo": t.TempDir(), "effort": "high"}); r.Error == nil || !strings.Contains(r.Error.Message, "paired device") {
		t.Fatalf("a device opened a task with an effort: %+v", r)
	}
	if r := phone.call("models.test", map[string]any{"name": "control"}); r.Error == nil || !strings.Contains(r.Error.Message, "paired device") {
		t.Fatalf("a device tried a model: %+v", r)
	}
}

// Choosing another default does not lose the first: it stays in the list of
// models, as it was.
func TestChoosingAnotherDefaultKeepsThePreviousOne(t *testing.T) {
	c, srv, _, _ := withChat(t)
	c.ok("settings.provider.write", map[string]any{"provider": "openai", "url": srv.V1(), "api_key": "sk-good-key-123456"})
	c.ok("settings.control.write", map[string]any{"server": "127-0-0-1", "model": "plain"})
	var settings settingsResult
	into(t, c.ok("settings.control.write", map[string]any{"server": "127-0-0-1", "model": "thinker"}), &settings)
	var names []string
	for _, choice := range settings.Choices {
		names = append(names, choice.Name+":"+choice.Model)
	}
	if want := []string{"control:thinker", "127-0-0-1-plain:plain"}; !reflect.DeepEqual(names, want) {
		t.Fatalf("choices %v, want %v", names, want)
	}
	// Choosing it again changes nothing about the list.
	into(t, c.ok("settings.control.write", map[string]any{"name": "127-0-0-1-plain"}), &settings)
	names = nil
	for _, choice := range settings.Choices {
		names = append(names, choice.Name+":"+choice.Model)
	}
	if want := []string{"control:plain", "127-0-0-1-thinker:thinker"}; !reflect.DeepEqual(names, want) {
		t.Fatalf("choices after choosing the first again: %v, want %v", names, want)
	}
}

// A model's entry says whether its provider is asked to cache its prompt,
// next to the rest of its settings, and the default model, which names the
// entry, is asked the same; a word that means nothing is refused.
func TestModelWriteSetsCacheControl(t *testing.T) {
	o := options(t)
	raw := "[servers.openrouter]\nprovider = \"openai\"\nurl = \"https://openrouter.ai/api/v1\"\n\n[control]\nserver = \"openrouter\"\nmodel = \"anthropic/claude-sonnet-5.5\"\n"
	if err := os.MkdirAll(o.Paths.ConfigDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(o.Paths.ConfigFile, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	var err error
	if o.Config, err = config.Parse([]byte(raw)); err != nil {
		t.Fatal(err)
	}
	path, _ := start(t, o)
	c := connect(t, path)
	c.hello()
	model := map[string]any{"server": "openrouter", "model": "anthropic/claude-sonnet-5.5"}
	with := func(settings map[string]any) map[string]any {
		p := maps.Clone(model)
		p["settings"] = settings
		return p
	}
	var settings settingsResult
	into(t, c.ok("settings.model.write", with(map[string]any{"cache_control": "1h"})), &settings)
	if got := choice(t, settings, "control").Settings.CacheControl; got != "1h" {
		t.Fatalf("the default model's cache_control after setting it: %q", got)
	}
	cfg, err := config.Parse([]byte(read(t, o.Paths.ConfigFile)))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Control.CacheControl != "" || cfg.Models["openrouter-anthropic-claude-sonnet-5-5"].CacheControl != "1h" {
		t.Fatalf("not saved in the model's entry alone:\n%s", read(t, o.Paths.ConfigFile))
	}
	if r := c.call("settings.model.write", with(map[string]any{"cache_control": "forever"})); r.Error == nil || !strings.Contains(r.Error.Message, "cache_control") {
		t.Fatalf("a word nobody sends: %+v", r.Error)
	}
	// Saving the same model as the default again keeps it, as it keeps the
	// rest of the model's settings.
	c.ok("settings.control.write", model)
	if !strings.Contains(read(t, o.Paths.ConfigFile), "cache_control = \"1h\"") {
		t.Fatalf("saving the model dropped it:\n%s", read(t, o.Paths.ConfigFile))
	}
	into(t, c.ok("settings.model.write", with(map[string]any{})), &settings)
	if strings.Contains(read(t, o.Paths.ConfigFile), "cache_control") || choice(t, settings, "control").Settings.CacheControl != "" {
		t.Fatalf("not removed:\n%s", read(t, o.Paths.ConfigFile))
	}
}

// choice is the model named name among settings' choices.
func choice(t *testing.T, settings settingsResult, name string) modelChoice {
	t.Helper()
	for _, c := range settings.Choices {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("no choice %q in %+v", name, settings.Choices)
	return modelChoice{}
}

// A test request asks what a task's would: a model set to have its prompt
// cached is sent the ask, and a provider that refuses it is found by the
// test, with what to turn off, not by the first task.
func TestModelTestCarriesTheAskToCache(t *testing.T) {
	c, srv, _, _ := withChat(t)
	c.ok("settings.provider.write", map[string]any{"provider": "openai", "url": srv.V1(), "api_key": "sk-good-key-123456"})
	var result testResult
	c.ok("settings.control.write", map[string]any{"server": "127-0-0-1", "model": "claude"})
	c.ok("settings.model.write", map[string]any{"server": "127-0-0-1", "model": "claude", "settings": map[string]any{"cache_control": "ephemeral"}})
	into(t, c.ok("models.test", map[string]any{"name": "control"}), &result)
	if !result.OK || result.CacheControl != "ephemeral" {
		t.Fatalf("a model that caches when asked: %+v", result)
	}
	if got := srv.Requests(); len(got) != 1 || got[0].CacheControl != `{"type":"ephemeral"}` {
		t.Fatalf("the provider was sent %+v", got)
	}
	// Another model starts from its own defaults; asked to cache it is refused.
	c.ok("settings.control.write", map[string]any{"server": "127-0-0-1", "model": "thinker"})
	c.ok("settings.model.write", map[string]any{"server": "127-0-0-1", "model": "thinker", "settings": map[string]any{"cache_control": "ephemeral"}})
	into(t, c.ok("models.test", map[string]any{"name": "control"}), &result)
	if result.OK || result.State != "refused" || result.Status != 400 || !strings.Contains(result.Detail, "turn Prompt cache off") {
		t.Fatalf("a provider that refuses the ask: %+v", result)
	}
}
