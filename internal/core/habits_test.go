package core

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fclaude/nodwyn/internal/config"
	fake "github.com/fclaude/nodwyn/test/fakes/ollama"
)

// A strategy reads the models' and the window's properties (ADR 32).
func TestModelAndWindowPrimitives(t *testing.T) {
	t.Parallel()
	r := strategyRig(t, "", 10)
	for src, want := range map[string]string{
		"(assoc-ref (model-info) 'name)":              `"control"`,
		"(assoc-ref (model-info \"control\") 'name)":  `"control"`,
		"(assoc-ref (model-info) 'max-output)":        "16384",
		"(assoc-ref (window-limits) 'high-water)":     "61440",
		"(assoc-ref (window-limits) 'low-water)":      "30720",
		"(assoc-ref (window-limits) 'output-reserve)": "16384",
		"(car (model-names))":                         `"control"`,
	} {
		if got := r.eval(src); got != want {
			t.Errorf("%s = %s, want %s", src, got, want)
		}
	}
	if _, err := r.s.Eval(context.Background(), `(model-info "nope")`); err == nil || !strings.Contains(err.Error(), `no model "nope"`) {
		t.Fatalf("unknown model: %v", err)
	}
}

// A command line sent through cmd/args is answered, unrun, with the bash -c
// call that runs it (shell.scm).
func TestShellCommandLineIsRedirected(t *testing.T) {
	t.Parallel()
	pipe := fake.ToolCall{ID: "p", Name: "shell-exec", Args: map[string]any{"cmd": "ls", "args": []any{"-1", "|", "head"}}}
	r := strategyRig(t, "", 10, fake.Turn{ToolCalls: []fake.ToolCall{pipe}}, fake.Turn{Content: "done"})
	if err := r.turn("list the files"); err != "" {
		t.Fatal(err)
	}
	raw := string(r.srv.Requests[1].Raw)
	if !strings.Contains(raw, `Not executed: shell-exec runs one program with its arguments`) ||
		!strings.Contains(raw, `call shell-exec with cmd \\\"bash\\\" and args [\\\"-c\\\", \\\"ls -1 | head\\\"]`) {
		t.Fatalf("command line not redirected:\n%s", raw)
	}
}

