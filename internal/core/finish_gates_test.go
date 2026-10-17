package core

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	fake "github.com/fclaude/nodwyn/test/fakes/ollama"
)

// commitFiles writes files into repo and commits them.
func commitFiles(t *testing.T, repo string, files map[string]string) {
	t.Helper()
	var names []string
	for name, content := range files {
		path := filepath.Join(repo, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		names = append(names, name)
	}
	cmd := exec.Command("git", append([]string{"add", "--"}, names...)...)
	cmd.Dir = repo
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git add: %v\n%s", err, out)
	}
	cmd = exec.Command("git", "-c", "commit.gpgsign=false", "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "-m", "files")
	cmd.Dir = repo
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git commit: %v\n%s", err, out)
	}
}

// validateLeavingAPdf is a repository whose make validate leaves an
// untracked build output behind, as validate's `make expert` left
// docs/expert/expert.pdf in the snapshots commit-bench ran against.
func validateLeavingAPdf(t *testing.T) string {
	t.Helper()
	repo := gitWorkTree(t)
	commitFiles(t, repo, map[string]string{
		"Makefile": "validate:\n\t@printf x > expert.pdf; echo 'validate: PASS (0 warnings)'\n",
	})
	return repo
}

// requestsMention counts the requests whose newest message contains want.
func requestsMention(r *rig, want string) int {
	n := 0
	for i := range r.srv.Requests {
		if strings.Contains(lastMessage(r, i), want) {
			n++
		}
	}
	return n
}

// Deleting a file a validate run left behind is no change: the repository
// as validate checked it stands. On commit-bench (dsl20r, 6e18b7ba) validate
// regenerated the untracked docs/expert/expert.pdf, the model deleted it, and
// the repository's reminder ("validate has not run since your last change")
// sent it back to validate, which made the file again: about 100k tokens,
// 7% of the run, in two builds (ADR 32, amended 2026-10-16).
func TestDeletingWhatValidateLeftIsNoChange(t *testing.T) {
	t.Parallel()
	r := trustedRigIn(t, validateLeavingAPdf(t), validationTools,
		write("w", "tracked.txt", "two\n"),
		shellArgs("v", "bash", "-c", "make validate 2>&1 | tail -n 5"),
		shell("rm", "rm expert.pdf"), // what the validate run left
		fake.Turn{Content: "done: validate: PASS (0 warnings)"},
	)
	if e := r.turn("change tracked.txt"); e != "" {
		t.Fatal(e)
	}
	if len(r.srv.Requests) != 4 {
		t.Fatalf("%d requests, want 4; last message %q", len(r.srv.Requests), lastMessage(r, len(r.srv.Requests)-1))
	}
	if n := requestsMention(r, "validate has not run"); n != 0 {
		t.Fatalf("%d requests said validate has not run after the model deleted what validate left", n)
	}
	// The run's own result says what it left, where the model can act on it.
	if raw := string(r.srv.Requests[2].Raw); !strings.Contains(raw, "validate left") || !strings.Contains(raw, "expert.pdf") {
		t.Fatalf("the validate result does not name what it left:\n%s", raw)
	}
}

// A change to a tracked file after validate is a change, with or without a
// deleted leftover: the reminder still comes.
func TestAnEditAfterValidateIsStillAChange(t *testing.T) {
	t.Parallel()
	r := trustedRigIn(t, validateLeavingAPdf(t), validationTools,
		write("w", "tracked.txt", "two\n"),
		shellArgs("v", "bash", "-c", "make validate 2>&1 | tail -n 5"),
		shell("rm", "rm expert.pdf && echo three >> tracked.txt"), // the leftover, and a real edit
		fake.Turn{Content: "done"},                                // told validate has not run
		fake.Turn{Content: "validate has not run again; the last edit is untested."},
	)
	if e := r.turn("change tracked.txt"); e != "" {
		t.Fatal(e)
	}
	if n := requestsMention(r, "validate has not run"); n != 1 {
		t.Fatalf("%d requests said validate has not run, want 1", n)
	}
}

