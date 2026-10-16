package core

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fclaude/nodwyn/internal/config"
	"github.com/fclaude/nodwyn/internal/llm"
	"github.com/fclaude/nodwyn/internal/scheme"
	"github.com/fclaude/nodwyn/internal/window"
	fake "github.com/fclaude/nodwyn/test/fakes/ollama"
)

// The default prompts are checked for what matters about them, not for
// their wording: the session sends what (system-prompt repo) builds, the
// sections that do not apply are left out, and the planner's and runs'
// prompts carry what they are given. A copy pinned byte for byte showed
// the move to Scheme (2026-09-26) reproduced the Go strings; after that it
// only made every edit to prompts.scm twice.
func TestDefaultPrompts(t *testing.T) {
	t.Parallel()
	r := newRig(t, true)
	system := r.s.loop.WindowContext().System
	if built := evalText(t, r, `(system-prompt (repository))`); system != built {
		t.Fatalf("the session's system prompt is not what (system-prompt repo) builds:\n%s\n---\n%s", system, built)
	}
	if !strings.HasPrefix(system, "You are nodwyn, a coding agent working inside the repository at "+r.repo+".\n") {
		t.Fatalf("system prompt does not name the repository:\n%s", system)
	}
	if rules := evalText(t, r, "working-rules"); !strings.Contains(system, rules) || !strings.Contains(system, "$TMPDIR") {
		t.Fatalf("system prompt lacks the working rules or the scratch rule:\n%s", system)
	}
	// A plain directory: no git work tree, no mounted folders, no notes, no
	// AGENTS.md.
	for _, absent := range []string{"git-status and git-diff", "Folders outside it", "Notes kept from earlier sessions", "AGENTS.md"} {
		if strings.Contains(system, absent) {
			t.Errorf("system prompt has %q, which does not apply here", absent)
		}
	}
	// The planner's prompts are ordinary Scheme, read when the plan gate
	// (plan.scm) uses them.
	if got := evalText(t, r, `(plan-system-prompt (repository))`); !strings.Contains(got, r.repo) {
		t.Fatalf("plan-system-prompt: %q", got)
	}
	if got := evalText(t, r, `(plan-draft-prompt "fix it")`); !strings.HasSuffix(got, "Request:\nfix it") {
		t.Fatalf("plan-draft: %q", got)
	}
	if got := evalText(t, r, `(plan-revise-prompt "fix it" "1. edit" 0.5)`); !strings.Contains(got, "scored 0.50 out of 1") ||
		!strings.Contains(got, "Request:\nfix it") || !strings.HasSuffix(got, "Previous plan:\n1. edit") {
		t.Fatalf("plan-revise: %q", got)
	}
	for score, want := range map[string]string{"0.0": "0.00", "0.07": "0.07", "0.456": "0.46", "1.0": "1.00"} {
		if got := evalText(t, r, `(plan-revise-prompt "r" "p" `+score+`)`); !strings.Contains(got, "scored "+want+" out of 1") {
			t.Errorf("score %s formatted as %q", score, got)
		}
	}
	if evalText(t, r, "plan-needed-question") == "" || evalText(t, r, "plan-score-rubric") == "" || r.eval("(length plan-score-levels)") != "5" {
		t.Fatal("judge prompts missing")
	}
	// Recipes that recorded no header replay with the window's default, so
	// the default task header must stay the window's; the plan header asks
	// for the plan-step marks.
	if evalText(t, r, "task-header") != window.DefaultTaskHeader || !strings.Contains(evalText(t, r, "plan-header"), "plan-step") {
		t.Fatal("the window's headers changed")
	}
	if got := evalText(t, r, `(run-turn-prompt "T" "DONE" 2 5)`); !strings.HasPrefix(got, "Run task:\nT\n\n") ||
		!strings.Contains(got, "put DONE alone on the final line") || !strings.HasSuffix(got, "This is turn 2 of at most 5.") {
		t.Fatalf("run-turn-prompt: %q", got)
	}
}

