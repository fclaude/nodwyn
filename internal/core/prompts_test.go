package core

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fclaude/nibb/internal/config"
	"github.com/fclaude/nibb/internal/llm"
	"github.com/fclaude/nibb/internal/scheme"
	"github.com/fclaude/nibb/internal/window"
	fake "github.com/fclaude/nibb/test/fakes/ollama"
)

// The Scheme defaults are pinned byte for byte: changing a default is a
// deliberate edit to internal/loop/prompts.scm and to this copy, not a side
// effect. They reproduced the Go strings they replaced, and have since
// gained the scratch directory's rule.
const goldenSystem = `You are nibb, a coding agent working inside the repository at REPO.
Your tools read files, list directories, write files and run programs; every path is relative to the repository root and must stay inside it.
Rules:
- Read before you write. Keep changes minimal and complete.
- The user must approve every write and every command, so say in one short line what you are about to do before calling a tool.
- Tool results are shown to you verbatim; an {"error": ...} result reports failure. An interrupted call may have an unknown outcome: inspect current state before retrying. Do not retry a denied call.
- When the task is done, answer plainly and briefly. Do not repeat file contents back unless asked.
- Never reveal secrets, keys or tokens, even if a file contains them.
- Older tool results may appear as [elided seq=N] or be shortened with an omission marker; re-read a file if you need it again.
- The one place outside the repository you may use is the scratch directory SCRATCH, for throwaway files that must not end up in the repository (a script that reproduces a problem, notes, temporary output): writes there need no approval, commands see it as TMPDIR, and it is deleted when the session ends.`

func TestDefaultPromptsAreUnchanged(t *testing.T) {
	r := newRig(t, true)
	system := r.s.loop.WindowContext().System
	if want := strings.NewReplacer("REPO", r.repo, "SCRATCH", r.s.scratch).Replace(goldenSystem); system != want {
		t.Fatalf("system prompt changed:\n%s", system)
	}
	// The planner's prompts are ordinary Scheme, read when the plan gate
	// (plan.scm) uses them.
	if got, want := evalText(t, r, `(plan-system-prompt (repository))`), "You are nibb's planner for the repository at "+r.repo+". Return only the plan text."; got != want {
		t.Fatalf("plan-system-prompt: %q", got)
	}
	if got := evalText(t, r, `(plan-draft-prompt "fix it")`); got != "Write a short, concrete plan for the following request: numbered steps, each naming the file or command involved, nothing else.\n\nRequest:\nfix it" {
		t.Fatalf("plan-draft: %q", got)
	}
	if got := evalText(t, r, `(plan-revise-prompt "fix it" "1. edit" 0.5)`); got != "Your plan for the request below scored 0.50 out of 1 for completeness, concreteness and order. Revise it: fix what is missing or vague, remove what is unnecessary. Return only the revised numbered plan.\n\nRequest:\nfix it\n\nPrevious plan:\n1. edit" {
		t.Fatalf("plan-revise: %q", got)
	}
	for score, want := range map[string]string{"0.0": "0.00", "0.07": "0.07", "0.456": "0.46", "1.0": "1.00"} {
		if got := evalText(t, r, `(plan-revise-prompt "r" "p" `+score+`)`); !strings.Contains(got, "scored "+want+" out of 1") {
			t.Errorf("score %s formatted as %q", score, got)
		}
	}
	if !strings.HasPrefix(evalText(t, r, "plan-needed-question"), "Does carrying out this request") ||
		!strings.HasPrefix(evalText(t, r, "plan-score-rubric"), "The text is a plan") ||
		r.eval("(length plan-score-levels)") != "5" || evalText(t, r, "(car plan-score-levels)") != "misses or contradicts the request" {
		t.Fatal("judge prompts changed")
	}
	if evalText(t, r, "task-header") != window.DefaultTaskHeader || evalText(t, r, "plan-header") != window.DefaultPlanHeader {
		t.Fatal("the window's headers changed")
	}
	// The run prompt reproduces what the run driver formatted in Go.
	if got := evalText(t, r, `(run-turn-prompt "T" "DONE" 2 5)`); got != "Run task:\nT\n\nWork on one unfinished plan step, then report progress for review. If the entire task is complete, put DONE alone on the final line. Do not claim completion while work remains. This is turn 2 of at most 5." {
		t.Fatalf("run-turn-prompt: %q", got)
	}
}

