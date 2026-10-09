package core

import (
	"strings"
	"testing"

	"github.com/fclaude/nodwyn/internal/config"
	fake "github.com/fclaude/nodwyn/test/fakes/ollama"
)

// A strategy reads a model's labels and lists the models that carry one.
func TestModelLabelsInScheme(t *testing.T) {
	t.Parallel()
	fast := newFakeOpenAI(t)
	deep := newFakeOpenAI(t)
	r := modelRig(t, "", map[string]config.Model{
		"fast": {Provider: "openai", URL: fast.URL + "/v1", Model: "small", Labels: []string{"fast", "subagent"}},
		"deep": {Provider: "openai", URL: deep.URL + "/v1", Model: "large", Labels: []string{"strong"}},
		"bare": {Provider: "openai", URL: deep.URL + "/v1", Model: "bare"},
	}, Options{})
	for src, want := range map[string]string{
		`(assoc-ref (model-info "fast") 'labels)`: `("fast" "subagent")`,
		`(assoc-ref (model-info "bare") 'labels)`: `()`,
		`(model-names "subagent")`:                `("fast")`,
		`(model-names "strong")`:                  `("deep")`,
		`(model-names "nothing")`:                 `()`,
		`(model-names)`:                           `("bare" "control" "deep" "fast")`,
	} {
		if got := r.eval(src); got != want {
			t.Errorf("%s = %s, want %s", src, got, want)
		}
	}
}

// A label edited while a task is open reaches it at once: the task's model
// sees the new labels in the delegate listing, with no new session, and the
// tools it was offered are the ones it was.
func TestLabelsChangeInAnOpenSession(t *testing.T) {
	t.Parallel()
	helper := newFakeOpenAI(t)
	models := map[string]config.Model{
		"fast": {Provider: "openai", URL: helper.URL + "/v1", Model: "small", Labels: []string{"fast"}},
	}
	list := func(id string) fake.ToolCall {
		return fake.ToolCall{ID: id, Name: "delegate", Args: map[string]any{"model": "", "task": ""}}
	}
	r := modelRig(t, "", models, Options{Delegates: &DelegateChoice{Models: []string{"fast"}}},
		fake.Turn{ToolCalls: []fake.ToolCall{list("l1")}},
		fake.Turn{Content: "one"},
		fake.Turn{Content: "nothing to change"},
		fake.Turn{ToolCalls: []fake.ToolCall{list("l2")}},
		fake.Turn{Content: "two"},
		fake.Turn{Content: "nothing to change"},
	)
	if e := r.turn("first"); e != "" {
		t.Fatal(e)
	}
	if got := string(r.srv.Requests[1].Raw); !strings.Contains(got, "labels fast") || strings.Contains(got, "careful") {
		t.Fatalf("before the edit:\n%s", got)
	}
	toolsBefore := len(r.s.loop.Tools())
	r.s.SetLabels(config.Config{Models: map[string]config.Model{"fast": {Labels: []string{"careful", "slow"}}}})
	if got := r.eval(`(assoc-ref (model-info "fast") 'labels)`); got != `("careful" "slow")` {
		t.Fatalf("model-info after the edit = %s", got)
	}
	if e := r.turn("second"); e != "" {
		t.Fatal(e)
	}
	shown := false
	for _, req := range r.srv.Requests {
		shown = shown || strings.Contains(string(req.Raw), "labels careful, slow")
	}
	if !shown {
		t.Fatal("the listing after the edit does not show the new labels")
	}
	if len(r.s.loop.Tools()) != toolsBefore {
		t.Fatal("editing labels changed the tool list")
	}
}