// The files a validate run left are named in the finish review when they
// are still there, and deleting them needs no new run.
func TestWhatValidateLeftIsNamedInTheReview(t *testing.T) {
	t.Parallel()
	r := trustedRigIn(t, validateLeavingAPdf(t), validationTools,
		write("w", "tracked.txt", "two\n"),
		shellArgs("v", "bash", "-c", "make validate 2>&1 | tail -n 5"),
		fake.Turn{Content: "done: validate: PASS (0 warnings)"}, // reviewed: expert.pdf is new
		fake.Turn{Content: "expert.pdf stays out of the change; I deleted it."},
	)
	if e := r.turn("change tracked.txt"); e != "" {
		t.Fatal(e)
	}
	msg := lastMessage(r, len(r.srv.Requests)-1)
	if !strings.Contains(msg, "git does not track them: expert.pdf") || !strings.Contains(msg, "needs no new run of validate") {
		t.Fatalf("the review does not say validate left expert.pdf: %q", msg)
	}
}

// The validate tool of nodwyn's init.scm sets validated-state itself, after
// the files it left exist; deleting them afterwards is no change either.
func TestDeletingWhatTheValidateToolLeftIsNoChange(t *testing.T) {
	t.Parallel()
	repo := gitWorkTree(t)
	r := trustedRigIn(t, repo, validationTools+`
(register-tool "validate"
  (lambda ()
    (shell-exec "sh" '("-c" "printf x > expert.pdf"))
    (set! validated-state (validation-state))
    "validate: 1 changed file since 0123abcd (HEAD)\np1: exited 0 (make validate)\nvalidate: PASS (0 warnings)\n")
  '((doc . "Run the repository's checks.") (params . ())))
`,
		write("w", "tracked.txt", "two\n"),
		fake.Turn{ToolCalls: []fake.ToolCall{{ID: "v", Name: "validate", Args: map[string]any{}}}},
		shell("rm", "rm expert.pdf"),
		fake.Turn{Content: "done: validate: PASS (0 warnings)"},
	)
	if e := r.turn("change tracked.txt"); e != "" {
		t.Fatal(e)
	}
	if n := requestsMention(r, "validate has not run"); n != 0 || len(r.srv.Requests) != 4 {
		t.Fatalf("%d requests, %d said validate has not run; last message %q", len(r.srv.Requests), n, lastMessage(r, len(r.srv.Requests)-1))
	}
}

// goRunValidate is a shell line that runs go run ./scripts/validate, with a
// function standing for the go command so the test needs no toolchain.
const goRunValidate = "go() { echo 'validate: PASS (0 warnings)'; }; go run ./scripts/validate 2>&1 | tail -n 5"

// A run of go run ./scripts/validate through the shell counts as the
// validate tool's run, as a shell make validate does: it is a test run, it
// sets validated-state, and it is not a check to take apart. On commit-bench
// (dsl16r, 3422faf1) the gate did not count it (ADR 32, amended 2026-10-16).
func TestAShellGoRunValidateCountsAsValidated(t *testing.T) {
	t.Parallel()
	r := trustedRigIn(t, gitWorkTree(t), validationTools,
		write("w", "tracked.txt", "two\n"),
		shellArgs("v", "bash", "-c", goRunValidate),
		fake.Turn{Content: "done: validate: PASS (0 warnings)"},
	)
	if e := r.turn("change tracked.txt"); e != "" {
		t.Fatal(e)
	}
	if len(r.srv.Requests) != 3 {
		t.Fatalf("%d requests, want 3; last message %q", len(r.srv.Requests), lastMessage(r, len(r.srv.Requests)-1))
	}
	if got := r.results()["v"]; got != "" {
		t.Fatalf("go run ./scripts/validate failed: %s", got)
	}
}

