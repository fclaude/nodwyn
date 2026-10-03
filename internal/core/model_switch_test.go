package core

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/fclaude/nodwyn/internal/config"
	"github.com/fclaude/nodwyn/internal/log"
	"github.com/fclaude/nodwyn/internal/window"
	fake "github.com/fclaude/nodwyn/test/fakes/ollama"
)

// switchOptions is a conversation whose [control] model is on home and whose
// config.toml names an away server its models may be changed to (ADR 40).
func switchOptions(t *testing.T, home, away *fake.Server, adjust func(*config.Config)) Options {
	t.Helper()
	cfg := config.Default()
	cfg.Control.URL, cfg.Control.Model, cfg.Control.Server = home.URL, "control-model", "home"
	cfg.Servers = map[string]config.Server{"home": {URL: home.URL}, "away": {URL: away.URL}}
	cfg.Models = map[string]config.Model{"coder": {Server: "away", Model: "coder-model", NumCtx: 32768}}
	if adjust != nil {
		adjust(&cfg)
	}
	return Options{Config: cfg, Repo: t.TempDir(), Version: "test", NoConfirm: true, LogWriter: &bytes.Buffer{}}
}

// asked lists the model each llm.req in the session's log asked, in order.
func askedModels(t *testing.T, s *Session) []string {
	t.Helper()
	var models []string
	for _, ev := range s.EventsSoFar() {
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

// A conversation opened on a chosen model asks it first, and records that it
// talks to it.
func TestSessionStartsOnAChosenModel(t *testing.T) {
	t.Parallel()
	home := fake.NewT(t, fake.Turn{Content: "unused"})
	defer home.Close()
	away := fake.NewT(t, fake.Turn{Content: "away answer"})
	defer away.Close()
	o := switchOptions(t, home, away, nil)
	o.Model = ModelChoice{Server: "away", Model: "away-model"}
	s, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if got := s.Model(); got.Name != "" || got.Server != "away" || got.Model != "away-model" {
		t.Fatalf("model: %+v", got)
	}
	if err := s.Send("hello"); err != nil {
		t.Fatal(err)
	}
	(&rig{t: t, s: s}).drain("n")
	if len(away.Requests) != 1 || away.Requests[0].Model != "away-model" || len(home.Requests) != 0 {
		t.Fatalf("away saw %d requests, home %d", len(away.Requests), len(home.Requests))
	}
	var start log.SessionStart
	for _, ev := range s.EventsSoFar() {
		if ev.Kind == log.KindSessionStart {
			if err := ev.Decode(&start); err != nil {
				t.Fatal(err)
			}
		}
	}
	if start.Model != "away-model" || start.URL != away.URL {
		t.Fatalf("session.start: %+v", start)
	}
}

// A switch moves the next turn to the other server, says so in the log, and
// every request still replays to its recorded hash: the switched model's
// settings go with its request.
func TestSwitchMovesTheNextTurnAndStillReplays(t *testing.T) {
	t.Parallel()
	home := fake.NewT(t, fake.Turn{Content: "home answer"})
	defer home.Close()
	away := fake.NewT(t, fake.Turn{Content: "away answer"})
	defer away.Close()
	o := switchOptions(t, home, away, nil)
	s, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.Send("first"); err != nil {
		t.Fatal(err)
	}
	(&rig{t: t, s: s}).drain("n")

	state, err := s.SetModel(ModelChoice{Name: "coder", Actor: "tester"})
	if err != nil {
		t.Fatal(err)
	}
	if state.Name != "coder" || state.Server != "away" || state.Model != "coder-model" {
		t.Fatalf("switch: %+v", state)
	}
	if got := s.Model(); got != state {
		t.Fatalf("Model() says %+v, the switch said %+v", got, state)
	}
	if err := s.Send("second"); err != nil {
		t.Fatal(err)
	}
	(&rig{t: t, s: s}).drain("n")
	if got := askedModels(t, s); len(got) != 2 || got[0] != "control-model" || got[1] != "coder-model" {
		t.Fatalf("the log asked %v", got)
	}
	if len(home.Requests) != 1 || len(away.Requests) != 1 || away.Requests[0].Model != "coder-model" {
		t.Fatalf("home saw %d, away %d", len(home.Requests), len(away.Requests))
	}
	records := 0
	for _, ev := range s.EventsSoFar() {
		if ev.Kind != log.KindSessionModel {
			continue
		}
		records++
		var rec log.SessionModel
		if err := ev.Decode(&rec); err != nil {
			t.Fatal(err)
		}
		if rec.From != "control-model" || rec.Model != "coder-model" || rec.Name != "coder" || rec.Server != "away" || rec.Actor != "tester" || rec.URL != away.URL {
			t.Fatalf("session.model: %+v", rec)
		}
	}
	if records != 1 {
		t.Fatalf("%d session.model records", records)
	}
	// Every request still proves itself: the recipe is the session's, and a
	// request whose own settings differ records them (ADR 40).
	events := s.EventsSoFar()
	requests := 0
	for _, ev := range events {
		if ev.Kind != log.KindLLMReq {
			continue
		}
		requests++
		rebuilt, err := window.ReplayRequest(events, ev.Seq)
		if err != nil {
			t.Fatalf("seq %d: %v", ev.Seq, err)
		}
		var rec log.LLMReq
		if err := ev.Decode(&rec); err != nil {
			t.Fatal(err)
		}
		if rebuilt.Model != rec.Model {
			t.Fatalf("seq %d replayed as %q, was %q", ev.Seq, rebuilt.Model, rec.Model)
		}
	}
	if requests != 2 {
		t.Fatalf("%d requests replayed", requests)
	}
}

// A conversation that is longer than the target's context is refused, with
// numbers: it may be compacted first, or the person may choose another model.
func TestSwitchRefusedWhenTheConversationDoesNotFit(t *testing.T) {
	t.Parallel()
	home := fake.NewT(t, fake.Turn{Content: "home answer"})
	defer home.Close()
	away := fake.NewT(t, fake.Turn{Content: "unused"})
	defer away.Close()
	o := switchOptions(t, home, away, func(c *config.Config) {
		c.Window.OutputReserve = 1024
		c.Models["small"] = config.Model{Server: "away", Model: "small-model", NumCtx: 4096}
	})
	s, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.Send(strings.Repeat("a long message ", 1000)); err != nil {
		t.Fatal(err)
	}
	(&rig{t: t, s: s}).drain("n")
	_, err = s.SetModel(ModelChoice{Name: "small"})
	if err == nil || !strings.Contains(err.Error(), "small-model") || !strings.Contains(err.Error(), "compact") {
		t.Fatalf("switch to a small model: %v", err)
	}
	if got := s.Model(); got.Model != "control-model" {
		t.Fatalf("a refused switch changed the model: %+v", got)
	}
	// The same conversation fits a model with room for it.
	if _, err := s.SetModel(ModelChoice{Name: "coder"}); err != nil {
		t.Fatal(err)
	}
}

// A switch is refused while a turn runs: the turn under way has already asked
// its model.
func TestSwitchRefusedWhileATurnRuns(t *testing.T) {
	t.Parallel()
	away := fake.NewT(t, fake.Turn{Content: "unused"})
	defer away.Close()
	home := fake.NewT(t) // never asked: the turn's provider waits
	defer home.Close()
	o := switchOptions(t, home, away, nil)
	o.Provider = waitingProvider{}
	s, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.Send("wait"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for !s.Busy() {
		if time.Now().After(deadline) {
			t.Fatal("the turn never started")
		}
		time.Sleep(time.Millisecond)
	}
	_, err = s.SetModel(ModelChoice{Server: "away", Model: "away-model"})
	if err == nil || !strings.Contains(err.Error(), "a turn is running") {
		t.Fatalf("switch during a turn: %v", err)
	}
	s.Abort()
	(&rig{t: t, s: s}).drain("n")
	if _, err := s.SetModel(ModelChoice{Server: "away", Model: "away-model"}); err != nil {
		t.Fatalf("switch after the turn: %v", err)
	}
}

// A switch reaches only what config.toml names: no address, and no key, comes
// from the choice.
func TestSwitchRefusesUnknownTargets(t *testing.T) {
	t.Parallel()
	home := fake.NewT(t, fake.Turn{Content: "unused"})
	defer home.Close()
	away := fake.NewT(t, fake.Turn{Content: "unused"})
	defer away.Close()
	o := switchOptions(t, home, away, func(c *config.Config) {
		c.Servers["keyed"] = config.Server{Provider: "openai", URL: "https://api.example.test/v1", Credential: "router-key"}
	})
	s, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, c := range []ModelChoice{
		{Server: "nowhere", Model: "m"},
		{Name: "absent"},
		{Server: "away"},
		{Model: "m"},
		{Name: "coder", Server: "away", Model: "m"},
	} {
		if _, err := s.SetModel(c); err == nil {
			t.Fatalf("accepted %+v", c)
		}
	}
	// A keyed model needs the keychain the key is in, and this task has none.
	if _, err := s.SetModel(ModelChoice{Server: "keyed", Model: "m"}); err == nil || !strings.Contains(err.Error(), "keychain") {
		t.Fatalf("keyed model without a keychain: %v", err)
	}
}

// A conversation keeps the model it was switched to when it is resumed: the
// log says which, and that is the person's choice outliving the process.
func TestResumeKeepsTheModelItWasSwitchedTo(t *testing.T) {
	t.Parallel()
	home := fake.NewT(t, fake.Turn{Content: "home answer"}, fake.Turn{Content: "unused"})
	defer home.Close()
	away := fake.NewT(t, fake.Turn{Content: "away answer"})
	defer away.Close()
	o := diskOptions(t)
	o.Config.Control.URL, o.Config.Control.Model = home.URL, "control-model"
	o.Config.Servers = map[string]config.Server{"away": {URL: away.URL}}
	s, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Send("first"); err != nil {
		t.Fatal(err)
	}
	(&rig{t: t, s: s}).drain("n")
	if _, err := s.SetModel(ModelChoice{Server: "away", Model: "away-model"}); err != nil {
		t.Fatal(err)
	}
	id := s.ID()
	s.Close()

	resumed, err := Resume(o, id)
	if err != nil {
		t.Fatal(err)
	}
	defer resumed.Close()
	if got := resumed.Model(); got.Server != "away" || got.Model != "away-model" {
		t.Fatalf("resumed model: %+v", got)
	}
	if err := resumed.Send("second"); err != nil {
		t.Fatal(err)
	}
	(&rig{t: t, s: resumed}).drain("n")
	if len(away.Requests) != 1 || away.Requests[0].Model != "away-model" {
		t.Fatalf("the away server answered %d turns", len(away.Requests))
	}
}

// A model config.toml no longer offers does not make the task unopenable: it
// talks to the [control] model again, and the log says what it lost.
func TestResumeFallsBackWhenTheChosenModelIsGone(t *testing.T) {
	t.Parallel()
	home := fake.NewT(t, fake.Turn{Content: "home answer"}, fake.Turn{Content: "home again"})
	defer home.Close()
	away := fake.NewT(t, fake.Turn{Content: "away answer"})
	defer away.Close()
	o := diskOptions(t)
	o.Config.Control.URL, o.Config.Control.Model = home.URL, "control-model"
	o.Config.Servers = map[string]config.Server{"away": {URL: away.URL}}
	s, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Send("first"); err != nil {
		t.Fatal(err)
	}
	(&rig{t: t, s: s}).drain("n")
	if _, err := s.SetModel(ModelChoice{Server: "away", Model: "away-model"}); err != nil {
		t.Fatal(err)
	}
	id := s.ID()
	s.Close()

	gone := o
	gone.Config.Servers = nil
	resumed, err := Resume(gone, id)
	if err != nil {
		t.Fatal(err)
	}
	defer resumed.Close()
	if got := resumed.Model(); got.Model != "control-model" {
		t.Fatalf("resumed model: %+v", got)
	}
	if err := resumed.Send("second"); err != nil {
		t.Fatal(err)
	}
	(&rig{t: t, s: resumed}).drain("n")
	if len(home.Requests) != 2 {
		t.Fatalf("the control model answered %d turns", len(home.Requests))
	}
	note := ""
	for _, ev := range resumed.EventsSoFar() {
		if ev.Kind != log.KindNote {
			continue
		}
		var n log.Note
		if err := ev.Decode(&n); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(n.Text, "no longer configured") {
			note = n.Text
		}
	}
	if !strings.Contains(note, "away") {
		t.Fatalf("the log does not say what was lost: %q", note)
	}
}

// The registry's own names are choices too, and the endpoint's settings are
// built from config.toml, not from the choice.
func TestSwitchToTheRegistryAndBack(t *testing.T) {
	t.Parallel()
	home := fake.NewT(t, fake.Turn{Content: "home answer"}, fake.Turn{Content: "home again"})
	defer home.Close()
	away := fake.NewT(t, fake.Turn{Content: "away answer"})
	defer away.Close()
	o := switchOptions(t, home, away, nil)
	s, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if got := s.Model(); got.Name != "control" || got.Server != "home" {
		t.Fatalf("a session without a choice: %+v", got)
	}
	if _, err := s.SetModel(ModelChoice{Name: "coder"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Send("away please"); err != nil {
		t.Fatal(err)
	}
	(&rig{t: t, s: s}).drain("n")
	back, err := s.SetModel(ModelChoice{Name: "control"})
	if err != nil {
		t.Fatal(err)
	}
	if back.Name != "control" || back.Model != "control-model" {
		t.Fatalf("back to control: %+v", back)
	}
	if err := s.Send("home please"); err != nil {
		t.Fatal(err)
	}
	(&rig{t: t, s: s}).drain("n")
	if got := askedModels(t, s); len(got) != 2 || got[0] != "coder-model" || got[1] != "control-model" {
		t.Fatalf("the log asked %v", got)
	}
	if len(home.Requests) != 1 || len(away.Requests) != 1 {
		t.Fatalf("home saw %d, away %d", len(home.Requests), len(away.Requests))
	}
}

// Whether images go is asked of the model that answers: after a switch to a
// model on the same server that takes none, an image confirmed for that
// server is withheld, and the new model was asked (ADR 22 §3, ADR 40).
func TestSwitchAsksTheNewModelWhetherItTakesImages(t *testing.T) {
	t.Parallel()
	r := imageRig(t, true, func(o *Options) {
		o.Config.Control.Server = "home"
		o.Config.Servers = map[string]config.Server{"home": {URL: o.Config.Control.URL}}
	}, fake.Turn{Content: "I cannot see it."})
	path, _ := picture(t, "shot.png", 64, 64)
	if _, _, err := r.attachAnswering(path, "shot.png", "y"); err != nil {
		t.Fatal(err)
	}
	r.srv.SetCapabilities("completion", "tools")
	if _, err := r.s.SetModel(ModelChoice{Server: "home", Model: "text-only"}); err != nil {
		t.Fatal(err)
	}
	if e := r.turn("what is this?"); e != "" {
		t.Fatal(e)
	}
	if n := countImages(t, r.srv.Requests[0]); n != 0 || r.srv.Requests[0].Model != "text-only" {
		t.Fatalf("%d images went to %s", n, r.srv.Requests[0].Model)
	}
	if r.srv.Shows() != 2 {
		t.Fatalf("asked %d times whether a model takes images, not once per model", r.srv.Shows())
	}
	reqs, _ := r.requests()
	if len(reqs[0].Withheld) != 1 || reqs[0].Withheld[0].Reason != window.ImageNoVision {
		t.Fatalf("withheld: %+v", reqs[0].Withheld)
	}
}

// A conversation opened on a chosen model talks to it again when resumed:
// the choice is in its log from the start, as a switch is.
func TestResumeKeepsTheModelItOpenedOn(t *testing.T) {
	t.Parallel()
	home := fake.NewT(t)
	defer home.Close()
	away := fake.NewT(t, fake.Turn{Content: "away answer"}, fake.Turn{Content: "away again"})
	defer away.Close()
	o := diskOptions(t)
	o.Config.Control.URL, o.Config.Control.Model = home.URL, "control-model"
	o.Config.Servers = map[string]config.Server{"away": {URL: away.URL}}
	o.Model = ModelChoice{Server: "away", Model: "away-model", Actor: "opener"}
	s, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Send("first"); err != nil {
		t.Fatal(err)
	}
	(&rig{t: t, s: s}).drain("n")
	var rec log.SessionModel
	for _, ev := range s.EventsSoFar() {
		if ev.Kind == log.KindSessionModel {
			if err := ev.Decode(&rec); err != nil {
				t.Fatal(err)
			}
		}
	}
	if rec.From != "" || rec.Model != "away-model" || rec.Server != "away" || rec.Actor != "opener" || rec.Context == nil {
		t.Fatalf("session.model at open: %+v", rec)
	}
	id := s.ID()
	s.Close()

	// The daemon resumes with the options it opens with, which choose no model.
	o.Model = ModelChoice{}
	resumed, err := Resume(o, id)
	if err != nil {
		t.Fatal(err)
	}
	defer resumed.Close()
	if got := resumed.Model(); got.Server != "away" || got.Model != "away-model" {
		t.Fatalf("resumed on %+v", got)
	}
	if err := resumed.Send("second"); err != nil {
		t.Fatal(err)
	}
	(&rig{t: t, s: resumed}).drain("n")
	if len(away.Requests) != 2 || len(home.Requests) != 0 {
		t.Fatalf("away answered %d turns, home %d", len(away.Requests), len(home.Requests))
	}
}

// On a chat-completions server that lists its models' context (OpenRouter's
// shape), a switch sizes the window for the model that will answer, as
// opening on it would, and is refused when the conversation cannot fit it:
// a model whose num_ctx config.toml does not set is judged by what the
// server says. Every request still replays by the recipe in the log.
func TestSwitchSizesTheWindowForTheModel(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/models":
			w.Write([]byte(`{"data":[{"id":"long","context_length":1048576},{"id":"mid","context_length":131072},{"id":"short","context_length":32768}]}`))
		case "/v1/chat/completions":
			w.Write([]byte(`{"choices":[{"message":` + oaiText("done") + `,"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	cfg := config.Default()
	cfg.Servers = map[string]config.Server{"or": {Provider: "openai", URL: srv.URL + "/v1"}}
	cfg.Control = config.Model{Server: "or", Provider: "openai", URL: srv.URL + "/v1", Model: "long"}
	logb := &bytes.Buffer{}
	s, err := New(Options{Config: cfg, Repo: t.TempDir(), NoConfirm: true, LogWriter: logb})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	r := &rig{t: t, s: s, logb: logb}
	if got := r.evalInt("(assoc-ref (window-limits) 'high-water)"); got != 774144 {
		t.Fatalf("opened with high water %d", got)
	}
	// About 40,000 tokens the window never trims: a user message.
	if e := r.turn(strings.Repeat("lorem ipsum dolor sit amet ", 4500)); e != "" {
		t.Fatal(e)
	}
	if _, err := s.SetModel(ModelChoice{Server: "or", Model: "short"}); err == nil || !strings.Contains(err.Error(), "compact it first") {
		t.Fatalf("a 32768-token model took a ~40k-token conversation: %v", err)
	}
	if got := s.Model(); got.Model != "long" {
		t.Fatalf("a refused switch moved the conversation to %+v", got)
	}
	if _, err := s.SetModel(ModelChoice{Server: "or", Model: "mid"}); err != nil {
		t.Fatal(err)
	}
	// (131072 − 16384) × 3/4, as opening on mid would size it.
	if got := r.evalInt("(assoc-ref (window-limits) 'high-water)"); got != 86016 {
		t.Fatalf("high water after the switch %d", got)
	}
	if got := r.evalInt("(assoc-ref (window-limits) 'ceiling)"); got != 114688 {
		t.Fatalf("ceiling after the switch %d", got)
	}
	if got := r.evalInt("(assoc-ref (model-info) 'num-ctx)"); got != 131072 {
		t.Fatalf("num-ctx after the switch %d", got)
	}
	if !strings.Contains(logb.String(), "sized for mid's context of 131072 tokens") {
		t.Fatal("the log does not say how the window was sized for mid")
	}
	if e := r.turn("and now?"); e != "" {
		t.Fatal(e)
	}
	if n := replayAll(t, logb); n != 2 {
		t.Fatalf("replayed %d requests", n)
	}
}
