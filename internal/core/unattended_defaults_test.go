package core

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fclaude/nodwyn/internal/config"
	"github.com/fclaude/nodwyn/internal/llm"
	fake "github.com/fclaude/nodwyn/test/fakes/ollama"
)

// unattendedRig is a --yolo session: nobody approves its calls.
func unattendedRig(t *testing.T, turns ...fake.Turn) *rig {
	t.Helper()
	return unattendedRigIn(t, t.TempDir(), turns...)
}

// unattendedRigIn is unattendedRig on a repository made beforehand.
func unattendedRigIn(t *testing.T, repo string, turns ...fake.Turn) *rig {
	t.Helper()
	srv := fake.NewT(t, turns...)
	t.Cleanup(srv.Close)
	logb := &bytes.Buffer{}
	s, err := New(Options{Config: config.Default(), Repo: repo, Provider: llm.NewOllama(srv.URL), AutoApprove: true, LogWriter: logb})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return &rig{t: t, s: s, srv: srv, repo: repo, logb: logb}
}

func shell(id, script string) fake.Turn {
	return fake.Turn{ToolCalls: []fake.ToolCall{{ID: id, Name: "shell-exec", Args: map[string]any{"cmd": "sh", "args": []string{"-c", script}}}}}
}

func writeFile(id string) fake.Turn {
	return fake.Turn{ToolCalls: []fake.ToolCall{{ID: id, Name: "fs-write", Args: map[string]any{"path": "fix.txt", "content": "x\n"}}}}
}

func lastMessage(r *rig, i int) string {
	msgs := requestMessages(r.t, r.srv.Requests[i])
	return msgs[len(msgs)-1].Content
}

func TestUnattendedSessionsGetTheUnattendedPrompt(t *testing.T) {
	t.Parallel()
	u := unattendedRig(t)
	if sys := u.s.loop.WindowContext().System; !strings.Contains(sys, "You work unattended") || !strings.Contains(sys, "Set up a way to check your work") {
		t.Fatalf("unattended system prompt:\n%s", sys)
	}
	i := newRig(t, true)
	if sys := i.s.loop.WindowContext().System; strings.Contains(sys, "You work unattended") {
		t.Fatal("an interactive session got the unattended prompt")
	}
	// A person reads a chat's answer, so a chat answers a question before
	// building; nobody reads an unattended one, which makes the change.
	const answer = "When the request asks a question, answer it."
	if !strings.Contains(i.s.loop.WindowContext().System, answer) || strings.Contains(u.s.loop.WindowContext().System, answer) {
		t.Fatal("the rule on questions is not the interactive prompt's alone")
	}
}

// Unattended, a turn ends on evidence: a change, then a test run after it.
func TestUnattendedTurnEndsOnEvidence(t *testing.T) {
	t.Parallel()
	r := unattendedRig(t,
		fake.Turn{Content: "done"},         // nothing changed: reminded
		writeFile("w"),                     // a change
		fake.Turn{Content: "done"},         // untested: reminded
		shell("t", "true # go test ./..."), // a test run after the change
		fake.Turn{Content: "done"},         // ends
	)
	if e := r.turn("fix it"); e != "" {
		t.Fatal(e)
	}
	if len(r.srv.Requests) != 5 {
		t.Fatalf("%d requests, want 5", len(r.srv.Requests))
	}
	if !strings.Contains(lastMessage(r, 1), "No file has changed yet") {
		t.Fatalf("no-change reminder: %q", lastMessage(r, 1))
	}
	if !strings.Contains(lastMessage(r, 3), "after your last test run") {
		t.Fatalf("untested reminder: %q", lastMessage(r, 3))
	}
}