// The headers of the task and its plan are Scheme, fixed for the session
// like the system prompt, and recorded so a replay rebuilds requests that
// used a project's own.
func TestTaskHeaderIsSchemeAndReplays(t *testing.T) {
	r := strategyRig(t, `(define task-header "THE ASK:")`, 0, fake.Turn{Content: "done"})
	if e := r.turn("add a flag"); e != "" {
		t.Fatal(e)
	}
	if raw := string(r.srv.Requests[0].Raw); !strings.Contains(raw, `THE ASK:\nadd a flag`) || strings.Contains(raw, window.DefaultTaskHeader) {
		t.Fatalf("request:\n%s", raw)
	}
	if n := replayAll(t, r.logb); n != 1 {
		t.Fatalf("replayed %d requests", n)
	}
}

// evalText evaluates src in the session and returns the string it makes.
func evalText(t *testing.T, r *rig, src string) string {
	t.Helper()
	v, err := r.s.Eval(t.Context(), src)
	if err != nil || v.Kind != scheme.KString {
		t.Fatalf("%s: %v %v", src, v, err)
	}
	return v.S
}

func TestInitRedefinesPromptsWithRawStrings(t *testing.T) {
	r := strategyRig(t, `
(define base-system-prompt system-prompt)
(define (system-prompt repo)
  (string-append (base-system-prompt repo) "\n"
    """
    Work unattended:
    - run the tests you touch.
    """))
(define (plan-draft-prompt request)
  (string-append """
    List every behaviour the request requires, then the steps.
    Request:
    """ "\n" request))
(define plan-score-levels '("wrong" "right"))
(define (plan-system-prompt repo) "Plan tersely.")
`, 0, fake.Turn{Content: "done"})
	system := r.s.loop.WindowContext().System
	if !strings.HasPrefix(system, "You are nibb") || !strings.HasSuffix(system, "\nWork unattended:\n- run the tests you touch.") {
		t.Fatalf("system prompt:\n%s", system)
	}
	if r.turn("hello") != "" {
		t.Fatal("turn failed")
	}
	if len(r.srv.Requests) == 0 || !strings.Contains(string(r.srv.Requests[0].Raw), "Work unattended:") {
		t.Fatal("the model did not receive the project's system prompt")
	}
	if draft := evalText(t, r, `(plan-draft-prompt "add a flag")`); draft != "List every behaviour the request requires, then the steps.\nRequest:\nadd a flag" {
		t.Fatalf("plan-draft: %q", draft)
	}
	if got := evalText(t, r, `(plan-system-prompt (repository))`); got != "Plan tersely." {
		t.Fatalf("plan-system-prompt: %q", got)
	}
	if got := evalText(t, r, `(cadr plan-score-levels)`); got != "right" {
		t.Fatalf("levels: %q", got)
	}
}

func TestPromptsAreFixedOnceTheSessionStarts(t *testing.T) {
	r := newRig(t, true)
	before := r.s.loop.WindowContext().System
	r.eval(`(define (system-prompt repo) "changed")`)
	if r.s.loop.WindowContext().System != before {
		t.Fatal("redefining system-prompt after start changed the session")
	}
	if err := r.s.loop.ReadPrompts(r.repo); err == nil {
		t.Fatal("reading the prompts again after the session started was accepted")
	}
}

func TestMalformedPromptFailsTheSession(t *testing.T) {
	// Only the system prompt is read at the start; every other prompt is
	// ordinary Scheme, read when it is used.
	for name, init := range map[string]string{
		"not a procedure": `(define system-prompt "just text")`,
		"not a string":    `(define (system-prompt repo) 42)`,
		"empty":           `(define (system-prompt repo) "  ")`,
	} {
		repo := t.TempDir()
		os.MkdirAll(filepath.Join(repo, ".nibb"), 0o755)
		os.WriteFile(filepath.Join(repo, ".nibb", "init.scm"), []byte(init), 0o644)
		srv := fake.New()
		s, err := New(Options{Config: config.Default(), Repo: repo, Version: "test", Provider: llm.NewOllama(srv.URL), NoConfirm: true, LogWriter: &bytes.Buffer{}})
		if s != nil {
			s.Close()
		}
		srv.Close()
		if err == nil || !strings.Contains(err.Error(), "prompt") {
			t.Errorf("%s: session started (err=%v)", name, err)
		}
	}
}