// A plan's check step that ends in validate is met by a validate run through
// the shell, and is not asked for again while nothing has changed since.
func TestAShellValidateMeetsThePlansCheckStep(t *testing.T) {
	t.Parallel()
	plan := `(change "tracked.txt" "" "say two")
(none test "a one-line change")
(none docs "no document describes tracked.txt")
(check "go vet ./... && go run ./scripts/validate")`
	r := trustedRigIn(t, gitWorkTree(t), validationTools,
		fake.Turn{Content: "tracked.txt says one."}, // the explorer
		fake.Turn{Content: plan},                    // the planner
		write("w", "tracked.txt", "two\n"),
		shellArgs("v", "bash", "-c", goRunValidate),
		fake.Turn{Content: "done: validate: PASS (0 warnings)"},
	)
	if e := r.planned("change tracked.txt"); e != "" {
		t.Fatal(e)
	}
	if len(r.srv.Requests) != 5 {
		t.Fatalf("%d requests, want 5; last message %q", len(r.srv.Requests), lastMessage(r, len(r.srv.Requests)-1))
	}
	if n := requestsMention(r, "evidence is missing"); n != 0 {
		t.Fatalf("the plan's check step was asked for after a passing validate run")
	}
}

// A validate run through the shell is not a check to take apart, as the
// validate tool's is not: it runs for minutes, and check-without has five
// reruns for all its runs (a shell make validate took all five on commit-bench's
// 3422faf1). Here the change's part is one the target cannot tell, which
// check-without would name.
func TestAValidateRunInTheShellIsNotTakenApart(t *testing.T) {
	t.Parallel()
	repo := codeRepo(t)
	commitFiles(t, repo, map[string]string{"Makefile": "validate:\n\t@echo 'validate: PASS (0 warnings)'\n"})
	r := unattendedRigIn(t, repo,
		write("w", "calc.py", "def one():\n    return 10\n\n\ndef two():\n    return 2\n"),
		shellArgs("v", "bash", "-c", "make validate 2>&1 | tail -n 5"),
		fake.Turn{Content: "done: validate: PASS (0 warnings)"},
	)
	if e := r.turn("make one return 10"); e != "" {
		t.Fatal(e)
	}
	if len(r.srv.Requests) != 3 {
		t.Fatalf("%d requests, want 3; last message %q", len(r.srv.Requests), lastMessage(r, len(r.srv.Requests)-1))
	}
}

// A validate run in the background that exits after the files it made are
// there says so in the result that shows its exit, and deleting them is no
// change.
func TestDeletingWhatABackgroundValidateLeftIsNoChange(t *testing.T) {
	t.Parallel()
	r := trustedRigIn(t, validateLeavingAPdf(t), validationTools,
		write("w", "tracked.txt", "two\n"),
		fake.Turn{ToolCalls: []fake.ToolCall{{ID: "s", Name: "shell-start", Args: map[string]any{"cmd": "make", "args": []any{"validate"}}}}},
		fake.Turn{ToolCalls: []fake.ToolCall{{ID: "r", Name: "shell-read", Args: map[string]any{"id": "p1", "wait": 30}}}},
		shell("rm", "rm expert.pdf"),
		fake.Turn{Content: "done: validate: PASS (0 warnings)"},
	)
	if e := r.turn("change tracked.txt"); e != "" {
		t.Fatal(e)
	}
	if n := requestsMention(r, "validate has not run"); n != 0 || len(r.srv.Requests) != 5 {
		t.Fatalf("%d requests, %d said validate has not run; last message %q", len(r.srv.Requests), n, lastMessage(r, len(r.srv.Requests)-1))
	}
	if raw := string(r.srv.Requests[3].Raw); !strings.Contains(raw, "validate left") || !strings.Contains(raw, "expert.pdf") {
		t.Fatalf("the result that shows validate's exit does not name what it left:\n%s", raw)
	}
}

// oldFailure prints what go test prints for TestOld failing, and exits 1.
const oldFailure = "echo '--- FAIL: TestOld (0.01s)'; echo FAIL; echo \"FAIL\texample.com/x\t0.1s\"; exit 1 # go test ./..."