// A person reads a chat's reply before anything else happens, so a turn
// asked for a proposal may end unchanged once the model says so: the
// reminder says it may, once.
func TestReviewedTurnMayEndUnchanged(t *testing.T) {
	t.Parallel()
	r := unattendedRig(t,
		fake.Turn{Content: "Proposal: split the tab."},                         // nothing changed: reminded, with the way out
		fake.Turn{Content: "You asked for a proposal first; nothing changed."}, // ends
	)
	if e := r.turn("propose something before changing anything"); e != "" {
		t.Fatal(e)
	}
	if len(r.srv.Requests) != 2 || !strings.Contains(lastMessage(r, 1), "unless the request asks for no change yet") {
		t.Fatalf("%d requests; last message %q", len(r.srv.Requests), lastMessage(r, 1))
	}
}

// Nobody reads a planned turn's answer outside a run, or a run's last turn,
// before it ends: it changes something or runs out of reminders, and the
// reminder offers no way out.
func TestUnreviewedTurnIsNotOfferedAWayOut(t *testing.T) {
	t.Parallel()
	r := newRig(t, true,
		fake.Turn{Content: "main.go declares package main."}, // the explorer
		fake.Turn{Content: "1. Edit main.go."},               // the planner
		fake.Turn{Content: "done"}, fake.Turn{Content: "done"}, fake.Turn{Content: "done"},
	)
	if err := r.s.SendPlanned("change main.go"); err != nil {
		t.Fatal(err)
	}
	r.drain("n")
	if len(r.srv.Requests) != 5 {
		t.Fatalf("%d requests, want 5", len(r.srv.Requests))
	}
	for _, i := range []int{3, 4} {
		if got := lastMessage(r, i); got != "No file has changed yet. Make the change in the repository before finishing." {
			t.Fatalf("reminder %d: %q", i, got)
		}
	}
}

func shellArgs(id, cmd string, args ...string) fake.Turn {
	return fake.Turn{ToolCalls: []fake.ToolCall{{ID: id, Name: "shell-exec", Args: map[string]any{"cmd": cmd, "args": args}}}}
}

func write(id, path, content string) fake.Turn {
	return fake.Turn{ToolCalls: []fake.ToolCall{{ID: id, Name: "fs-write", Args: map[string]any{"path": path, "content": content}}}}
}

// gitWorkTree is a git repository with tracked.txt committed and notes.txt
// left untracked, as a user's work tree may be.
func gitWorkTree(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	repo := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q")
	os.WriteFile(filepath.Join(repo, "tracked.txt"), []byte("one\n"), 0o644)
	run("add", "tracked.txt")
	run("-c", "commit.gpgsign=false", "commit", "-q", "-m", "first")
	os.WriteFile(filepath.Join(repo, "notes.txt"), []byte("mine\n"), 0o644)
	return repo
}

// The check script lives in the scratch directory: writing it is not a
// change, and running it is a test run, failing or not.
func TestCheckScriptIsATestNotAChange(t *testing.T) {
	t.Parallel()
	r := unattendedRig(t,
		write("c", "$TMPDIR/check.sh", "exit 1\n"), // the check, failing
		fake.Turn{Content: "done"},                 // nothing changed yet: reminded
		writeFile("w"),                             // a change
		shellArgs("t", "bash", "$TMPDIR/check.sh"), // the check, after the change
		fake.Turn{Content: "done"},                 // ends
	)
	if e := r.turn("fix it"); e != "" {
		t.Fatal(e)
	}
	if len(r.srv.Requests) != 5 {
		t.Fatalf("%d requests, want 5; last message %q", len(r.srv.Requests), lastMessage(r, len(r.srv.Requests)-1))
	}
	if !strings.Contains(lastMessage(r, 2), "No file has changed yet") {
		t.Fatalf("a write to $TMPDIR counted as a change: %q", lastMessage(r, 2))
	}
}

