package core

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/fclaude/nodwyn/internal/config"
	"github.com/fclaude/nodwyn/internal/log"
	"github.com/fclaude/nodwyn/internal/window"
	"github.com/fclaude/nodwyn/test/fakes/chat"
	fake "github.com/fclaude/nodwyn/test/fakes/ollama"
)

var thinkingEfforts = []string{"low", "medium", "high"}

// effortOptions is a conversation whose [control] model takes efforts and a
// provider that refuses a reasoning_effort it should not have been sent
// (test/fakes/chat), so a request that carries the wrong thing fails the
// turn instead of passing.
func effortOptions(t *testing.T, srv *chat.Server) Options {
	t.Helper()
	// Read as config.toml is, so the servers are resolved as the daemon's are.
	// [control] lists no efforts of its own: the entry for the same model does,
	// once (Config.Efforts).
	cfg, err := config.Parse([]byte(`
[servers.chat]
provider = "openai"
url = "` + srv.V1() + `"

[control]
server = "chat"
model = "thinker"
effort = "medium"

[models.thinker]
server = "chat"
model = "thinker"
efforts = ["low", "medium", "high"]
effort = "low"

[models.plain]
server = "chat"
model = "plain"

# Too small to hold any conversation with the default reply reserve.
[models.tiny]
server = "chat"
model = "plain"
num_ctx = 1000
`))
	if err != nil {
		t.Fatal(err)
	}
	return Options{Config: cfg, Repo: t.TempDir(), Version: "test", NoConfirm: true, LogWriter: &bytes.Buffer{}}
}

func newChat() *chat.Server {
	return chat.New("",
		chat.Model{ID: "thinker", Efforts: thinkingEfforts},
		chat.Model{ID: "plain", Caches: true},
	)
}

// say sends one message and waits for the turn to end.
func say(t *testing.T, s *Session, text string) {
	t.Helper()
	if err := s.Send(text); err != nil {
		t.Fatal(err)
	}
	(&rig{t: t, s: s}).drain("n")
}

// providerSaw is what each chat completion the provider saw carried.
func providerSaw(srv *chat.Server) []string {
	var out []string
	for _, r := range srv.Requests() {
		effort := "none"
		if r.HasEffort {
			effort = r.Effort
		}
		out = append(out, r.Model+":"+effort)
	}
	return out
}

// replays checks every request in the log against the hash it recorded,
// which covers the effort it asked for.
func replays(t *testing.T, s *Session) []log.LLMReq {
	t.Helper()
	events := s.EventsSoFar()
	var recs []log.LLMReq
	for _, ev := range events {
		if ev.Kind != log.KindLLMReq {
			continue
		}
		if _, err := window.ReplayRequest(events, ev.Seq); err != nil {
			t.Fatalf("seq %d does not replay: %v", ev.Seq, err)
		}
		var rec log.LLMReq
		if err := ev.Decode(&rec); err != nil {
			t.Fatal(err)
		}
		recs = append(recs, rec)
	}
	return recs
}

// A conversation opened without a choice asks the default model's default
// effort, which the state names with the efforts the model takes.
func TestConversationAsksTheDefaultEffort(t *testing.T) {
	t.Parallel()
	srv := newChat()
	defer srv.Close()
	s, err := New(effortOptions(t, srv))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	got := s.Model()
	if got.Effort != "medium" || !reflect.DeepEqual(got.Efforts, thinkingEfforts) {
		t.Fatalf("state: %+v", got)
	}
	say(t, s, "hello")
	if want := []string{"thinker:medium"}; !reflect.DeepEqual(providerSaw(srv), want) {
		t.Fatalf("the provider saw %v, want %v", providerSaw(srv), want)
	}
	if recs := replays(t, s); len(recs) != 1 || recs[0].Effort != "medium" {
		t.Fatalf("llm.req records: %+v", recs)
	}
}

// An effort chosen as the conversation opens is the first request's, and the
// choice is in the log, so reopening it asks the same.
func TestEffortChosenAtOpenReachesTheProvider(t *testing.T) {
	t.Parallel()
	srv := newChat()
	defer srv.Close()
	o := effortOptions(t, srv)
	o.Model = ModelChoice{Name: "thinker", Effort: "high"}
	s, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	say(t, s, "hello")
	if want := []string{"thinker:high"}; !reflect.DeepEqual(providerSaw(srv), want) {
		t.Fatalf("the provider saw %v, want %v", providerSaw(srv), want)
	}
	var rec log.SessionModel
	for _, ev := range s.EventsSoFar() {
		if ev.Kind == log.KindSessionModel {
			if err := ev.Decode(&rec); err != nil {
				t.Fatal(err)
			}
		}
	}
	if rec.Effort != "high" || rec.Name != "thinker" {
		t.Fatalf("session.model at open: %+v", rec)
	}
}

