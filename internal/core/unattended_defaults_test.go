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

// A test run that exits non-zero is a run, but it is no evidence that the
// change works: a turn that ends on one is told once (ADR 32, amended
// 2026-10-06), and ends after the model has run the tests green.
func TestUnattendedTurnIsToldOfAFailingTestRun(t *testing.T) {
	t.Parallel()
	r := unattendedRig(t,
		writeFile("w"),                       // a change
		shell("t1", "false # go test ./..."), // a test run that fails
		fake.Turn{Content: "done"},           // told the run failed
		shell("t2", "true # go test ./..."),  // fixed, and run again
		fake.Turn{Content: "done"},           // ends
	)
	if e := r.turn("fix it"); e != "" {
		t.Fatal(e)
	}
	if len(r.srv.Requests) != 5 {
		t.Fatalf("%d requests, want 5", len(r.srv.Requests))
	}
	if !strings.Contains(lastMessage(r, 3), "last test run after your last edit failed") {
		t.Fatalf("failing-run reminder: %q", lastMessage(r, 3))
	}
}

// The reminder offers a way out, that the tests fail on the unchanged code
// too: a model that says so ends the turn without running them again, and
// is not asked a second time about the run it was told of.
func TestAFailingTestRunIsToldOnce(t *testing.T) {
	t.Parallel()
	r := unattendedRig(t,
		writeFile("w"),
		shell("t1", "false # go test ./..."),
		fake.Turn{Content: "done"}, // told
		fake.Turn{Content: "TestX fails on the unchanged code too; the change is not the cause."},
	)
	if e := r.turn("fix it"); e != "" {
		t.Fatal(e)
	}
	if len(r.srv.Requests) != 4 {
		t.Fatalf("%d requests, want 4", len(r.srv.Requests))
	}
}