// A check fails until the work is done: its failing runs are not test
// commands that cannot run, so they never offer the way out.
func TestFailingCheckRunsAreNotUnrunnable(t *testing.T) {
	t.Parallel()
	turns := []fake.Turn{write("c", "$TMPDIR/check.sh", "exit 1\n"), writeFile("w")}
	for i := 0; i < 6; i++ {
		turns = append(turns, shellArgs("t"+string(rune('a'+i)), "bash", "$TMPDIR/check.sh", string(rune('a'+i))))
	}
	turns = append(turns, fake.Turn{Content: "done"})
	r := unattendedRig(t, turns...)
	if e := r.turn("fix it"); e != "" {
		t.Fatal(e)
	}
	if len(r.srv.Requests) != 9 {
		t.Fatalf("%d requests, want 9", len(r.srv.Requests))
	}
	for i := range r.srv.Requests {
		if strings.Contains(lastMessage(r, i), "did not run successfully") {
			t.Fatalf("request %d offered the way out for a failing check", i)
		}
	}
}

// In a git work tree git says what changed: a write that changes nothing
// is no change, nor is a file a run leaves behind, and an edit through the
// shell is one. The leftover is named before the turn ends.
func TestGitSaysWhatChanged(t *testing.T) {
	t.Parallel()
	r := unattendedRigIn(t, gitWorkTree(t),
		write("same", "tracked.txt", "one\n"), // the same content: no change
		shell("o", "touch run.log"),           // a run's leftover: no change
		fake.Turn{Content: "done"},            // reminded
		shell("e", "echo two >> tracked.txt"), // a change, through the shell
		shell("t", "true # go test ./..."),    // a test run after it
		fake.Turn{Content: "done"},            // run.log named
		fake.Turn{Content: "done"},            // ends
	)
	if e := r.turn("fix it"); e != "" {
		t.Fatal(e)
	}
	if len(r.srv.Requests) != 7 {
		t.Fatalf("%d requests, want 7; last message %q", len(r.srv.Requests), lastMessage(r, len(r.srv.Requests)-1))
	}
	if !strings.Contains(lastMessage(r, 3), "No file has changed yet") {
		t.Fatalf("no change counted as one: %q", lastMessage(r, 3))
	}
	if !strings.Contains(lastMessage(r, 6), "git does not track them: run.log.") {
		t.Fatalf("leftover not named: %q", lastMessage(r, 6))
	}
}

// An edit to a file git showed before the turn leaves the status as it was;
// the edit still counts, and the file is not new.
func TestEditToAFileGitShowedIsAChange(t *testing.T) {
	t.Parallel()
	r := unattendedRigIn(t, gitWorkTree(t),
		write("w", "notes.txt", "mine, edited\n"), // untracked before the turn
		shell("t", "true # pytest"),
		fake.Turn{Content: "done"}, // ends
	)
	if e := r.turn("fix it"); e != "" {
		t.Fatal(e)
	}
	if len(r.srv.Requests) != 3 {
		t.Fatalf("%d requests, want 3; last message %q", len(r.srv.Requests), lastMessage(r, len(r.srv.Requests)-1))
	}
}

// Files new since the turn began that git does not track are named once
// before the turn ends; files untracked before it are not.
func TestNewUntrackedFilesAreNamedOnce(t *testing.T) {
	t.Parallel()
	r := unattendedRigIn(t, gitWorkTree(t),
		write("w", "repro.py", "print(1)\n"), // a stray script in the repository
		shell("t", "true # pytest"),          // a test run after it
		fake.Turn{Content: "done"},           // reminded of repro.py
		fake.Turn{Content: "done"},           // ends: said once
	)
	if e := r.turn("fix it"); e != "" {
		t.Fatal(e)
	}
	if len(r.srv.Requests) != 4 {
		t.Fatalf("%d requests, want 4", len(r.srv.Requests))
	}
	note := lastMessage(r, 3)
	if !strings.Contains(note, "git does not track them: repro.py.") || strings.Contains(note, "notes.txt") {
		t.Fatalf("new files note: %q", note)
	}
}