// read-definition returns a definition by name with its line range
// (readdef.scm).
func TestReadDefinitionByName(t *testing.T) {
	t.Parallel()
	call := fake.ToolCall{ID: "d", Name: "read-definition", Args: map[string]any{"path": "lib.py", "name": "shout"}}
	r := strategyRig(t, "", 10, fake.Turn{ToolCalls: []fake.ToolCall{call}}, fake.Turn{Content: "done"})
	src := "import re\n\n\ndef shout(s):\n    return s.upper()\n\n\ndef whisper(s):\n    return s.lower()\n"
	if err := os.WriteFile(filepath.Join(r.repo, "lib.py"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := r.turn("show me shout"); err != "" {
		t.Fatal(err)
	}
	raw := string(r.srv.Requests[1].Raw)
	if !strings.Contains(raw, `[lines 4-5 of lib.py]\ndef shout(s):\n    return s.upper()`) || strings.Contains(raw, "def whisper") {
		t.Fatalf("definition not read by name:\n%s", raw)
	}
}

// hygieneRun reads each path in a round of its own, then answers, in a
// session where a read two rounds old is due to fold (hygiene.scm), and
// returns the rig.
func hygieneRun(t *testing.T, init string, paths ...string) *rig {
	t.Helper()
	var turns []fake.Turn
	for i, p := range paths {
		turns = append(turns, fake.Turn{ToolCalls: []fake.ToolCall{{ID: fmt.Sprint("r", i), Name: "fs-read", Args: map[string]any{"path": p}}}})
	}
	turns = append(turns, fake.Turn{Content: "done"})
	r := strategyRig(t, "(define hygiene-age 2) (define hygiene-min-bytes 1)\n"+init, 10, turns...)
	for name, size := range map[string]int{"big.py": 400, "bigger.py": 600, "small.py": 2} {
		if err := os.WriteFile(filepath.Join(r.repo, name), []byte(strings.Repeat("value = 12345678\n", size)), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := r.turn("read them"); err != "" {
		t.Fatal(err)
	}
	return r
}

// A read is folded when the provider's cache would gain from it, or loses
// nothing by it: when the fold takes out at least as much as the server
// must read again after it, when the cache is not serving the context, or
// when the context is filling the window. Otherwise it stays (hygiene.scm).
func TestHygieneFoldsWhenItPays(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, init string
		paths      []string
		why        string // "" when nothing folds
	}{
		{"what follows is larger", "", []string{"big.py", "bigger.py", "small.py"}, ""},
		{"the fold is most of what follows", "", []string{"big.py", "small.py", "small.py"}, "tokens the server reads again"},
		{"the cache is not serving", "(set! hygiene-cache '((1000 . 0)))", []string{"big.py", "bigger.py", "small.py"}, "the provider's cache is not serving this context"},
		{"the context fills the window", "(define hygiene-pressure 0.01)", []string{"big.py", "bigger.py", "small.py"}, "the context is filling the window"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := hygieneRun(t, tc.init, tc.paths...)
			logText := r.logb.String()
			folded := strings.Contains(logText, `"kind":"result.fold"`)
			if folded != (tc.why != "") || (tc.why != "" && !strings.Contains(logText, "hygiene: folded 1 reads, 6800 bytes: ")) || !strings.Contains(logText, tc.why) {
				t.Fatalf("folded %v, want the reason %q:\n%s", folded, tc.why, logText)
			}
		})
	}
}

// (context-tokens result) is the window from that result on.
func TestContextTokensFromAResult(t *testing.T) {
	t.Parallel()
	r := hygieneRun(t, "", "big.py", "bigger.py", "small.py")
	whole, first, second := r.eval("(context-tokens)"), r.eval(`(context-tokens "r0")`), r.eval(`(context-tokens "r1")`)
	var w, a, b int
	fmt.Sscan(whole, &w)
	fmt.Sscan(first, &a)
	fmt.Sscan(second, &b)
	if !(w > a && a > b && b > 2000) {
		t.Fatalf("whole %s, from r0 %s, from r1 %s", whole, first, second)
	}
	if _, err := r.s.Eval(context.Background(), `(context-tokens "nope")`); err == nil || !strings.Contains(err.Error(), `no result for call "nope"`) {
		t.Fatalf("unknown call: %v", err)
	}
}

// The provider's cache counts of the main context's requests judge whether
// its cache serves the context (hygiene.scm); a sub-agent's do not.
func TestHygieneWatchesTheProvidersCache(t *testing.T) {
	t.Parallel()
	cached := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cached += 30
		fmt.Fprintf(w, `{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":100,"completion_tokens":1,"prompt_tokens_details":{"cached_tokens":%d}}}`, cached)
	}))
	t.Cleanup(srv.Close)
	r := modelRig(t, `
(define (agent-turn request)
  (model-turn "impl")
  (set! hygiene-depth 1)
  (model-turn "impl")
  (set! hygiene-depth 0)
  (model-turn "impl"))`,
		map[string]config.Model{"impl": {Provider: "openai", URL: srv.URL + "/v1", Model: "m"}}, Options{})
	if err := r.turn("go"); err != "" {
		t.Fatal(err)
	}
	if got := r.eval("hygiene-cache"); got != "((100 . 90) (100 . 30))" {
		t.Fatalf("hygiene-cache = %s", got)
	}
	if got := r.eval("(hygiene-cache-serves?)"); got != "#t" {
		t.Fatalf("serves = %s", got)
	}
	r.eval("(set! hygiene-cache '((100 . 10) (100 . 20)))")
	if got := r.eval("(hygiene-cache-serves?)"); got != "#f" {
		t.Fatalf("serves at 15%% = %s", got)
	}
}

// look is a round that calls one tool that only looks at the repository.
func look(id, name string, args map[string]any) fake.Turn {
	return fake.Turn{ToolCalls: []fake.ToolCall{{ID: id, Name: name, Args: args}}}
}

// After rounds that only read and search, a chat's model is told to write
// down what it found, and to answer now if the request asks or needs the
// person's decision (strategy.scm, reading-reminder-after).
func TestReadingRoundsGetAReminder(t *testing.T) {
	t.Parallel()
	r := strategyRig(t, "(define reading-reminder-after 3)\n", 10,
		look("a", "fs-read", map[string]any{"path": "main.go"}),
		look("b", "fs-list", map[string]any{"path": "."}),
		look("c", "index-grep", map[string]any{"path": ".", "pattern": "package"}),
		fake.Turn{Content: "There is nothing to enable yet."},
	)
	if err := r.turn("the UI does not let me enable worktrees"); err != "" {
		t.Fatal(err)
	}
	for i := 1; i < 3; i++ {
		if got := lastMessage(r, i); strings.Contains(got, "You have only read") {
			t.Fatalf("reminded after %d rounds: %q", i, got)
		}
	}
	if got := lastMessage(r, 3); !strings.HasPrefix(got, "You have only read and searched for the last 3 rounds.") || !strings.Contains(got, "answer now") ||
		!strings.HasSuffix(got, "Otherwise write your plan with plan-write now and start the change, and read more only for a step of the plan.") {
		t.Fatalf("no reminder after 3 rounds: %q", got)
	}
}