// newFailure is oldFailure with a second failing test, TestNew.
const newFailure = "echo '--- FAIL: TestOld (0.01s)'; echo '--- FAIL: TestNew (0.01s)'; echo FAIL; echo \"FAIL\texample.com/x\t0.1s\"; exit 1 # go test ./..."

// The first full test run of a turn, before any edit, runs on the unchanged
// code: the tests it fails fail there. A later run that fails only those
// is not told as a failing run (ADR 32, amended 2026-10-16). On commit-bench
// (dsl20r, 7513f7eb) 14 calls went on proving a failure was also on a
// pristine copy, after "your last test run after your last edit failed".
func TestAFailureTheUnchangedCodeHadIsNotTold(t *testing.T) {
	t.Parallel()
	r := unattendedRig(t,
		shell("t0", oldFailure), // before any edit: TestOld fails on the unchanged code
		writeFile("w"),
		shell("t1", oldFailure), // after the edit: the same failure
		fake.Turn{Content: "TestOld failed before my change too."},
	)
	if e := r.turn("fix it"); e != "" {
		t.Fatal(e)
	}
	if len(r.srv.Requests) != 4 {
		t.Fatalf("%d requests, want 4; last message %q", len(r.srv.Requests), lastMessage(r, len(r.srv.Requests)-1))
	}
	// The later run's result says so, where the model can act on it; the
	// first run, which is the unchanged code's own, does not.
	if got := lastMessage(r, 3); !strings.Contains(got, "also failed on the unchanged code") {
		t.Fatalf("the failing run's result does not say TestOld failed before: %q", got)
	}
	if got := lastMessage(r, 1); strings.Contains(got, "also failed on the unchanged code") {
		t.Fatalf("the first run, on the unchanged code, was told it failed there before: %q", got)
	}
}

// A failure the unchanged code did not have is told, with the ones it did.
func TestANewFailureIsToldWithTheSharedOnes(t *testing.T) {
	t.Parallel()
	r := unattendedRig(t,
		shell("t0", oldFailure),
		writeFile("w"),
		shell("t1", newFailure),
		fake.Turn{Content: "done"}, // told TestNew is new
		fake.Turn{Content: "TestNew is mine to fix; TestOld failed before."},
	)
	if e := r.turn("fix it"); e != "" {
		t.Fatal(e)
	}
	msg := lastMessage(r, 4)
	for _, want := range []string{"last test run after your last edit failed", "These also fail on the unchanged code", "TestOld", "do not fix them", "These do not: TestNew"} {
		if !strings.Contains(msg, want) {
			t.Errorf("the failing-run reminder lacks %q: %q", want, msg)
		}
	}
}

// A comparison the model made on the copy of the last commit is remembered
// across its later edits: the same failure after another edit needs no new
// comparison.
func TestAComparisonOnTheCopyIsRememberedAcrossEdits(t *testing.T) {
	t.Parallel()
	r := unattendedRig(t,
		writeFile("w"),
		shell("t1", oldFailure),
		shell("b", "echo '--- FAIL: TestOld (0.01s)'; echo FAIL; echo \"FAIL\texample.com/x\t0.1s\"; exit 1 # cd $TMPDIR/base && go test ./..."),
		write("w2", "fix.txt", "y\n"), // another edit
		shell("t2", oldFailure),       // the same failure, already compared
		fake.Turn{Content: "TestOld fails on the unchanged code too."},
	)
	if e := r.turn("fix it"); e != "" {
		t.Fatal(e)
	}
	if len(r.srv.Requests) != 6 {
		t.Fatalf("%d requests, want 6; last message %q", len(r.srv.Requests), lastMessage(r, len(r.srv.Requests)-1))
	}
}