// Test commands that keep failing to run get one way out: report the change
// as unverified instead of spending the budget.
func TestUnrunnableTestsGetAWayOut(t *testing.T) {
	t.Parallel()
	turns := []fake.Turn{writeFile("w")}
	for i := 0; i < 6; i++ {
		// Different each time: the same call four times in a row is a
		// progress stop, a different guard.
		turns = append(turns, shell("t"+string(rune('a'+i)), "exit 4 # pytest -q -k case"+string(rune('a'+i))))
	}
	turns = append(turns, fake.Turn{Content: "The tests cannot run here; the change is unverified."})
	r := unattendedRig(t, turns...)
	if e := r.turn("fix it"); e != "" {
		t.Fatal(e)
	}
	if len(r.srv.Requests) != 8 || !strings.Contains(lastMessage(r, 7), "did not run successfully") {
		t.Fatalf("%d requests; last message %q", len(r.srv.Requests), lastMessage(r, 7))
	}
}

// Before a plan is drafted, a read-only explorer looks at the repository and
// the planner is told what it found.
func TestPlannerExploresFirst(t *testing.T) {
	t.Parallel()
	r := newRig(t, true,
		fake.Turn{Content: "main.go declares package main; tests run with go test ./..."},
		fake.Turn{Content: "1. Edit main.go."},
		fake.Turn{Content: "done"}, fake.Turn{Content: "done"}, fake.Turn{Content: "done"},
	)
	if err := r.s.SendPlanned("change main.go"); err != nil {
		t.Fatal(err)
	}
	r.drain("n")
	explorer := requestMessages(t, r.srv.Requests[0])
	if !strings.HasPrefix(explorer[0].Content, "You explore a repository") || !strings.Contains(explorer[1].Content, "change main.go") {
		t.Fatalf("the explorer's context: %+v", explorer)
	}
	if planner := lastMessage(r, 1); !strings.Contains(planner, "What a look at the repository found:\nmain.go declares package main") {
		t.Fatalf("the planner was not told what exploring found:\n%s", planner)
	}
	// The model that does the work is told too, after the plan, so it does
	// not read the same files again to learn it.
	if worker := lastMessage(r, 2); worker != "What a read-only look at the repository found while the plan was made (read a file again only where you need its exact text):\nmain.go declares package main; tests run with go test ./..." {
		t.Fatalf("the worker was not told what exploring found:\n%s", worker)
	}
}

// Both system prompts ask for the calls that do not depend on each other in
// one reply: most models made one call a reply in the local sessions (1.2 to
// 1.4 for muse-spark, glm, qwen and deepseek), each reply sending the whole
// conversation again (ADR 32, amended 2026-10-05).
func TestThePromptsAskForIndependentCallsTogether(t *testing.T) {
	t.Parallel()
	const rule = "Calls that do not depend on each other go in the same reply"
	for name, sys := range map[string]string{
		"unattended":  unattendedRig(t).s.loop.WindowContext().System,
		"interactive": newRig(t, true).s.loop.WindowContext().System,
	} {
		if !strings.Contains(sys, rule) {
			t.Errorf("the %s prompt does not ask for independent calls together", name)
		}
	}
}

// Both prompts compare a failure with the unchanged code on a copy of the
// last commit, never with git stash: every worktree shares one stash, and a
// stash not popped, or popped onto other changes, loses work anywhere
// (ADR 32, amended 2026-10-05).
func TestThePromptsCompareWithACopyNotAStash(t *testing.T) {
	t.Parallel()
	for name, sys := range map[string]string{
		"unattended":  unattendedRig(t).s.loop.WindowContext().System,
		"interactive": newRig(t, true).s.loop.WindowContext().System,
	} {
		if strings.Contains(sys, "git stash;") || !strings.Contains(sys, "git archive HEAD | tar -x -C") || !strings.Contains(sys, "Never use git stash for this") {
			t.Errorf("the %s prompt's comparison with the unchanged code:\n%s", name, sys)
		}
	}
}