// Changing the effort between turns asks it from the next request, leaves the
// model, the window and the key alone, and every request still replays.
func TestChangingTheEffortBetweenTurns(t *testing.T) {
	t.Parallel()
	srv := newChat()
	defer srv.Close()
	s, err := New(effortOptions(t, srv))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	say(t, s, "first")
	state, err := s.SetModel(ModelChoice{Name: "control", Effort: "high", Actor: "tester"})
	if err != nil {
		t.Fatal(err)
	}
	if state.Effort != "high" || state.Model != "thinker" || state.Queued != nil {
		t.Fatalf("after the change: %+v", state)
	}
	say(t, s, "second")
	// An empty effort is the model's default again, which for [control] is
	// medium.
	if state, err = s.SetModel(ModelChoice{Name: "control"}); err != nil || state.Effort != "medium" {
		t.Fatalf("back to the default: %+v, %v", state, err)
	}
	say(t, s, "third")
	if want := []string{"thinker:medium", "thinker:high", "thinker:medium"}; !reflect.DeepEqual(providerSaw(srv), want) {
		t.Fatalf("the provider saw %v, want %v", providerSaw(srv), want)
	}
	recs := replays(t, s)
	if len(recs) != 3 || recs[1].Effort != "high" {
		t.Fatalf("llm.req records: %+v", recs)
	}
	// An effort-only change sizes nothing again: no note about the window.
	for _, ev := range s.EventsSoFar() {
		if ev.Kind != log.KindNote {
			continue
		}
		var n log.Note
		if err := ev.Decode(&n); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(n.Text, "window is sized") {
			t.Errorf("a change of effort sized the window: %q", n.Text)
		}
	}
}

// A model that declares no efforts is sent none, and is refused one: the
// provider would refuse the request, and the person would be answered with
// an error instead of by the effort they asked for.
func TestAModelWithoutEffortsTakesNone(t *testing.T) {
	t.Parallel()
	srv := newChat()
	defer srv.Close()
	s, err := New(effortOptions(t, srv))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	state, err := s.SetModel(ModelChoice{Name: "plain"})
	if err != nil {
		t.Fatal(err)
	}
	if state.Effort != "" || len(state.Efforts) != 0 {
		t.Fatalf("a model with no efforts: %+v", state)
	}
	say(t, s, "hello")
	if want := []string{"plain:none"}; !reflect.DeepEqual(providerSaw(srv), want) {
		t.Fatalf("the provider saw %v, want %v", providerSaw(srv), want)
	}
	_, err = s.SetModel(ModelChoice{Name: "plain", Effort: "high"})
	if err == nil || !strings.Contains(err.Error(), "takes no effort setting") {
		t.Fatalf("an effort for a model that takes none: %v", err)
	}
	_, err = s.SetModel(ModelChoice{Name: "thinker", Effort: "extreme"})
	if err == nil || !strings.Contains(err.Error(), "low, medium, high") {
		t.Fatalf("an effort the model does not take: %v", err)
	}
	if got := s.Model(); got.Model != "plain" {
		t.Fatalf("a refused choice changed the model: %+v", got)
	}
	// A conversation opened that way is refused before it exists.
	o := effortOptions(t, srv)
	o.Model = ModelChoice{Name: "plain", Effort: "low"}
	if _, err := New(o); err == nil || !strings.Contains(err.Error(), "takes no effort setting") {
		t.Fatalf("opened with an effort its model does not take: %v", err)
	}
}

// Moving to another model asks that model's default effort, not the last
// one's: what a model takes is its own.
func TestAnotherModelAsksItsOwnDefaultEffort(t *testing.T) {
	t.Parallel()
	srv := newChat()
	defer srv.Close()
	s, err := New(effortOptions(t, srv))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	state, err := s.SetModel(ModelChoice{Name: "thinker"})
	if err != nil || state.Effort != "low" {
		t.Fatalf("the entry's own default: %+v, %v", state, err)
	}
	say(t, s, "hello")
	if want := []string{"thinker:low"}; !reflect.DeepEqual(providerSaw(srv), want) {
		t.Fatalf("the provider saw %v, want %v", providerSaw(srv), want)
	}
}

