package core

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fclaude/nibb/internal/config"
	"github.com/fclaude/nibb/internal/llm"
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
	ps := r.s.PlanPrompts()
	if want := "You are nibb's planner for the repository at " + r.repo + ". Return only the plan text."; ps.System != want {
		t.Fatalf("plan-system-prompt: %q", ps.System)
	}
	draft, err := ps.Draft("fix it")
	if err != nil || draft != "Write a short, concrete plan for the following request: numbered steps, each naming the file or command involved, nothing else.\n\nRequest:\nfix it" {
		t.Fatalf("plan-draft: %q %v", draft, err)
	}
	revise, err := ps.Revise("fix it", "1. edit", 0.5)
	if err != nil || revise != "Your plan for the request below scored 0.50 out of 1 for completeness, concreteness and order. Revise it: fix what is missing or vague, remove what is unnecessary. Return only the revised numbered plan.\n\nRequest:\nfix it\n\nPrevious plan:\n1. edit" {
		t.Fatalf("plan-revise: %q %v", revise, err)
	}
	for score, want := range map[float64]string{0.0: "0.00", 0.07: "0.07", 0.456: "0.46", 1.0: "1.00"} {
		got, _ := ps.Revise("r", "p", score)
		if !strings.Contains(got, "scored "+want+" out of 1") {
			t.Errorf("score %v formatted as %q", score, got)
		}
	}
	if !strings.HasPrefix(ps.Needed, "Does carrying out this request") || !strings.HasPrefix(ps.ScoreRubric, "The text is a plan") ||
		len(ps.ScoreLevels) != 5 || ps.ScoreLevels[0] != "misses or contradicts the request" {
		t.Fatalf("judge prompts: %+v", ps)
	}
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
	ps := r.s.PlanPrompts()
	if draft, _ := ps.Draft("add a flag"); draft != "List every behaviour the request requires, then the steps.\nRequest:\nadd a flag" {
		t.Fatalf("plan-draft: %q", draft)
	}
	if ps.System != "Plan tersely." {
		t.Fatalf("plan-system-prompt: %q", ps.System)
	}
	if len(ps.ScoreLevels) != 2 || ps.ScoreLevels[1] != "right" {
		t.Fatalf("levels: %v", ps.ScoreLevels)
	}
}

func TestPromptsAreFixedOnceTheSessionStarts(t *testing.T) {
	r := newRig(t, true)
	before := r.s.loop.WindowContext().System
	r.eval(`(define (system-prompt repo) "changed")`)
	if r.s.loop.WindowContext().System != before {
		t.Fatal("redefining system-prompt after start changed the session")
	}
	if _, err := r.s.loop.ReadPrompts(r.repo); err == nil {
		t.Fatal("reading the prompts again after the session started was accepted")
	}
}

func TestMalformedPromptFailsTheSession(t *testing.T) {
	for name, init := range map[string]string{
		"not a procedure": `(define system-prompt "just text")`,
		"not a string":    `(define (plan-draft-prompt request) 42)`,
		"empty":           `(define (system-prompt repo) "  ")`,
		"levels":          `(define plan-score-levels '("only one"))`,
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
		if name == "not a string" {
			// Procedures are called when a plan is drafted, not at start.
			continue
		}
		if err == nil || !strings.Contains(err.Error(), "prompt") {
			t.Errorf("%s: session started (err=%v)", name, err)
		}
	}
}
