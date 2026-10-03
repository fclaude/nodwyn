package rpc

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/fclaude/nodwyn/internal/config"
	"github.com/fclaude/nodwyn/internal/log"
	fake "github.com/fclaude/nodwyn/test/fakes/ollama"
)

// sessionModelKind is the wire name of the record a switch writes (ADR 40).
// The test spells it out so it also runs against a daemon that has never
// heard of it: the log is a wire contract, not a Go constant.
const sessionModelKind = "session.model"

// sessionModelRecord is the session.model payload as the test reads it.
type sessionModelRecord struct {
	From   string `json:"from"`
	Model  string `json:"model"`
	Server string `json:"server"`
	Name   string `json:"name"`
	URL    string `json:"url"`
}

// requested reports the model each llm.req in the log asked, in order.
func requested(t *testing.T, c *client, id string) []string {
	t.Helper()
	var models []string
	for _, ev := range page(t, c, id, 0, 0, 500).Events {
		if ev.Kind != log.KindLLMReq {
			continue
		}
		var rec log.LLMReq
		if err := ev.Decode(&rec); err != nil {
			t.Fatal(err)
		}
		models = append(models, rec.Model)
	}
	return models
}

// A conversation's model is a choice, not the machine's: the next turn goes
// to the model session.model.set named, the log says so, and session.info
// shows it (ADR 40).
func TestSessionModelSetMovesTheNextTurn(t *testing.T) {
	first := fake.NewT(t, fake.Turn{Content: "first answer"})
	defer first.Close()
	second := fake.NewT(t, fake.Turn{Content: "second answer"})
	defer second.Close()
	o := options(t)
	o.Config.Control.URL, o.Config.Control.Model = first.URL, "model-a"
	o.Config.Servers = map[string]config.Server{"other": {URL: second.URL}}
	path, _ := start(t, o)
	c := connect(t, path)
	c.hello()
	id := opened(t, c, t.TempDir())
	subscribed(t, c, id, 0, 100)

	c.ok("session.send", map[string]any{"id": id, "text": "first"})
	c.until(string(log.KindTurnEnd))
	if got := requested(t, c, id); len(got) != 1 || got[0] != "model-a" {
		t.Fatalf("first turn asked %v", got)
	}

	var state struct {
		Model  string `json:"model"`
		Server string `json:"server"`
		Name   string `json:"name"`
	}
	if err := json.Unmarshal(c.ok("session.model.set", map[string]any{"id": id, "server": "other", "model": "model-b"}), &state); err != nil {
		t.Fatal(err)
	}
	if state.Model != "model-b" || state.Server != "other" {
		t.Fatalf("switch reported %+v", state)
	}
	var info struct {
		Model struct {
			Model  string `json:"model"`
			Server string `json:"server"`
		} `json:"model"`
	}
	if err := json.Unmarshal(c.ok("session.info", map[string]any{"id": id}), &info); err != nil {
		t.Fatal(err)
	}
	if info.Model.Model != "model-b" || info.Model.Server != "other" {
		t.Fatalf("session.info model: %+v", info.Model)
	}

	c.ok("session.send", map[string]any{"id": id, "text": "second"})
	c.until(string(log.KindTurnEnd))
	if got := requested(t, c, id); len(got) != 2 || got[1] != "model-b" {
		t.Fatalf("after the switch the log asked %v", got)
	}
	if len(second.Requests) != 1 || second.Requests[0].Model != "model-b" || len(first.Requests) != 1 {
		t.Fatalf("servers saw %d and %d requests", len(first.Requests), len(second.Requests))
	}
	// The switch is in the log, and it names no key: the model, where it is
	// reached, and the server it is on.
	switches := 0
	for _, ev := range page(t, c, id, 0, 0, 500).Events {
		if string(ev.Kind) != sessionModelKind {
			continue
		}
		switches++
		var rec sessionModelRecord
		if err := ev.Decode(&rec); err != nil {
			t.Fatal(err)
		}
		if rec.From != "model-a" || rec.Model != "model-b" || rec.Server != "other" || !strings.Contains(rec.URL, "127.0.0.1") {
			t.Fatalf("session.model: %+v", rec)
		}
	}
	if switches != 1 {
		t.Fatalf("%d session.model records", switches)
	}
}