// A test the request names is the task's to fix: failing before and after
// is not the unchanged code's failure to excuse.
func TestAFailingTestTheRequestNamesIsStillTold(t *testing.T) {
	t.Parallel()
	r := unattendedRig(t,
		shell("t0", oldFailure),
		writeFile("w"),
		shell("t1", oldFailure),
		fake.Turn{Content: "done"}, // told: TestOld is what the request asks to fix
		fake.Turn{Content: "TestOld still fails; the fix is incomplete."},
	)
	if e := r.turn("make TestOld pass"); e != "" {
		t.Fatal(e)
	}
	if len(r.srv.Requests) != 5 || !strings.Contains(lastMessage(r, 4), "last test run after your last edit failed") {
		t.Fatalf("%d requests; last message %q", len(r.srv.Requests), lastMessage(r, len(r.srv.Requests)-1))
	}
}

// A test the turn wrote fails on the copy of the last commit as it should,
// which says nothing about it failing again after the change.
func TestAFailingTestTheTurnWroteIsStillTold(t *testing.T) {
	t.Parallel()
	r := unattendedRig(t,
		write("w", "old_test.go", "package x\n\nfunc TestOld(t *testing.T) {}\n"),
		shell("b", "echo '--- FAIL: TestOld (0.01s)'; echo FAIL; echo \"FAIL\texample.com/x\t0.1s\"; exit 1 # cd $TMPDIR/base && go test ./..."),
		write("w2", "fix.txt", "y\n"),
		shell("t1", oldFailure),
		fake.Turn{Content: "done"}, // told: TestOld is the turn's own test
		fake.Turn{Content: "TestOld is mine and fails."},
	)
	if e := r.turn("fix it"); e != "" {
		t.Fatal(e)
	}
	if len(r.srv.Requests) != 6 || !strings.Contains(lastMessage(r, 5), "last test run after your last edit failed") {
		t.Fatalf("%d requests; last message %q", len(r.srv.Requests), lastMessage(r, len(r.srv.Requests)-1))
	}
}

