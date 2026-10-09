package core

import (
	"strings"
	"testing"
	"time"

	"github.com/fclaude/nodwyn/internal/config"
	"github.com/fclaude/nodwyn/internal/llm"
	fake "github.com/fclaude/nodwyn/test/fakes/ollama"
)

// The model hands a task to one of the models its task exposed, choosing by
// labels: a call with no task lists them, a model outside the set is refused
// whatever the model asks for, and a task that exposed none is not offered
// the tool.
func TestDelegateToExposedModels(t *testing.T) {
	t.Parallel()
	helper := newFakeOpenAI(t, oaiText("the helper's finding"))
	other := newFakeOpenAI(t)
	models := map[string]config.Model{
		"fast": {Provider: "openai", URL: helper.URL + "/v1", Model: "small-fast", Labels: []string{"fast", "cheap"}},
		"bare": {Provider: "openai", URL: other.URL + "/v1", Model: "bare-model", Labels: []string{"fast"}},
	}
	call := func(id, model, task string) fake.ToolCall {
		return fake.ToolCall{ID: id, Name: "delegate", Args: map[string]any{"model": model, "task": task}}
	}
	r := modelRig(t, "", models, Options{Delegates: &DelegateChoice{Models: []string{"fast"}, Actor: "tester"}},
		fake.Turn{ToolCalls: []fake.ToolCall{call("d0", "", "")}},
		fake.Turn{ToolCalls: []fake.ToolCall{call("d1", "bare", "find the answer")}},
		fake.Turn{ToolCalls: []fake.ToolCall{call("d2", "fast", "find the answer")}},
		fake.Turn{Content: "done"},
		fake.Turn{Content: "nothing to change"},
	)
	if e := r.turn("research this"); e != "" {
		t.Fatal(e)
	}
	listing := string(r.srv.Requests[1].Raw)
	if !strings.Contains(listing, "- fast: small-fast") || !strings.Contains(listing, "labels fast, cheap") || strings.Contains(listing, "- bare:") {
		t.Fatalf("the listing is not the exposed model alone:\n%s", listing)
	}
	refused := string(r.srv.Requests[2].Raw)
	if !strings.Contains(refused, "No model bare is available to delegate to") || len(other.seen()) != 0 {
		t.Fatalf("a model outside the set was not refused (it saw %d requests):\n%s", len(other.seen()), refused)
	}
	if !strings.Contains(string(r.srv.Requests[3].Raw), "the helper's finding") {
		t.Fatal("the helper's answer did not come back")
	}
	seen := helper.seen()
	if len(seen) != 1 || !strings.Contains(seen[0].body, "find the answer") || strings.Contains(seen[0].body, "research this") {
		t.Fatalf("the helper saw %d requests, or the conversation: %+v", len(seen), seen)
	}
	if strings.Contains(seen[0].body, `"name":"delegate"`) || !strings.Contains(seen[0].body, `"name":"fs-read"`) {
		t.Fatalf("the helper is not offered the other tools alone:\n%s", seen[0].body)
	}
	if n, want := replayAll(t, r.logb), len(r.srv.Requests)+1; n != want {
		t.Fatalf("replayed %d of %d requests", n, want)
	}

	// Resumed, the task offers the same models; opened with none, it has no tool.
	if got := r.s.Delegates(); len(got) != 1 || got[0] != "fast" {
		t.Fatalf("delegates = %v", got)
	}
	none := modelRig(t, "", models, Options{}, fake.Turn{Content: "done"}, fake.Turn{Content: "nothing to change"})
	if e := none.turn("hello"); e != "" {
		t.Fatal(e)
	}
	if strings.Contains(string(none.srv.Requests[0].Raw), `"delegate"`) {
		t.Fatal("delegate is offered to a task that exposed no model")
	}
	for _, bad := range []string{"nope", "control"} {
		o := diskOptions(t)
		o.Config.Models = models
		o.Delegates = &DelegateChoice{Models: []string{bad}}
		if _, err := resolveOptions(o); err == nil || !strings.Contains(err.Error(), `no model "`+bad+`"`) {
			t.Fatalf("delegate %q: %v", bad, err)
		}
	}
}

// A helper whose provider fails answers the delegate call with the failure:
// the task's model goes on in its own context, in the same turn, instead of
// the turn ending on another provider's error.
func TestDelegateFailureIsTheAnswer(t *testing.T) {
	// llm.RetryDelay is the whole process's, so this test is not parallel.
	defer func(d time.Duration) { llm.RetryDelay = d }(llm.RetryDelay)
	llm.RetryDelay = time.Millisecond
	helper := newFakeOpenAI(t) // no replies: every request fails
	models := map[string]config.Model{
		"fast": {Provider: "openai", URL: helper.URL + "/v1", Model: "small-fast"},
	}
	r := modelRig(t, "", models, Options{Delegates: &DelegateChoice{Models: []string{"fast"}}},
		fake.Turn{ToolCalls: []fake.ToolCall{{ID: "d1", Name: "delegate", Args: map[string]any{"model": "fast", "task": "find it"}}}},
		fake.Turn{Content: "done without the helper"},
		fake.Turn{Content: "nothing to change"},
	)
	if e := r.turn("research this"); e != "" {
		t.Fatalf("the helper's failure ended the turn: %s", e)
	}
	if len(helper.seen()) == 0 {
		t.Fatal("the helper was never asked")
	}
	next := string(r.srv.Requests[1].Raw)
	if !strings.Contains(next, "The helper on fast failed") || !strings.Contains(next, "research this") || strings.Contains(next, "You are a helper another model") {
		t.Fatalf("the task's model did not read the failure in its own context:\n%s", next)
	}
}

// A task keeps the models it exposed when it is resumed, those config.toml
// still has: the log says which, and the tool is offered again.
func TestResumeKeepsTheDelegates(t *testing.T) {
	t.Parallel()
	home := fake.NewT(t, fake.Turn{Content: "first"})
	defer home.Close()
	helper := newFakeOpenAI(t)
	o := diskOptions(t)
	o.Config.Control.URL, o.Config.Control.Model = home.URL, "control-model"
	o.Config.Models = map[string]config.Model{
		"fast": {Provider: "openai", URL: helper.URL + "/v1", Model: "f"},
		"gone": {Provider: "openai", URL: helper.URL + "/v1", Model: "g"},
	}
	o.Delegates = &DelegateChoice{Models: []string{"fast", "gone"}, Actor: "tester"}
	s, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	id := s.ID()
	s.Close()

	o.Delegates = nil
	delete(o.Config.Models, "gone")
	resumed, err := Resume(o, id)
	if err != nil {
		t.Fatal(err)
	}
	defer resumed.Close()
	if got := resumed.Delegates(); len(got) != 1 || got[0] != "fast" {
		t.Fatalf("resumed delegates = %v, want [fast]", got)
	}
	if _, ok := resumed.interp.Lookup("delegate"); !ok {
		t.Fatal("the delegate tool is not registered after resume")
	}
}