// A server that is not configured, and a name that is not a model, are
// refused: a switch reaches nowhere the person has not already set up.
func TestSessionModelSetRefusesWhatIsNotConfigured(t *testing.T) {
	model := fake.NewT(t, fake.Turn{Content: "answer"})
	defer model.Close()
	o := options(t)
	o.Config.Control.URL, o.Config.Control.Model = model.URL, "model-a"
	o.Config.Models = map[string]config.Model{"coder": {URL: model.URL, Model: "model-c"}}
	path, _ := start(t, o)
	c := connect(t, path)
	c.hello()
	id := opened(t, c, t.TempDir())

	if r := c.call("session.model.set", map[string]any{"id": id, "server": "nowhere", "model": "m"}); r.Error == nil || !strings.Contains(r.Error.Message, "nowhere") {
		t.Fatalf("unconfigured server: %+v", r.Error)
	}
	if r := c.call("session.model.set", map[string]any{"id": id, "name": "absent"}); r.Error == nil || !strings.Contains(r.Error.Message, "absent") {
		t.Fatalf("unknown model name: %+v", r.Error)
	}
	if r := c.call("session.model.set", map[string]any{"id": id}); r.Error == nil {
		t.Fatal("a switch with nothing to switch to was accepted")
	}
	// A named model of the registry is a choice too, and names it.
	var state struct {
		Model string `json:"model"`
		Name  string `json:"name"`
	}
	if err := json.Unmarshal(c.ok("session.model.set", map[string]any{"id": id, "name": "coder"}), &state); err != nil {
		t.Fatal(err)
	}
	if state.Model != "model-c" || state.Name != "coder" {
		t.Fatalf("named switch reported %+v", state)
	}
}

// A conversation can start on a chosen model over the wire, and the choice
// is this computer's: a paired device opens a task on the [control] model,
// and may neither open one on another model nor move one (ADR 30, ADR 40).
func TestSessionOpensOnAChosenModelButNotFromADevice(t *testing.T) {
	first := fake.NewT(t)
	defer first.Close()
	second := fake.NewT(t, fake.Turn{Content: "second answer"})
	defer second.Close()
	o := options(t)
	o.Config.Control.URL, o.Config.Control.Model = first.URL, "model-a"
	o.Config.Servers = map[string]config.Server{"other": {URL: second.URL}}
	path, _ := start(t, o)
	c := connect(t, path)
	c.hello()
	var info struct {
		ID    string `json:"id"`
		Model struct {
			Model  string `json:"model"`
			Server string `json:"server"`
		} `json:"model"`
	}
	if err := json.Unmarshal(c.ok("session.open", map[string]any{"repo": t.TempDir(), "server": "other", "model": "model-b"}), &info); err != nil {
		t.Fatal(err)
	}
	if info.Model.Model != "model-b" || info.Model.Server != "other" {
		t.Fatalf("opened on %+v", info.Model)
	}
	subscribed(t, c, info.ID, 0, 100)
	c.ok("session.send", map[string]any{"id": info.ID, "text": "hello"})
	c.until(string(log.KindTurnEnd))
	if len(second.Requests) != 1 || second.Requests[0].Model != "model-b" || len(first.Requests) != 0 {
		t.Fatalf("servers saw %d and %d requests", len(first.Requests), len(second.Requests))
	}

	phone := connect(t, path)
	phone.ok("hello", map[string]any{"protocol": 1, "via": "web:phone"})
	if r := phone.call("session.open", map[string]any{"repo": t.TempDir(), "server": "other", "model": "model-b"}); r.Error == nil || !strings.Contains(r.Error.Message, "paired device") {
		t.Fatalf("a device opened a task on a model it chose: %+v", r)
	}
	if r := phone.call("session.model.set", map[string]any{"id": info.ID, "name": "control"}); r.Error == nil || !strings.Contains(r.Error.Message, "paired device") {
		t.Fatalf("a device moved a task to another model: %+v", r)
	}
	var plain struct {
		Model struct {
			Model string `json:"model"`
			Name  string `json:"name"`
		} `json:"model"`
	}
	if err := json.Unmarshal(phone.ok("session.open", map[string]any{"repo": t.TempDir()}), &plain); err != nil {
		t.Fatal(err)
	}
	if plain.Model.Model != "model-a" || plain.Model.Name != "control" {
		t.Fatalf("a device's task opened on %+v", plain.Model)
	}
}