// A choice made while a response is running does not touch it: the request
// under way keeps the effort it asked with, the state says what is queued,
// and the next turn asks the new one.
func TestEffortChosenWhileAResponseRunsAppliesAfterIt(t *testing.T) {
	t.Parallel()
	srv := newChat()
	defer srv.Close()
	srv.Hold()
	s, err := New(effortOptions(t, srv))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.Send("first"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the request to be held", func() bool { return srv.Waiting() == 1 })
	state, err := s.SetModel(ModelChoice{Name: "control", Effort: "high"})
	if err != nil {
		t.Fatal(err)
	}
	if state.Effort != "medium" || state.Queued == nil || state.Queued.Effort != "high" || state.Queued.Model != "thinker" {
		t.Fatalf("the running response keeps its effort and the choice waits: %+v", state)
	}
	// A later choice replaces the one that has not applied.
	if state, err = s.SetModel(ModelChoice{Name: "control", Effort: "low"}); err != nil || state.Queued == nil || state.Queued.Effort != "low" {
		t.Fatalf("a second choice: %+v, %v", state, err)
	}
	srv.Release()
	(&rig{t: t, s: s}).drain("n")
	if got := s.Model(); got.Effort != "low" || got.Queued != nil || got.Notice != "" {
		t.Fatalf("after the response: %+v", got)
	}
	say(t, s, "second")
	if want := []string{"thinker:medium", "thinker:low"}; !reflect.DeepEqual(providerSaw(srv), want) {
		t.Fatalf("the provider saw %v, want %v", providerSaw(srv), want)
	}
	replays(t, s)
}

// A choice that cannot apply when the response ends (the conversation no
// longer fits the target) leaves the conversation on the model it had, and
// says so in the state and the log: it is not answered by another model.
func TestQueuedChoiceThatCannotApplyIsSaid(t *testing.T) {
	t.Parallel()
	srv := newChat()
	defer srv.Close()
	srv.Hold()
	s, err := New(effortOptions(t, srv))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.Send("first"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the request to be held", func() bool { return srv.Waiting() == 1 })
	if _, err := s.SetModel(ModelChoice{Name: "tiny"}); err != nil {
		t.Fatal(err)
	}
	srv.Release()
	(&rig{t: t, s: s}).drain("n")
	got := s.Model()
	if got.Model != "thinker" || got.Queued != nil || !strings.Contains(got.Notice, "compact it first") {
		t.Fatalf("a choice that did not fit: %+v", got)
	}
	var said bool
	var last log.SessionModel
	for _, ev := range s.EventsSoFar() {
		switch ev.Kind {
		case log.KindNote:
			var n log.Note
			if err := ev.Decode(&n); err != nil {
				t.Fatal(err)
			}
			said = said || strings.Contains(n.Text, "could not be applied")
		case log.KindSessionModel:
			var rec log.SessionModel // a fresh one: decoding does not clear what a record leaves out
			if err := ev.Decode(&rec); err != nil {
				t.Fatal(err)
			}
			last = rec
		}
	}
	if !said {
		t.Error("the log does not say the choice could not be applied")
	}
	// The log's last choice is what the next turn asks, so a resume does not
	// try the choice that could not apply again.
	if last.Queued || last.Model != "thinker" || last.Context == nil {
		t.Fatalf("the last session.model record: %+v", last)
	}
	say(t, s, "second")
	if want := []string{"thinker:medium", "thinker:medium"}; !reflect.DeepEqual(providerSaw(srv), want) {
		t.Fatalf("the provider saw %v, want %v", providerSaw(srv), want)
	}
}

// A conversation keeps its effort when it is reopened, and one closed before a
// queued choice applied has the choice, since it is in the log.
func TestResumeKeepsTheEffortAndAChoiceStillQueued(t *testing.T) {
	t.Parallel()
	srv := newChat()
	defer srv.Close()
	o := effortOptions(t, srv)
	o.LogWriter = nil
	o.Paths = config.PathsUnder(t.TempDir(), t.TempDir())
	s, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetModel(ModelChoice{Name: "control", Effort: "high"}); err != nil {
		t.Fatal(err)
	}
	id := s.ID()
	s.Close()
	resumed, err := Resume(o, id)
	if err != nil {
		t.Fatal(err)
	}
	if got := resumed.Model(); got.Effort != "high" || got.Unavailable != "" {
		t.Fatalf("resumed: %+v", got)
	}
	say(t, resumed, "hello")
	if want := []string{"thinker:high"}; !reflect.DeepEqual(providerSaw(srv), want) {
		t.Fatalf("the provider saw %v, want %v", providerSaw(srv), want)
	}
	// Close with a choice still queued behind a response that is held.
	srv.Hold()
	if err := resumed.Send("again"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the request to be held", func() bool { return srv.Waiting() == 1 })
	if _, err := resumed.SetModel(ModelChoice{Name: "control", Effort: "low"}); err != nil {
		t.Fatal(err)
	}
	resumed.Close()
	srv.Release()
	reopened, err := Resume(o, id)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if got := reopened.Model(); got.Effort != "low" || got.Queued != nil {
		t.Fatalf("a choice made while a response ran, reopened: %+v", got)
	}
}

// An effort config.toml no longer offers is not replaced by another one, or by
// none: the conversation says so and waits to be given one.
func TestResumeWithAnEffortNoLongerOfferedWaitsForAChoice(t *testing.T) {
	t.Parallel()
	srv := newChat()
	defer srv.Close()
	o := effortOptions(t, srv)
	o.LogWriter = nil
	o.Paths = config.PathsUnder(t.TempDir(), t.TempDir())
	s, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetModel(ModelChoice{Name: "thinker", Effort: "high"}); err != nil {
		t.Fatal(err)
	}
	id := s.ID()
	s.Close()

	changed := o
	m := changed.Config.Models["thinker"]
	m.Efforts, m.Effort = []string{"low", "medium"}, "low"
	changed.Config.Models = map[string]config.Model{"thinker": m, "plain": o.Config.Models["plain"]}
	resumed, err := Resume(changed, id)
	if err != nil {
		t.Fatal(err)
	}
	defer resumed.Close()
	if got := resumed.Model(); !strings.Contains(got.Unavailable, "does not take the effort") || !strings.Contains(got.Unavailable, "high") {
		t.Fatalf("resumed: %+v", got)
	}
	if err := resumed.Send("hello"); err == nil {
		t.Fatal("a turn was asked with an effort the model no longer takes")
	}
	if len(srv.Requests()) != 0 {
		t.Fatalf("the provider was asked %v", providerSaw(srv))
	}
	if _, err := resumed.SetModel(ModelChoice{Name: "thinker", Effort: "medium"}); err != nil {
		t.Fatal(err)
	}
	say(t, resumed, "hello")
	if want := []string{"thinker:medium"}; !reflect.DeepEqual(providerSaw(srv), want) {
		t.Fatalf("the provider saw %v, want %v", providerSaw(srv), want)
	}
}

// Ollama is sent the effort as think: the booleans for on and off.
func TestOllamaModelIsSentItsEffortAsThink(t *testing.T) {
	t.Parallel()
	srv := fake.NewT(t, fake.Turn{Content: "one"}, fake.Turn{Content: "two"})
	defer srv.Close()
	cfg, err := config.Parse([]byte(`
[servers.local]
url = "` + srv.URL + `"

[control]
server = "local"
model = "qwen3"
efforts = ["off", "on"]
effort = "off"
`))
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(Options{Config: cfg, Repo: t.TempDir(), Version: "test", NoConfirm: true, LogWriter: &bytes.Buffer{}})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	say(t, s, "one")
	if _, err := s.SetModel(ModelChoice{Name: "control", Effort: "on"}); err != nil {
		t.Fatal(err)
	}
	say(t, s, "two")
	var think []string
	for _, r := range srv.Requests {
		var body map[string]json.RawMessage
		if err := json.Unmarshal(r.Raw, &body); err != nil {
			t.Fatal(err)
		}
		think = append(think, string(body["think"]))
	}
	if want := []string{"false", "true"}; !reflect.DeepEqual(think, want) {
		t.Fatalf("think was %v, want %v", think, want)
	}
}

func waitFor(t *testing.T, what string, ready func() bool) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); !ready(); time.Sleep(2 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
	}
}

// A Claude model is billed for its whole prompt on every request unless the
// request asks the provider to cache it (ADR 9, 2026-10-05): with
// cache_control set, the second request of a conversation reads what the first
// wrote and the log says so; without it the same conversation is never read
// from the cache, however stable its prefix is. The fake caches as a Claude
// route does, only when asked.
func TestPromptCachingNeedsTheModelToAskForIt(t *testing.T) {
	t.Parallel()
	srv := newChat()
	defer srv.Close()
	cfg, err := config.Parse([]byte(`
[servers.chat]
provider = "openai"
url = "` + srv.V1() + `"

[control]
server = "chat"
model = "plain"
cache_control = "ephemeral"

[models.bare]
server = "chat"
model = "thinker"
`))
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(Options{Config: cfg, Repo: t.TempDir(), Version: "test", NoConfirm: true, LogWriter: &bytes.Buffer{}})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	say(t, s, "one")
	say(t, s, "two")
	if _, err := s.SetModel(ModelChoice{Name: "bare"}); err != nil {
		t.Fatal(err)
	}
	say(t, s, "three")
	say(t, s, "four")
	got := srv.Requests()
	if len(got) != 4 {
		t.Fatalf("the provider saw %d requests", len(got))
	}
	for i, want := range []string{`{"type":"ephemeral"}`, `{"type":"ephemeral"}`, "", ""} {
		if got[i].CacheControl != want {
			t.Errorf("request %d sent cache_control %q, want %q", i, got[i].CacheControl, want)
		}
	}
	if got[0].CachedTokens != 0 || got[0].CacheWriteTokens != got[0].PromptTokens {
		t.Errorf("the first request writes its prompt: %+v", got[0])
	}
	if got[1].CachedTokens < got[1].PromptTokens*8/10 {
		t.Errorf("the second read %d of %d prompt tokens from the cache", got[1].CachedTokens, got[1].PromptTokens)
	}
	if got[2].CachedTokens != 0 || got[3].CachedTokens != 0 {
		t.Errorf("a model that did not ask was read from the cache: %+v %+v", got[2], got[3])
	}
	// The log says what the provider reported, so the console's cached share
	// and the budget count it.
	var cached []int
	for _, ev := range s.EventsSoFar() {
		if ev.Kind != log.KindLLMResp {
			continue
		}
		var rec log.LLMResp
		if err := ev.Decode(&rec); err != nil {
			t.Fatal(err)
		}
		if rec.Usage.CachedTokens == nil {
			t.Fatalf("a reply the provider reported the cache for says nothing of it: %+v", rec.Usage)
		}
		cached = append(cached, *rec.Usage.CachedTokens)
	}
	if len(cached) != 4 || cached[1] != got[1].CachedTokens || cached[1] == 0 || cached[3] != 0 {
		t.Errorf("the log reads %v from the cache, the provider reported %d then %d", cached, got[1].CachedTokens, got[3].CachedTokens)
	}
	replays(t, s)
}

// A model that takes an effort beside tools only on OpenAI's Responses API
// (gpt-6.1-sol in session 20261005T033050Z-a7ed2b66) ends the turn on chat
// completions with advice naming that API, not another effort, and answers,
// with the effort chosen, from a server whose dialect is "responses".
func TestAnEffortBesideToolsIsAskedOnTheResponsesAPI(t *testing.T) {
	t.Parallel()
	srv := chat.New("", chat.Model{ID: "sol", Efforts: thinkingEfforts, ToolsNeedResponses: true})
	defer srv.Close()
	for _, dialect := range []string{"", "responses"} {
		cfg, err := config.Parse([]byte(`
[servers.openai]
provider = "openai"
url = "` + srv.V1() + `"
dialect = "` + dialect + `"

[control]
server = "openai"
model = "sol"
effort = "high"

[models.sol]
server = "openai"
model = "sol"
efforts = ["low", "medium", "high"]
`))
		if err != nil {
			t.Fatal(err)
		}
		s, err := New(Options{Config: cfg, Repo: t.TempDir(), Version: "test", NoConfirm: true, LogWriter: &bytes.Buffer{}})
		if err != nil {
			t.Fatal(err)
		}
		say(t, s, "hello")
		var end log.TurnEnd
		for _, ev := range s.EventsSoFar() {
			if ev.Kind == log.KindTurnEnd {
				if err := ev.Decode(&end); err != nil {
					t.Fatal(err)
				}
			}
		}
		replays(t, s)
		s.Close()
		last := srv.Requests()[len(srv.Requests())-1]
		switch dialect {
		case "":
			if last.Path != "/v1/chat/completions" || last.Answered != 400 || end.Outcome != "error" || !strings.Contains(end.Error, "OpenAI Responses") {
				t.Fatalf("chat completions: request %+v, turn end %+v", last, end)
			}
		default:
			if last.Path != "/v1/responses" || last.Answered != 200 || last.Effort != "high" || end.Outcome == "error" {
				t.Fatalf("responses: request %+v, turn end %+v", last, end)
			}
		}
	}
	if n := len(srv.Requests()); n != 2 {
		t.Fatalf("the provider saw %d requests, want one in each protocol", n)
	}
}