// A failing run before the last edit says nothing about the change as it
// now is: only the runs after the last edit count. This holds without the
// failing-run reminder too, and is here so the reminder does not nag.
func TestAFailingRunBeforeTheLastEditIsNotTold(t *testing.T) {
	t.Parallel()
	r := unattendedRig(t,
		writeFile("w"),
		shell("t1", "false # go test ./..."),
		write("w2", "fix.txt", "y\n"),
		shell("t2", "true # go test ./..."),
		fake.Turn{Content: "done"},
	)
	if e := r.turn("fix it"); e != "" {
		t.Fatal(e)
	}
	if len(r.srv.Requests) != 5 {
		t.Fatalf("%d requests, want 5", len(r.srv.Requests))
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
// before it ends: it changes something or runs out of reminders. The
// reminder's way out is a check that shows the code already does what the
// request asks; a turn that runs none is reminded again.
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
		if got := lastMessage(r, i); !strings.HasPrefix(got, "No file has changed yet. Make the change in the repository before finishing. If you have found") {
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
// change, and running it is a test run, failing or not. A failing one is a
// run, so the turn is told it failed and not that nothing ran.
func TestCheckScriptIsATestNotAChange(t *testing.T) {
	t.Parallel()
	r := unattendedRig(t,
		write("c", "$TMPDIR/check.sh", "exit 1\n"), // the check, failing
		fake.Turn{Content: "done"},                 // nothing changed yet: reminded
		writeFile("w"),                             // a change
		shellArgs("t", "bash", "$TMPDIR/check.sh"), // the check, after the change: a run, though it fails
		fake.Turn{Content: "done"},                 // told it failed
		fake.Turn{Content: "done"},                 // ends
	)
	if e := r.turn("fix it"); e != "" {
		t.Fatal(e)
	}
	if len(r.srv.Requests) != 6 {
		t.Fatalf("%d requests, want 6; last message %q", len(r.srv.Requests), lastMessage(r, len(r.srv.Requests)-1))
	}
	if !strings.Contains(lastMessage(r, 2), "No file has changed yet") {
		t.Fatalf("a write to $TMPDIR counted as a change: %q", lastMessage(r, 2))
	}
	if !strings.Contains(lastMessage(r, 5), "last test run after your last edit failed") {
		t.Fatalf("a failing check was not told as a failing run: %q", lastMessage(r, 5))
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
	turns = append(turns, fake.Turn{Content: "done"}, fake.Turn{Content: "done"}) // told the last run failed, then ends
	r := unattendedRig(t, turns...)
	if e := r.turn("fix it"); e != "" {
		t.Fatal(e)
	}
	if len(r.srv.Requests) != 10 {
		t.Fatalf("%d requests, want 10", len(r.srv.Requests))
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
	if worker := lastMessage(r, 2); worker != "What a read-only look at the repository found while the plan was made. Nothing was run to find it: its facts are leads, and any fix it suggests is a guess to test (read a file again only where you need its exact text):\nmain.go declares package main; tests run with go test ./..." {
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

// A request the code already answers ends unchanged once a check has run:
// on django-13513 every harness found the fix in place, and the no-change
// reminder sent all three nodwyn runs back to change something anyway (ADR
// 32, amended 2026-10-10). The way out is a check, so a turn that runs none
// is reminded again (TestUnreviewedTurnIsNotOfferedAWayOut).
func TestAnUnattendedTurnMayEndUnchangedAfterACheck(t *testing.T) {
	t.Parallel()
	r := newRig(t, true,
		fake.Turn{Content: "debug.py already handles it."},                               // the explorer
		fake.Turn{Content: "1. Check: the fix — confirm it is in place."},                // the planner
		fake.Turn{Content: "The fix is already there."},                                  // nothing changed: reminded
		shell("t", "true # pytest tests/test_debug.py"),                                  // the check the reminder asks for
		fake.Turn{Content: "The check passes on the unchanged code: nothing to change."}, // ends
	)
	if err := r.s.SendPlanned("make the debug page show the cause"); err != nil {
		t.Fatal(err)
	}
	r.drain("n")
	if len(r.srv.Requests) != 5 {
		t.Fatalf("%d requests, want 5; last message %q", len(r.srv.Requests), lastMessage(r, len(r.srv.Requests)-1))
	}
	if !strings.Contains(lastMessage(r, 3), "show it instead: run a check with the request's own example") {
		t.Fatalf("no-change reminder: %q", lastMessage(r, 3))
	}
}

// A run on the copy of the last commit that fails as the change's run did is
// the comparison the failing-run reminder asks for, done: the reminder is
// not said. On django-13158 it was, after the model had shown the failures
// on the unchanged code, at a cost of about 12 calls (ADR 32, amended
// 2026-10-10).
func TestAFailureOnTheUnchangedCodeTooIsNotTold(t *testing.T) {
	t.Parallel()
	r := unattendedRig(t,
		writeFile("w"),
		shell("t1", "false # go test ./..."),
		shell("b", "false # cd $TMPDIR/base && go test ./..."),
		fake.Turn{Content: "TestX fails on the unchanged code too."}, // ends
	)
	if e := r.turn("fix it"); e != "" {
		t.Fatal(e)
	}
	if len(r.srv.Requests) != 4 {
		t.Fatalf("%d requests, want 4; last message %q", len(r.srv.Requests), lastMessage(r, len(r.srv.Requests)-1))
	}
}

// An exit that says no test ran is no failure: a program not found (127),
// as a missing flake8 at the end of a line whose tests had passed was
// (django-15161), or pytest given a path that does not exist (4), which
// cost scikit-learn-13124 35 calls after its work was done (ADR 32, amended
// 2026-10-10).
func TestAnExitThatRanNoTestsIsNoFailure(t *testing.T) {
	t.Parallel()
	r := unattendedRig(t,
		writeFile("w"),
		shell("t", "true # go test ./..."),
		shell("l", "exit 127 # go test ./... && flake8"),
		shell("p", "exit 4 # pytest tests/missing.py"),
		fake.Turn{Content: "done"}, // ends
	)
	if e := r.turn("fix it"); e != "" {
		t.Fatal(e)
	}
	if len(r.srv.Requests) != 5 {
		t.Fatalf("%d requests, want 5; last message %q", len(r.srv.Requests), lastMessage(r, len(r.srv.Requests)-1))
	}
}

// A failing run the model was told of is not told again until it edits:
// running the same failing tests again says nothing new.
func TestAFailingRunIsToldOncePerEdit(t *testing.T) {
	t.Parallel()
	r := unattendedRig(t,
		writeFile("w"),
		shell("t1", "false # go test ./..."),
		fake.Turn{Content: "done"}, // told
		shell("t2", "false # go test ./... -run TestX"),
		fake.Turn{Content: "TestX fails without my change too."}, // ends
	)
	if e := r.turn("fix it"); e != "" {
		t.Fatal(e)
	}
	if len(r.srv.Requests) != 5 {
		t.Fatalf("%d requests, want 5; last message %q", len(r.srv.Requests), lastMessage(r, len(r.srv.Requests)-1))
	}
}

// Files new in the repository that the turn's plan names are part of the
// change, not what a run left behind: the reminder names only the others.
// On the long-context benchmark it named the test and release-note files
// the plan had asked for in 13 of 107 attempts (ADR 32, amended
// 2026-10-10).
func TestNewFilesThePlanNamesAreNotNamed(t *testing.T) {
	t.Parallel()
	const plan = "1. Change: tracked.txt — say two.\n" +
		"2. Test: tests/test_two.py test_two — it reads two; fails today: it says one.\n" +
		"3. Docs: none — no doc describes it.\n" +
		"4. Check: pytest tests/test_two.py"
	r := unattendedRigIn(t, gitWorkTree(t),
		fake.Turn{ToolCalls: []fake.ToolCall{planWriteCall("p", plan)}},
		write("w", "tests/test_two.py", "def test_two(): pass\n"),
		write("s", "repro.py", "print(1)\n"),
		shell("t", "true # pytest tests/test_two.py"),
		fake.Turn{Content: "done"}, // repro.py named
		fake.Turn{Content: "done"}, // ends
	)
	if e := r.turn("fix it"); e != "" {
		t.Fatal(e)
	}
	if len(r.srv.Requests) != 6 {
		t.Fatalf("%d requests, want 6", len(r.srv.Requests))
	}
	if note := lastMessage(r, 5); !strings.Contains(note, "git does not track them: repro.py.") || strings.Contains(note, "tests") {
		t.Fatalf("new files note: %q", note)
	}
}

// Both system prompts leave the plan's choices to the runs, ask for the
// evidence that decides a detail at the edit, and for a test that tells the
// fix from its nearest wrong version; the plan's author is not called one
// "who read the code", which a model quoted back to keep the plan's wrong
// choice (xarray-6599). The environment is found without listing folders
// outside the repository, and the comparison with the unchanged code runs
// the copy's code (ADR 32, amended 2026-10-10).
func TestThePromptsLeaveThePlansChoicesToTheRuns(t *testing.T) {
	t.Parallel()
	for name, sys := range map[string]string{
		"unattended":  unattendedRig(t).s.loop.WindowContext().System,
		"interactive": newRig(t, true).s.loop.WindowContext().System,
	} {
		for _, want := range []string{
			"A choice is not right because the plan made it",
			"Before an edit that settles a detail the request leaves open",
			"includes an input that tells it apart from the nearest wrong version",
			"the existing tests that encode the old behaviour the request changes",
			"Only those: an existing test of something the request does not mention is a constraint",
			"run your check once against the option you rejected too",
			"conda env list",
			"PYTHONPATH=$TMPDIR/base",
		} {
			if !strings.Contains(sys, want) {
				t.Errorf("the %s prompt lacks %q", name, want)
			}
		}
		for _, gone := range []string{"someone who read the code", "ls /opt/*/envs"} {
			if strings.Contains(sys, gone) {
				t.Errorf("the %s prompt still says %q", name, gone)
			}
		}
	}
}

// The explorer reads the code first, lists every place the change must
// reach and the options it finds without choosing, and copies the request's
// literals; the planner leaves an open detail open, copies the literals and
// asks for a test input that tells right from nearly right. On the
// long-context benchmark what the explorer listed won tasks and what it
// decided lost them (ADR 32, amended 2026-10-10).
func TestTheExplorerAndThePlannerLeaveChoicesOpen(t *testing.T) {
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
	explorer := requestMessages(t, r.srv.Requests[0])
	for _, want := range []string{"Read the code the request involves first", "Do not decide the fix"} {
		if !strings.Contains(explorer[0].Content, want) {
			t.Errorf("the explorer's system prompt lacks %q", want)
		}
	}
	for _, want := range []string{"every place the change must reach", "copied exactly", "without choosing", "no chosen fix"} {
		if !strings.Contains(explorer[1].Content, want) {
			t.Errorf("the explorer's task lacks %q", want)
		}
	}
	planner := lastMessage(r, 1)
	for _, want := range []string{"name the options and what will decide between them", "Copy the request's own examples",
		"with an input that tells it apart from the nearest wrong behaviour", "never a ticket or issue number"} {
		if !strings.Contains(planner, want) {
			t.Errorf("the planner's request lacks %q", want)
		}
	}
}

// codeRepo is a git repository with calc.py committed, for the checks that
// look at code: check-without leaves documents such as tracked.txt alone.
func codeRepo(t *testing.T) string {
	t.Helper()
	repo := gitWorkTree(t)
	cmd := exec.Command("sh", "-c", "printf 'def one():\\n    return 1\\n\\n\\ndef two():\\n    return 2\\n' > calc.py && git add calc.py && git -c commit.gpgsign=false -c user.name=t -c user.email=t@t commit -q -m code")
	cmd.Dir = repo
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	return repo
}

// Before an unattended turn ends on a passing check, the check runs again
// with each part of the change taken out in turn (check-without), and the
// parts it still passes without are named: it does not test them. Here the
// check looks at one()'s return only, so two()'s change is named, once (ADR
// 32, amended 2026-10-11).
func TestTheChecksUntestedPartsAreNamed(t *testing.T) {
	t.Parallel()
	repo := codeRepo(t)
	r := unattendedRigIn(t, repo,
		write("c", "$TMPDIR/check.sh", "grep -q 'return 10' calc.py\n"),
		write("w", "calc.py", "def one():\n    return 10\n\n\ndef two():\n    return 20\n"),
		shellArgs("t", "bash", "$TMPDIR/check.sh"),
		fake.Turn{Content: "done"}, // told the check does not test two()'s change
		fake.Turn{Content: "two() is part of the change; the check now covers it."},
	)
	if e := r.turn("make one return 10 and two return 20"); e != "" {
		t.Fatal(e)
	}
	if len(r.srv.Requests) != 5 {
		t.Fatalf("%d requests, want 5; last message %q", len(r.srv.Requests), lastMessage(r, len(r.srv.Requests)-1))
	}
	msg := lastMessage(r, 4)
	if !strings.Contains(msg, "still passes with each of these parts of your change taken out") || !strings.Contains(msg, "calc.py:6 (hunk") || strings.Contains(msg, "calc.py:2 (hunk") {
		t.Fatalf("untested parts message: %q", msg)
	}
	if data, _ := os.ReadFile(filepath.Join(repo, "calc.py")); !strings.Contains(string(data), "return 20") {
		t.Fatalf("calc.py was not put back as the model left it:\n%s", data)
	}
}

// A report that cites a command as its evidence is asked about it, once,
// when that command has not run since the last edit: "done" was claimed on
// runs that predated the last edit (ADR 32, amended 2026-10-11).
func TestAReportCitingAnOldRunIsAsked(t *testing.T) {
	t.Parallel()
	r := unattendedRig(t,
		writeFile("w1"),
		shellArgs("t1", "bash", "-c", "pytest -q tests || true"), // a run, before the last edit
		write("w2", "fix.txt", "y\n"),
		shell("t2", "true # go test ./..."), // a run after it, of something else
		fake.Turn{Content: "Done: `pytest -q tests` passes."},
		fake.Turn{Content: "Re-ran it: unchecked after the last edit."},
	)
	if e := r.turn("fix it"); e != "" {
		t.Fatal(e)
	}
	if len(r.srv.Requests) != 6 {
		t.Fatalf("%d requests, want 6; last message %q", len(r.srv.Requests), lastMessage(r, len(r.srv.Requests)-1))
	}
	if msg := lastMessage(r, 5); !strings.Contains(msg, "cites `pytest -q tests`, which has not run since your last edit") {
		t.Fatalf("citation message: %q", msg)
	}
}

// A check whose exit status cannot fail (; echo EXIT=0) still tells a part
// apart when it prints something else without it: only the parts it prints
// the same without are named. Here it counts one()'s return, so two()'s
// change alone is named, as for a check that fails (ADR 32, amended
// 2026-10-12).
func TestAMaskedCheckTellsPartsApartByWhatItPrints(t *testing.T) {
	t.Parallel()
	repo := codeRepo(t)
	r := unattendedRigIn(t, repo,
		write("c", "$TMPDIR/check.sh", "grep -c 'return 10' calc.py; echo EXIT=0\n"),
		write("w", "calc.py", "def one():\n    return 10\n\n\ndef two():\n    return 20\n"),
		shellArgs("t", "bash", "$TMPDIR/check.sh"),
		fake.Turn{Content: "done"}, // told the check does not test two()'s change
		fake.Turn{Content: "two() is part of the change; the check now covers it."},
	)
	if e := r.turn("make one return 10 and two return 20"); e != "" {
		t.Fatal(e)
	}
	if len(r.srv.Requests) != 5 {
		t.Fatalf("%d requests, want 5; last message %q", len(r.srv.Requests), lastMessage(r, len(r.srv.Requests)-1))
	}
	msg := lastMessage(r, 4)
	if !strings.Contains(msg, "calc.py:6 (hunk") || strings.Contains(msg, "calc.py:2 (hunk") {
		t.Fatalf("untested parts message: %q", msg)
	}
}

// Output a report quotes, the lines of a here-document and the words of a
// grep pattern are no command the turn ran, and a command it never ran is no
// citation of an old run: none of them is asked about (ADR 32, amended
// 2026-10-12).
func TestAReportQuotingOutputIsNotAsked(t *testing.T) {
	t.Parallel()
	r := unattendedRig(t,
		shellArgs("t1", "bash", "-c", "cat > $TMPDIR/out.txt <<'EOF'\nok in 0.4s\n--- PASS: TestX\nEOF\ngrep -E '^(--- PASS|ok in)' $TMPDIR/out.txt || true"),
		writeFile("w"),
		shell("t2", "true # go test ./..."),
		fake.Turn{Content: "Done: the run printed `ok in 0.4s` and `--- PASS: TestX`; `make accept` should pass too."},
	)
	if e := r.turn("fix it"); e != "" {
		t.Fatal(e)
	}
	if len(r.srv.Requests) != 4 {
		t.Fatalf("%d requests, want 4; last message %q", len(r.srv.Requests), lastMessage(r, len(r.srv.Requests)-1))
	}
}

// A check that the repository's whole check runs again is not asked about
// once the whole check has passed since the last edit: make validate runs go
// test -race ./... itself (ADR 32, amended 2026-10-12).
func TestACitationTheWholeCheckRanAgainIsNotAsked(t *testing.T) {
	t.Parallel()
	r := unattendedRig(t,
		writeFile("w1"),
		shellArgs("t1", "bash", "-c", "go test -race ./... 2>&1 | tail -n 5 || true"), // before the last edit
		write("w2", "fix.txt", "y\n"),
		shellArgs("t2", "bash", "-c", "true # make validate"), // the whole check, after it
		fake.Turn{Content: "Done: `go test -race ./...` and `make validate` pass."},
	)
	if e := r.turn("fix it"); e != "" {
		t.Fatal(e)
	}
	if len(r.srv.Requests) != 5 {
		t.Fatalf("%d requests, want 5; last message %q", len(r.srv.Requests), lastMessage(r, len(r.srv.Requests)-1))
	}
}

// A test run in the background that exits after the last edit is a test run
// as shell-exec's is: the turn is not told it changed files after its last
// one (ADR 32, amended 2026-10-12).
func TestABackgroundTestRunIsATestRun(t *testing.T) {
	t.Parallel()
	r := unattendedRig(t,
		writeFile("w"),
		fake.Turn{ToolCalls: []fake.ToolCall{{ID: "s", Name: "shell-start", Args: map[string]any{"cmd": "sh", "args": []any{"-c", "sleep 2; true # go test ./..."}}}}},
		fake.Turn{ToolCalls: []fake.ToolCall{{ID: "r", Name: "shell-read", Args: map[string]any{"id": "p1", "wait": 30}}}},
		fake.Turn{Content: "done"},
	)
	if e := r.turn("fix it"); e != "" {
		t.Fatal(e)
	}
	if len(r.srv.Requests) != 4 {
		t.Fatalf("%d requests, want 4; last message %q", len(r.srv.Requests), lastMessage(r, len(r.srv.Requests)-1))
	}
}

// A check run in the background is the passing check check-without runs
// again, as shell-exec's is. The call it runs is the shell-start call, the
// model's own words: one rebuilt from the shell's output carries its taint,
// and the guard refused it as a command built from tool output, which ended
// a finished turn in an error (commit-bench, nodwyn c768750, 556d6d12; ADR
// 32, amended 2026-10-12).
func TestABackgroundCheckIsTakenApartLikeAnyOther(t *testing.T) {
	t.Parallel()
	repo := codeRepo(t)
	r := unattendedRigIn(t, repo,
		write("c", "$TMPDIR/check.sh", "grep -q 'return 10' calc.py\n"),
		write("w", "calc.py", "def one():\n    return 10\n\n\ndef two():\n    return 20\n"),
		fake.Turn{ToolCalls: []fake.ToolCall{{ID: "s", Name: "shell-start", Args: map[string]any{"cmd": "bash", "args": []any{"-c", "sleep 2; bash $TMPDIR/check.sh"}}}}},
		fake.Turn{ToolCalls: []fake.ToolCall{{ID: "r", Name: "shell-read", Args: map[string]any{"id": "p1", "wait": 30}}}},
		fake.Turn{Content: "done"}, // told the check does not test two()'s change
		fake.Turn{Content: "two() is part of the change; the check now covers it."},
	)
	if e := r.turn("make one return 10 and two return 20"); e != "" {
		t.Fatal(e)
	}
	if len(r.srv.Requests) != 6 {
		t.Fatalf("%d requests, want 6; last message %q", len(r.srv.Requests), lastMessage(r, len(r.srv.Requests)-1))
	}
	if msg := lastMessage(r, 5); !strings.Contains(msg, "calc.py:6 (hunk") || strings.Contains(msg, "calc.py:2 (hunk") {
		t.Fatalf("untested parts message: %q", msg)
	}
}

// What the end of an unattended turn finds is said in one message, once:
// here a new file git does not track, a part the passing check does not test
// and a command the report cites from before the last edit. They were three
// messages, each once a turn, and each answer re-armed the next; on
// commit-bench the rounds after the first report cost 0.41-0.85M tokens over
// five tasks (ADR 32, amended 2026-10-15). An answer to it ends the turn.
func TestTheFinishReviewIsOneMessage(t *testing.T) {
	t.Parallel()
	repo := codeRepo(t)
	r := unattendedRigIn(t, repo,
		write("c", "$TMPDIR/check.sh", "grep -q 'return 10' calc.py\n"),
		shellArgs("t1", "bash", "-c", "pytest -q tests || true"), // a run before the last edit
		write("w", "calc.py", "def one():\n    return 10\n\n\ndef two():\n    return 20\n"),
		write("s", "stray.txt", "left behind\n"),
		shellArgs("t2", "bash", "$TMPDIR/check.sh"),
		fake.Turn{Content: "Done: `pytest -q tests` passes."}, // reviewed: three points
		fake.Turn{Content: "stray.txt is gone; two() is part of the change; pytest did not run here."},
	)
	if e := r.turn("make one return 10 and two return 20"); e != "" {
		t.Fatal(e)
	}
	if len(r.srv.Requests) != 7 {
		t.Fatalf("%d requests, want 7; last message %q", len(r.srv.Requests), lastMessage(r, len(r.srv.Requests)-1))
	}
	msg := lastMessage(r, 6)
	for _, want := range []string{"found 3 things", "1. These files are new in the repository", "stray.txt", "2. Your check",
		"calc.py:6 (hunk", "3. Your report cites `pytest -q tests`", "Your next reply without tool calls ends the turn"} {
		if !strings.Contains(msg, want) {
			t.Errorf("the review lacks %q: %q", want, msg)
		}
	}
}

// After the finish review the turn is accepted: an answer ends it, even after
// an edit the model did not test again, which the review asked it to. On
// commit-bench (A c97f9a41) six chained reminders, each re-armed by the
// answer to the one before, cost 0.38M tokens with no edit to the repository
// (ADR 32, amended 2026-10-15).
func TestAnAnswerAfterTheFinishReviewEndsTheTurn(t *testing.T) {
	t.Parallel()
	repo := codeRepo(t)
	r := unattendedRigIn(t, repo,
		write("c", "$TMPDIR/check.sh", "grep -q 'return 10' calc.py\n"),
		write("w", "calc.py", "def one():\n    return 10\n\n\ndef two():\n    return 20\n"),
		shellArgs("t", "bash", "$TMPDIR/check.sh"),
		fake.Turn{Content: "done"}, // reviewed: two()'s change is untested
		write("w2", "calc.py", "def one():\n    return 10\n\n\ndef two():\n    return 2 * 10\n"),
		fake.Turn{Content: "two() now says what it computes; the check does not cover it."},
	)
	if e := r.turn("make one return 10 and two return 20"); e != "" {
		t.Fatal(e)
	}
	if len(r.srv.Requests) != 6 {
		t.Fatalf("%d requests, want 6; last message %q", len(r.srv.Requests), lastMessage(r, len(r.srv.Requests)-1))
	}
	if msg := lastMessage(r, 4); !strings.Contains(msg, "calc.py:6 (hunk") {
		t.Fatalf("review: %q", msg)
	}
}

// validationTools stands for the part of nodwyn's .nodwyn/init.scm that
// reminds a turn which changed files to validate: it compares the repository
// with validated-state, which only its validate tool sets.
const validationTools = `
(define validation-base (guard (e (else #f)) validation-base))
(define (validation-state)
  (list (filter (lambda (l) (string-prefix? "?? " l)) (or (git-state) '()))
        (guard (e (else "")) (format "~a" (git-diff "." #f (or validation-base "HEAD"))))))
(define validated-state (guard (e (else #f)) validated-state))
(define validate-base-agent-turn (or (guard (e (else #f)) validate-base-agent-turn) default-agent-turn))
(define (default-agent-turn request)
  (set! validation-base (guard (e (else #f)) (git-head)))
  (let ((before (validation-state)))
    (validate-base-agent-turn request)
    (let ((now (validation-state)))
      (when (and (not turn-stopping) (not (equal? now before)) (not (equal? now validated-state)))
        (add-message "You changed files in this turn and validate has not run since your last change.")
        (ask-model)))))
`

// A make validate run through the shell counts as the validate tool's run
// for a repository's own reminder: on commit-bench (A c97f9a41) "validate
// has not run" came twice right after make validate had run in the shell,
// and the harness runs against old snapshots whose init.scm it cannot change
// (ADR 32, amended 2026-10-15).
func TestAShellMakeValidateCountsAsValidated(t *testing.T) {
	t.Parallel()
	repo := gitWorkTree(t)
	cmd := exec.Command("sh", "-c", "printf 'validate:\\n\\t@echo \"validate: PASS (0 warnings)\"\\n' > Makefile && git add Makefile && git -c commit.gpgsign=false -c user.name=t -c user.email=t@t commit -q -m make")
	cmd.Dir = repo
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	r := trustedRigIn(t, repo, validationTools,
		write("w", "tracked.txt", "two\n"),
		shellArgs("v", "bash", "-c", "make validate 2>&1 | tail -n 5"),
		fake.Turn{Content: "done: validate: PASS (0 warnings)"},
	)
	if e := r.turn("change tracked.txt"); e != "" {
		t.Fatal(e)
	}
	if len(r.srv.Requests) != 3 {
		t.Fatalf("%d requests, want 3; last message %q", len(r.srv.Requests), lastMessage(r, len(r.srv.Requests)-1))
	}
	if got := r.results()["v"]; got != "" {
		t.Fatalf("make validate failed: %s", got)
	}
}
