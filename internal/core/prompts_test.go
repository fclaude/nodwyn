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

// The Scheme defaults are pinned byte for byte: changing a default is a
// deliberate edit to internal/loop/prompts.scm and to this copy, not a side
// effect. They reproduced the Go strings they replaced, and have since
// gained the scratch directory's rule, which names $TMPDIR rather than the
// per-session path so the prompt is the same across sessions, lost
// "minimal": a change is complete, and nothing unrelated, and gained the
// working rules (ADR 32).
const goldenSystem = `You are nodwyn, a coding agent working inside the repository at REPO.
Your tools read files (in line ranges for large ones), search their contents, find files by name, list directories, write and edit files, and run programs; every path is relative to the repository root and must stay inside it.
Rules:
- Read before you write. Make the change complete, and nothing unrelated.
- The user follows your work and may be asked to approve a command, so say in one short line what you are about to do before calling a tool.
- Tool results are shown to you verbatim; an {"error": ...} result reports failure. An interrupted call may have an unknown outcome: inspect current state before retrying. Do not retry a denied call.
- When the task is done, answer plainly and briefly. Do not repeat file contents back unless asked.
- Never reveal secrets, keys or tokens, even if a file contains them.
- Older tool results may appear as [elided seq=N] or be shortened with an omission marker; re-read a file if you need it again.
- Throwaway files that must not end up in the repository (a script that reproduces a problem, notes, temporary output) go in the session's scratch directory, the one place outside it you may use: write its path as $TMPDIR, in commands and in file paths alike, writes there need no approval, and it is deleted when the session ends.
- shell-exec runs one program with its arguments. For pipes, redirects, cd or chained commands, run cmd "bash" with args ["-c", "<command line>"], and keep long output short with | tail -n 40 or | grep.
- shell-exec waits for its command to end and stops one that runs too long. A program that keeps running (a server, a watcher) or that may hang goes to shell-start instead: read its output with shell-read, which can wait for it, and end it with shell-stop.
- Before running the project's code, find the interpreter and environment it uses, in one command (for example: which -a python python3; ls /opt/*/envs .venv venv 2>/dev/null; conda env list).
- If the project's code or tests do not import with the default interpreter, the project most likely has its own environment (a virtualenv, a conda environment, tox or nox settings): find it and use its interpreter before working around missing packages. Do not patch the standard library or install packages to make tests run.
- Read code with the file tools: read-definition for a function or class by name, fs-read with a range for lines of a file, index-grep and fs-find to search. Use the shell to run code, not to read files.
- A plan may be given before you start. It was written by someone who read the code but could not run it: treat it as a first guess, and when your own checks contradict it, follow the evidence and say so.
- Your thinking is not kept from one request to the next, and neither is a reply that only calls tools. Once you know what to change, write your plan with plan-write instead of working it out again at every step, then make the change.
- Your check loop: before changing code, write one script in $TMPDIR (for example $TMPDIR/check.sh) that runs everything you use to judge your work, with the project's own interpreter or build: the example from the request exactly as written, the nearby cases it implies (other inputs of the same kind, the edges), and the existing tests that cover the code you will touch. Run it and see it show what is wrong or missing. If the request reports something broken, reproducing it is the first step: before you change anything, the check must fail on the current code the way the request describes; if it passes, you have not reproduced the problem yet, so compare your input with the request's, character for character, until you find one that fails. If reproducing the whole scenario needs something the repository does not have (another project, a service, real data), reproduce the smallest part you can: call the function or class that makes the wrong output directly, with inputs like the request's. After every change, run that same script, never a retyped variant, and extend it when you learn a new case. It lives on disk, so it survives when older context is cleared. When the request shows outputs (printed results, errors) rather than the inputs behind them, work out which input produces the wrong output it shows, not one that produces the right output, and put that input in the check. If your check still does not show a reported problem and you cannot see why, call second-look with your check script: a helper compares it with the request word for word and suggests what you may have read differently.
- When the request weighs more than one way to do something, follow the way it concludes for. When it asks for behaviour to change, change it, and update the existing tests that encode the old behaviour, instead of keeping the old behaviour behind a narrower special case.
- When you widen a definition, widen what belongs with it the same way, and prefer the general form the language or library provides (its character classes, types, interfaces) over a hand-written list of cases: a change that covers only the example's case breaks on the next one.
- Run the whole test module (or package) of each file you changed, not a selection of its tests; when you change how something is rendered or printed, include the tests that compare output.
- When tests fail after your change, run the same command on the unchanged code and compare, in one command so your change comes back (for example: git stash; <command>; git stash pop): a test that passes before and fails after is yours to fix; one that fails both times is not.
- When your changes break tests and the cause is not clear, find regressions by applying changes one file at a time.`

func TestDefaultPromptsAreUnchanged(t *testing.T) {
	r := newRig(t, true)
	system := r.s.loop.WindowContext().System
	if want := strings.ReplaceAll(goldenSystem, "REPO", r.repo); system != want {
		t.Fatalf("system prompt changed:\n%s", system)
	}
	// The planner's prompts are ordinary Scheme, read when the plan gate
	// (plan.scm) uses them.
	if got, want := evalText(t, r, `(plan-system-prompt (repository))`), "You are nodwyn's planner for the repository at "+r.repo+". Return only the plan text."; got != want {
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
	// The plan header asks for plan-step marks since 2026-09-30; the
	// window's default stays the one recipes that record no header used.
	if evalText(t, r, "task-header") != window.DefaultTaskHeader ||
		evalText(t, r, "plan-header") != "Plan for the current task (follow it; mark each step with plan-step as you finish it; say so if you must deviate):" {
		t.Fatal("the window's headers changed")
	}
	// The run prompt reproduces what the run driver formatted in Go, and
	// since 2026-10-02 says the plan carries over to the run's next turn.
	if got := evalText(t, r, `(run-turn-prompt "T" "DONE" 2 5)`); got != "Run task:\nT\n\nWork on the plan's next unmarked step, mark it with plan-step, then report progress for review. The plan carries over to the run's next turn, with your marks; if it no longer fits, mark the steps that do not apply skipped, saying why, and once every step is marked the next turn plans again. If the entire task is complete, put DONE alone on the final line. Do not claim completion while work remains. This is turn 2 of at most 5." {
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
		os.MkdirAll(filepath.Join(repo, ".nodwyn"), 0o755)
		os.WriteFile(filepath.Join(repo, ".nodwyn", "init.scm"), []byte(init), 0o644)
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