// A turn that began with uncommitted changes did not run its first tests on
// the last commit: what failed there is no failure of the unchanged code.
func TestAFailureBeforeTheFirstEditIsNotTheLastCommitsIfTheTurnBeganDirty(t *testing.T) {
	t.Parallel()
	repo := gitWorkTree(t)
	if err := os.WriteFile(filepath.Join(repo, "tracked.txt"), []byte("edited by the last turn\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r := unattendedRigIn(t, repo,
		shell("t0", oldFailure),
		write("w", "tracked.txt", "x\n"),
		shell("t1", oldFailure),
		fake.Turn{Content: "done"}, // told: the first run was not on the last commit
		fake.Turn{Content: "TestOld fails; it may be the last turn's."},
	)
	if e := r.turn("fix it"); e != "" {
		t.Fatal(e)
	}
	if len(r.srv.Requests) != 5 || !strings.Contains(lastMessage(r, 4), "last test run after your last edit failed") {
		t.Fatalf("%d requests; last message %q", len(r.srv.Requests), lastMessage(r, len(r.srv.Requests)-1))
	}
}

// A validate run that fails is the latest run, whatever failed in the run
// before it: known failures of that one excuse nothing about this.
func TestAFailingValidateAfterAKnownFailureIsStillTold(t *testing.T) {
	t.Parallel()
	repo := gitWorkTree(t)
	commitFiles(t, repo, map[string]string{"Makefile": "validate:\n\t@echo 'FAIL  make accept: exit 2'; exit 1\n"})
	r := unattendedRigIn(t, repo,
		shell("t0", oldFailure),
		write("w", "tracked.txt", "two\n"),
		shell("t1", oldFailure), // TestOld, known
		shellArgs("v", "make", "validate"),
		fake.Turn{Content: "done"}, // told: validate failed
		fake.Turn{Content: "validate fails; I will look at it."},
	)
	if e := r.turn("fix it"); e != "" {
		t.Fatal(e)
	}
	msg := lastMessage(r, len(r.srv.Requests)-1)
	if !strings.Contains(msg, "last test run after your last edit failed") || strings.Contains(msg, "These also fail") {
		t.Fatalf("%d requests; last message %q", len(r.srv.Requests), msg)
	}
}

// What a test run's output says failed, for Go, pytest and unittest; a
// package line is named as a package.
func TestFailureNamesReadWhatTheOutputSays(t *testing.T) {
	t.Parallel()
	r := unattendedRig(t)
	got := r.eval(`(failure-names (string-join (list
  "=== RUN   TestA" "--- FAIL: TestA (0.00s)" "    --- FAIL: TestA/sub_case (0.00s)" "FAIL"
  "FAIL\tgithub.com/x/y\t0.312s" "FAIL\tgithub.com/x/z [build failed]"
  "FAILED tests/test_a.py::test_b - AssertionError: 1 != 2" "FAIL: test_c (mod.Class)" "ERROR: test_d (mod.Class)"
  "--- FAIL: TestA (0.00s)" "ok  \tgithub.com/x/ok\t0.1s") "\n"))`)
	const want = `("TestA" "TestA/sub_case" "package github.com/x/y" "package github.com/x/z" "tests/test_a.py::test_b" "test_c (mod.Class)" "test_d (mod.Class)")`
	if got != want {
		t.Fatalf("failure-names:\n got %s\nwant %s", got, want)
	}
}

// A plan's test step may name a scenario file: a file the run added, or one
// the repository has, that an existing test runs. The step is met by that
// file and by the run of the tests that cover it, and the file is no stray.
// On commit-bench (dsl20r, 3422faf1) the plan named TestExfilScenario019 for
// a scenario file; the gate wanted that test in the file, and the run
// refactored a test file, regenerated the catalogue and ran validate again:
// 21 calls, 22% of the run (ADR 32, amended 2026-10-16).
func TestAScenarioFileMeetsItsTestStep(t *testing.T) {
	t.Parallel()
	repo := codeRepo(t)
	commitFiles(t, repo, map[string]string{"test/exfil/scenarios/007-old.scm": "(scenario)\n"})
	plan := `(change "calc.py" "one" "return 10")
(test "test/exfil/scenarios/019" "TestExfilScenario019" "a payload with a key before a private key does not crash" (fails-today "it panics") (run "go test ./test/exfil/"))
(test "test/exfil/scenarios/007-old.scm" "TestExfilScenario007" "the existing scenario still holds" (passes-today "it guards the old case") (run "go test ./test/exfil/"))
(none docs "no document describes calc")
(check "go test ./test/exfil/")`
	r := unattendedRigIn(t, repo,
		fake.Turn{Content: "calc.py defines one()."}, // the explorer
		fake.Turn{Content: plan},                     // the planner
		write("w", "calc.py", "def one():\n    return 10\n\n\ndef two():\n    return 2\n"),
		write("s", "test/exfil/scenarios/019-key-before-private-key.scm", "(scenario 19)\n"),
		// The check reads both files, so taking either out of the change fails it.
		shellArgs("t", "bash", "-c", "grep -q 'return 10' calc.py && test -f test/exfil/scenarios/019-key-before-private-key.scm && echo 'ok  \texample.com/x/test/exfil\t0.1s' # go test ./test/exfil/"),
		fake.Turn{Content: "done"},
	)
	if e := r.planned("make one() return 10"); e != "" {
		t.Fatal(e)
	}
	if len(r.srv.Requests) != 6 {
		t.Fatalf("%d requests, want 6; last message %q", len(r.srv.Requests), lastMessage(r, len(r.srv.Requests)-1))
	}
	for i := range r.srv.Requests {
		if msg := lastMessage(r, i); strings.Contains(msg, "the test is not in that file") || strings.Contains(msg, "git does not track them") {
			t.Fatalf("request %d: %q", i, msg)
		}
	}
}

// Only a scenario step's path starts a file's name: the backup of a file the
// plan changes is not the plan's, and is still named.
func TestABackupOfAPlannedFileIsStillNamed(t *testing.T) {
	t.Parallel()
	plan := `(change "calc.py" "one" "return 10")
(none test "a one-line change")
(none docs "no document describes calc")
(check "grep -q 'return 10' calc.py")`
	r := unattendedRigIn(t, codeRepo(t),
		fake.Turn{Content: "calc.py defines one()."}, // the explorer
		fake.Turn{Content: plan},                     // the planner
		write("w", "calc.py", "def one():\n    return 10\n\n\ndef two():\n    return 2\n"),
		write("b", "calc.py.orig", "def one():\n    return 1\n"),
		shellArgs("c", "bash", "-c", "grep -q 'return 10' calc.py && test -f calc.py.orig # pytest"),
		fake.Turn{Content: "done"},
		fake.Turn{Content: "calc.py.orig is a backup; deleted."},
	)
	if e := r.planned("make one() return 10"); e != "" {
		t.Fatal(e)
	}
	if msg := lastMessage(r, len(r.srv.Requests)-1); !strings.Contains(msg, "git does not track them: calc.py.orig") {
		t.Fatalf("the backup was not named: %q", msg)
	}
}

// The finish review is made at most finish-review-limit (2) times in a run
// nobody reads as it goes, whatever the turns: each is the passing check run
// again with each part taken out, and what it asks for. A turn after the
// second ends on its report. The count is the session's; a chat's turn,
// which a person reads, is not held to it (ADR 32, amended 2026-10-16).
func TestTheFinishReviewIsMadeAtMostTwiceInARun(t *testing.T) {
	t.Parallel()
	planned := func(t *testing.T, reviewsSoFar string) *rig {
		r := unattendedRigIn(t, gitWorkTree(t),
			fake.Turn{Content: "main.go declares package main."}, // the explorer
			fake.Turn{Content: "1. Add a file."},                 // the planner
			write("w", "stray.txt", "left behind\n"),
			shell("t", "true # pytest"),
			fake.Turn{Content: "done"},
			fake.Turn{Content: "stray.txt is part of the change."}, // answers a review, if there is one
		)
		r.eval("(set! finish-reviews " + reviewsSoFar + ")")
		if e := r.planned("add a file"); e != "" {
			t.Fatal(e)
		}
		return r
	}
	t.Run("the first review", func(t *testing.T) {
		r := planned(t, "0")
		if n := requestsMention(r, "git does not track them"); n != 1 {
			t.Fatalf("%d reviews, want 1", n)
		}
		if got := r.eval("finish-reviews"); got != "1" {
			t.Fatalf("finish-reviews = %s after one review", got)
		}
	})
	t.Run("the second review", func(t *testing.T) {
		r := planned(t, "1")
		if n := requestsMention(r, "git does not track them"); n != 1 {
			t.Fatalf("%d reviews, want 1", n)
		}
	})
	t.Run("a third turn ends on its report", func(t *testing.T) {
		r := unattendedRigIn(t, gitWorkTree(t),
			fake.Turn{Content: "main.go declares package main."}, // the explorer
			fake.Turn{Content: "1. Add a file."},                 // the planner
			write("w", "stray.txt", "left behind\n"),
			shell("t", "true # pytest"),
			fake.Turn{Content: "done"},
		)
		r.eval("(set! finish-reviews 2)")
		if e := r.planned("add a file"); e != "" {
			t.Fatal(e)
		}
		if n := requestsMention(r, "git does not track them"); n != 0 || len(r.srv.Requests) != 5 {
			t.Fatalf("%d reviews in %d requests, want none in 5", n, len(r.srv.Requests))
		}
	})
	t.Run("a chat's turn is not held to the limit", func(t *testing.T) {
		r := unattendedRigIn(t, gitWorkTree(t),
			write("w", "stray.txt", "left behind\n"),
			shell("t", "true # pytest"),
			fake.Turn{Content: "done"},
			fake.Turn{Content: "stray.txt is part of the change."},
		)
		r.eval("(set! finish-reviews 2)")
		if e := r.turn("add a file"); e != "" {
			t.Fatal(e)
		}
		if n := requestsMention(r, "git does not track them"); n != 1 {
			t.Fatalf("%d reviews, want 1", n)
		}
	})
}
