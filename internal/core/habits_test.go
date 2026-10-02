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