// A turn that has a plan is told to go on with it, not to write it again:
// asked for the plan it had, a model wrote it again, which starts its marks
// over (prompts.scm, reading-reminder-plan).
func TestReadingRoundsWithAPlanGoOnWithIt(t *testing.T) {
	t.Parallel()
	r := strategyRig(t, "(define reading-reminder-after 2)\n", 10,
		fake.Turn{ToolCalls: []fake.ToolCall{planWriteCall("w", shapedPlan)}},
		look("a", "fs-read", map[string]any{"path": "main.go"}),
		look("b", "fs-list", map[string]any{"path": "."}),
		fake.Turn{Content: "done"},
	)
	if err := r.turn("change main.go"); err != "" {
		t.Fatal(err)
	}
	got := lastMessage(r, 3)
	if !strings.HasPrefix(got, "You have only read and searched for the last 2 rounds.") ||
		!strings.HasSuffix(got, "Otherwise your plan is in front of you: make the change for its next unmarked step now, and read more only for that step. Write the plan again only if it must change, since that starts its marks over.") ||
		strings.Contains(got, "write your plan with plan-write now") {
		t.Fatalf("after 2 rounds with a plan: %q", got)
	}
}

// A round that runs or changes something starts the count again.
func TestReadingCountStartsAgainOnARun(t *testing.T) {
	t.Parallel()
	r := strategyRig(t, "(define reading-reminder-after 3)\n", 10,
		look("a", "fs-read", map[string]any{"path": "main.go"}),
		look("b", "fs-read", map[string]any{"path": "main.go"}),
		shellArgs("s", "true"),
		look("c", "fs-read", map[string]any{"path": "main.go"}),
		look("d", "fs-read", map[string]any{"path": "main.go"}),
		fake.Turn{Content: "done"},
	)
	if err := r.turn("look around"); err != "" {
		t.Fatal(err)
	}
	for i := range r.srv.Requests {
		if got := lastMessage(r, i); strings.Contains(got, "You have only read") {
			t.Fatalf("request %d reminded: %q", i, got)
		}
	}
}

// The evidence loop counts too, and a turn whose answer nobody reads before
// it ends is told to start the change, with no way out: on the plan it has,
// here the gate's.
func TestUnreviewedReadingRoundsAreToldToPlan(t *testing.T) {
	t.Parallel()
	turns := []fake.Turn{
		{Content: "main.go declares package main."}, // the explorer
		{Content: "1. Edit main.go."},               // the planner
	}
	for i := 0; i < 12; i++ {
		turns = append(turns, look(fmt.Sprint("r", i), "index-grep", map[string]any{"path": ".", "pattern": fmt.Sprint("word", i)}))
	}
	turns = append(turns, fake.Turn{Content: "done"}, fake.Turn{Content: "done"}, fake.Turn{Content: "done"})
	r := newRigWith(t, true, func(c *config.Config) { c.Loop.MaxToolCalls = 20 }, turns...)
	if err := r.s.SendPlanned("change main.go"); err != nil {
		t.Fatal(err)
	}
	r.drain("n")
	if len(r.srv.Requests) != 17 {
		t.Fatalf("%d requests, want 17", len(r.srv.Requests))
	}
	if got := lastMessage(r, 13); strings.Contains(got, "You have only read") {
		t.Fatalf("reminded after 11 rounds: %q", got)
	}
	want := "You have only read and searched for the last 12 rounds. Your thinking comes back to you only as the server reported it, often as a summary or not at all, so what you found is lost unless you act on it, and your plan is in front of you: make the change for its next unmarked step now, and read more only for that step. Write the plan again only if it must change, since that starts its marks over."
	if got := lastMessage(r, 14); got != want {
		t.Fatalf("after 12 rounds: %q", got)
	}
}