// Both system prompts say where the model's scratch is and what that means
// for its commands (they run with TMPDIR set to it, outside nodwyn's
// protected directories, so tests that make temporary files work and no
// HOME or TMPDIR of its own is needed), and say to read a long file by the
// part needed, found with index-grep, not whole; the explorer is asked to
// cite places as path:line, which plan-excerpts reads (ADR 32, amended
// 2026-10-16).
func TestPromptsSayWhereScratchIsAndHowToReadLongFiles(t *testing.T) {
	t.Parallel()
	for name, r := range map[string]*rig{"interactive": newRig(t, true), "unattended": unattendedRig(t)} {
		system := r.s.loop.WindowContext().System
		for _, want := range []string{
			"Your commands run with TMPDIR set to it",
			"do not set TMPDIR or HOME to another place",
			"do not use /tmp",
			"fs-read with start and count",
			"index-grep for a heading, a definition or a name finds its line",
			"not whole",
		} {
			if !strings.Contains(system, want) {
				t.Errorf("the %s system prompt lacks %q", name, want)
			}
		}
	}
	r := newRig(t, true)
	if task := evalText(t, r, `(plan-explore-task "fix it")`); !strings.Contains(task, "each place cited as path:line") {
		t.Errorf("the explorer is not asked to cite lines:\n%s", task)
	}
}

// The system prompt is the start of every request, the prefix a provider
// caches (ADR 9): two sessions of one repository send the same one, so it
// names $TMPDIR, never a session's own scratch directory.
func TestSystemPromptIsTheSameAcrossSessions(t *testing.T) {
	t.Parallel()
	r := newRig(t, true)
	cfg := config.Default()
	cfg.Control.URL = r.srv.URL
	cfg.Control.Model = "test-model"
	other, err := New(Options{Config: cfg, Repo: r.repo, Version: "test", Provider: llm.NewOllama(r.srv.URL), NoConfirm: true, LogWriter: &bytes.Buffer{}})
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	first, second := r.s.loop.WindowContext().System, other.loop.WindowContext().System
	if first != second {
		t.Fatalf("two sessions of one repository have different system prompts:\n%s\n---\n%s", first, second)
	}
	if strings.Contains(first, r.s.scratch) {
		t.Fatalf("system prompt names the session's scratch directory %s", r.s.scratch)
	}
}

// A repository's AGENTS.md joins both system prompts, interactive and
// unattended, after the working rules (ADR 36).
func TestAgentsFileJoinsTheSystemPrompt(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	os.WriteFile(filepath.Join(repo, "AGENTS.md"), []byte("# Working here\n\n- Validate with make accept.\n"), 0o644)
	srv := fake.NewT(t)
	defer srv.Close()
	for _, unattended := range []bool{false, true} {
		cfg := config.Default()
		cfg.Control.URL = srv.URL
		s, err := New(Options{Config: cfg, Repo: repo, Version: "test", Provider: llm.NewOllama(srv.URL), NoConfirm: true, AutoApprove: unattended, LogWriter: &bytes.Buffer{}})
		if err != nil {
			t.Fatal(err)
		}
		system := s.loop.WindowContext().System
		s.Close()
		rules := strings.Index(system, "find regressions by applying changes one file at a time")
		section := strings.Index(system, "\n\nInstructions from the repository's AGENTS.md")
		if rules < 0 || section < rules || !strings.HasSuffix(system, "\n# Working here\n\n- Validate with make accept.") ||
			strings.Contains(system, "You work unattended") != unattended {
			t.Fatalf("unattended=%v: system prompt:\n%s", unattended, system)
		}
	}
}

// The headers of the task and its plan are Scheme, fixed for the session
// like the system prompt, and recorded so a replay rebuilds requests that
// used a project's own.
func TestTaskHeaderIsSchemeAndReplays(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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
	if !strings.HasPrefix(system, "You are nodwyn") || !strings.HasSuffix(system, "\nWork unattended:\n- run the tests you touch.") {
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
	t.Parallel()
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
	t.Parallel()
	// Only the system prompt is read at the start; every other prompt is
	// ordinary Scheme, read when it is used.
	for name, init := range map[string]string{
		"not a procedure": `(define system-prompt "just text")`,
		"not a string":    `(define (system-prompt repo) 42)`,
		"empty":           `(define (system-prompt repo) "  ")`,
	} {
		repo := t.TempDir()
		os.MkdirAll(filepath.Join(repo, ".nodwyn"), 0o755)
		os.WriteFile(filepath.Join(repo, ".nodwyn", "init.scm"), []byte(init), 0o644)
		srv := fake.NewT(t)
		s, err := newInitSession(Options{Config: config.Default(), Repo: repo, Version: "test", Provider: llm.NewOllama(srv.URL), NoConfirm: true, LogWriter: &bytes.Buffer{}})
		if s != nil {
			s.Close()
		}
		srv.Close()
		if err == nil || !strings.Contains(err.Error(), "prompt") {
			t.Errorf("%s: session started (err=%v)", name, err)
		}
	}
}
