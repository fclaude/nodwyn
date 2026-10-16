package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/fclaude/nodwyn/test/fakes/home"
)

func TestMain(m *testing.M) {
	// The fixtures run go: it keeps the build cache and modules it has, so
	// a fresh home does not rebuild the standard library for every test.
	for _, k := range []string{"GOCACHE", "GOMODCACHE", "GOPATH"} {
		if out, err := exec.Command("go", "env", k).Output(); err == nil {
			os.Setenv(k, strings.TrimSpace(string(out)))
		}
	}
	// A home of its own (test/fakes/home): git's configuration and
	// nodwyn's denylist read from it.
	done := home.Isolate()
	code := m.Run()
	done()
	os.Exit(code)
}

// The checks follow the files: documentation no test reads needs none, the
// console and the host code and fakes it runs need the browser checks as
// well as make accept, and the browser checks' own scripts need only those.
// The books are documentation here (the release flow builds them), but their
// catalogue is checked when Go or Scheme files change, and the release
// publisher's tests run when it changes.
func TestPlanFollowsTheChangedFiles(t *testing.T) {
	t.Chdir("../..")
	for _, c := range []struct {
		changed []string
		want    []string
	}{
		{[]string{"AGENTS.md", "docs/adr/0001-scheme-on-go.md", "docs/manual.md"}, nil},
		{[]string{"docs/scheme.md"}, []string{"make accept"}},
		{[]string{"internal/core/session.go"}, []string{"gofmt", "book catalogue", "make accept", "browser checks"}},
		{[]string{"internal/core/conversation_search.go"}, []string{"gofmt", "book catalogue", "make accept", "browser checks"}},
		{[]string{"internal/schedule/schedule.go"}, []string{"gofmt", "book catalogue", "make accept", "browser checks"}},
		{[]string{"test/fakes/chat/chat.go"}, []string{"gofmt", "book catalogue", "make accept", "browser checks"}},
		{[]string{"internal/loop/prompts.scm"}, []string{"book catalogue", "make accept", "browser checks"}},
		{[]string{"internal/core/session_test.go"}, []string{"gofmt", "book catalogue", "make accept"}},
		{[]string{"test/exfil/exfil_test.go"}, []string{"gofmt", "book catalogue", "make accept"}},
		{[]string{"internal/frontend/web/assets/home.js"}, []string{"node --check", "make accept", "browser checks"}},
		{[]string{"test/browser/checks/screens.js"}, []string{"node --check", "browser checks"}},
		{[]string{"internal/frontend/rpc/sessions.go"}, []string{"gofmt", "book catalogue", "make accept", "browser checks"}},
		{[]string{"internal/frontend/rpc/server_test.go"}, []string{"gofmt", "book catalogue", "make accept"}},
		{[]string{"docs/book/parts/scheme.tex", "docs/expert/parts/09-primitives.tex", "docs/book/source/catalogue.tex"}, nil},
		{[]string{"docs/book/source/generate.py"}, []string{"book catalogue"}},
		{[]string{"internal/core/deleted.go"}, []string{"book catalogue", "make accept", "browser checks"}},
		{[]string{"scripts/release.py"}, []string{"release tests", "make accept"}},
		{[]string{"test/release/test_release.py"}, []string{"release tests", "make accept"}},
	} {
		var got []string
		for _, ch := range plan("", c.changed) {
			got = append(got, ch.name)
		}
		if !slices.Equal(got, c.want) {
			t.Errorf("plan(%v) = %v, want %v", c.changed, got, c.want)
		}
	}
}

// A command that leaves a process behind, as a failing browser check left
// its demo server on 2026-10-03, does not keep run waiting, and what it
// left is gone when run returns.
func TestRunEndsWhatACheckLeavesBehind(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "out.log")
	start := time.Now()
	exit, timedOut, err := run(".", logPath, time.Minute, "sh", "-c", "sleep 60 & echo $!")
	if err != nil || timedOut || exit != 0 {
		t.Fatalf("run: exit %d, timed out %v, %v", exit, timedOut, err)
	}
	if took := time.Since(start); took > 10*time.Second {
		t.Fatalf("run waited %s for what the command left behind", took)
	}
	raw, _ := os.ReadFile(logPath)
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		t.Fatalf("no pid in the log: %q", raw)
	}
	deadline := time.Now().Add(5 * time.Second)
	for alive(pid) {
		if time.Now().After(deadline) {
			t.Fatalf("process %d the command left is still running", pid)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// alive reports a running process; one killed but not yet reaped is a
// zombie, and counts as gone.
func alive(pid int) bool {
	if syscall.Kill(pid, 0) != nil {
		return false
	}
	stat, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return true
	}
	fields := strings.Fields(string(stat))
	return len(fields) < 3 || fields[2] != "Z"
}

// Stopping validate, as shell-stop does with SIGTERM, stops the command it
// is running and what that command started in a group of its own, as
// run.sh starts each browser check, and starts nothing after. On
// 2026-10-03 stopping it left a browser check, its demo and Chromium
// running. The test binary runs again as validate's stand-in.
func TestStoppingValidateStopsWhatItRuns(t *testing.T) {
	if dir := os.Getenv("VALIDATE_STOP_HELPER"); dir != "" {
		stopHelper(dir)
		return
	}
	dir := t.TempDir()
	cmd := exec.Command(os.Args[0], "-test.run=^TestStoppingValidateStopsWhatItRuns$")
	cmd.Env = append(os.Environ(), "VALIDATE_STOP_HELPER="+dir)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	var group int
	for deadline := time.Now().Add(30 * time.Second); group == 0; time.Sleep(50 * time.Millisecond) {
		if time.Now().After(deadline) {
			_ = cmd.Process.Kill()
			t.Fatal("the helper never started its check")
		}
		if raw, err := os.ReadFile(filepath.Join(dir, "group")); err == nil {
			group, _ = strconv.Atoi(strings.TrimSpace(string(raw)))
		}
	}
	_ = cmd.Process.Signal(syscall.SIGTERM)
	err := cmd.Wait()
	if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 130 {
		t.Fatalf("validate stopped with %v, want exit 130", err)
	}
	for deadline := time.Now().Add(5 * time.Second); syscall.Kill(-group, 0) == nil; time.Sleep(50 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("the check's own group %d still runs after validate stopped", group)
		}
	}
	if fileExists(filepath.Join(dir, "second")) {
		t.Fatal("a command started after validate was stopped")
	}
}

// stopHelper is validate as the stop test sees it: a check like run.sh,
// which runs a command in a group of its own and stops it on SIGTERM, then
// a second command, which must never start.
func stopHelper(dir string) {
	stopOnSignal()
	script := `timeout 60 sleep 60 & pid=$!
trap 'kill -KILL -- -$pid; exit 143' TERM
echo $pid > "$1/group.tmp" && mv "$1/group.tmp" "$1/group"
wait $pid`
	_, _, _ = run(dir, filepath.Join(dir, "first.log"), time.Minute, "bash", "-c", script, "bash", dir)
	_, _, _ = run(dir, filepath.Join(dir, "second.log"), time.Minute, "touch", "second")
	select {} // the signal handler exits
}

func TestRunStopsAtItsLimit(t *testing.T) {
	start := time.Now()
	_, timedOut, err := run(".", filepath.Join(t.TempDir(), "out.log"), 200*time.Millisecond, "sleep", "60")
	if err != nil || !timedOut {
		t.Fatalf("timed out %v, %v", timedOut, err)
	}
	if took := time.Since(start); took > 10*time.Second {
		t.Fatalf("took %s", took)
	}
}

func TestOutcomesReadTopLevelTests(t *testing.T) {
	out := "=== RUN   TestA\n--- PASS: TestA (0.00s)\n=== RUN   TestB\n    --- FAIL: TestB/sub (0.00s)\n--- FAIL: TestB (0.00s)\n--- SKIP: TestC (0.00s)\nFAIL\n"
	got := outcomes(out)
	if got["TestA"] != "PASS" || got["TestB"] != "FAIL" || got["TestC"] != "SKIP" || len(got) != 3 {
		t.Fatalf("outcomes = %v", got)
	}
	if !buildFailed("FAIL\texample.com/x [build failed]\n") || buildFailed(out) {
		t.Fatal("buildFailed misread")
	}
}

// fixture is a git repository holding files, committed once on main.
func fixture(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	writeAll(t, dir, files)
	gitIn(t, dir, "init", "-q", "-b", "main")
	gitIn(t, dir, "add", "-A")
	gitIn(t, dir, "commit", "-q", "-m", "base")
	return dir
}

func writeAll(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for p, body := range files {
		path := filepath.Join(dir, p)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func gitIn(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// Each changed test is run on the unchanged code. The fixture holds the
// kinds a change brings: a test that fails there (it tests the fix),
// one that passes there (on 2026-10-03 a test named for keeping
// attachments passed on the code that deleted them), and one that calls
// code the change adds, which cannot compile there. A test the change did
// not touch is not run.
func TestTestsAreRunOnTheUnchangedCode(t *testing.T) {
	repo := fixture(t, map[string]string{
		"go.mod":            "module example.com/fixture\n\ngo 1.24\n",
		"calc/calc.go":      "package calc\n\nfunc Double(n int) int { return n + n + 1 }\n",
		"calc/calc_test.go": "package calc\n\nimport \"testing\"\n\nfunc TestUntouched(t *testing.T) {}\n",
	})
	writeAll(t, repo, map[string]string{
		"calc/calc.go": "package calc\n\nfunc Double(n int) int { return n + n }\n",
		"calc/calc_test.go": `package calc

import "testing"

func TestUntouched(t *testing.T) {}

func TestDoubleIsTwice(t *testing.T) {
	if Double(2) != 4 {
		t.Fatal(Double(2))
	}
}

func TestDoubleIsNotNegative(t *testing.T) {
	if Double(0) < 0 {
		t.Fatal(Double(0))
	}
}
`,
		"calc/triple.go":      "package calc\n\nfunc Triple(n int) int { return 3 * n }\n",
		"calc/extra_test.go":  "package calc\n\nimport \"testing\"\n\nfunc TestDoubleIsPositive(t *testing.T) {\n\tif Double(1) <= 0 {\n\t\tt.Fatal()\n\t}\n}\n",
		"newapi/api_test.go":  "package newapi\n\nimport (\n\t\"testing\"\n\n\t\"example.com/fixture/calc\"\n)\n\nfunc TestTriple(t *testing.T) {\n\tif calc.Triple(2) != 6 {\n\t\tt.Fatal()\n\t}\n}\n",
		"newapi/api.go":       "package newapi\n",
		"calc/testdata/x.txt": "x\n",
	})
	t.Chdir(repo)
	base, _, err := baseCommit()
	if err != nil {
		t.Fatal(err)
	}
	changed, err := changedFiles(base)
	if err != nil {
		t.Fatal(err)
	}
	results := againstBase(base, changed, t.TempDir())
	byStatus := map[string]string{}
	for _, r := range results {
		byStatus[r.status] += r.detail
	}
	if !strings.Contains(byStatus["PASS"], "calc.TestDoubleIsTwice") {
		t.Errorf("the fix's test should fail on the unchanged code: %v", results)
	}
	if !strings.Contains(byStatus["WARN"], "calc.TestDoubleIsNotNegative") || strings.Contains(byStatus["WARN"], "TestDoubleIsTwice") {
		t.Errorf("only the test that passes on the unchanged code is flagged: %v", results)
	}
	if !strings.Contains(byStatus["SKIP"], "newapi.TestTriple (does not compile there)") {
		t.Errorf("a test of new code cannot say: %v", results)
	}
	if !strings.Contains(byStatus["WARN"], "calc.TestDoubleIsPositive") {
		t.Errorf("a new test file's tests are run: %v", results)
	}
	if strings.Contains(byStatus["PASS"]+byStatus["WARN"]+byStatus["SKIP"], "TestUntouched") {
		t.Errorf("a test the change did not touch was run: %v", results)
	}
}

// A primitive the change adds is listed in the book's and the expert
// guide's tables: on 2026-10-03 (worktree) was added to neither.
func TestNewPrimitivesAreInTheTables(t *testing.T) {
	tables := map[string]string{
		"docs/book/parts/scheme.tex":          "\\code{(old)} & old \\\\\n",
		"docs/expert/parts/09-primitives.tex": "\\code{(old)} & old \\\\\n",
	}
	files := map[string]string{"internal/prim/p.go": "package prim\n\nvar a = &scheme.Prim{Name: \"old\"}\n"}
	for p, b := range tables {
		files[p] = b
	}
	repo := fixture(t, files)
	writeAll(t, repo, map[string]string{"internal/prim/p.go": "package prim\n\nvar a = &scheme.Prim{Name: \"old\"}\n\nvar b = []*scheme.Prim{{Name: \"fresh\"}, {Name: \"fresh-too\"}}\n"})
	t.Chdir(repo)
	base, _, _ := baseCommit()
	changed, _ := changedFiles(base)
	got := primitiveTables(base, changed)
	if len(got) != 1 || got[0].status != "FAIL" || !strings.Contains(got[0].detail, "fresh in docs/book/parts/scheme.tex") ||
		!strings.Contains(got[0].detail, "fresh-too in docs/expert/parts/09-primitives.tex") || strings.Contains(got[0].detail, "old in") {
		t.Fatalf("missing primitives: %+v", got)
	}
	for p, b := range tables {
		writeAll(t, repo, map[string]string{p: b + "\\code{(fresh)}, \\code{fresh-too} & new \\\\\n"})
	}
	if got := primitiveTables(base, changed); len(got) != 1 || got[0].status != "PASS" {
		t.Fatalf("listed primitives: %+v", got)
	}
}

// A prompt change without an ADR 32 amendment, a wire change without its
// description and code without a test are each said; a change that brings
// them is not.
func TestDocRulesSayWhatIsMissing(t *testing.T) {
	got := docRules("", []string{"internal/loop/prompts.scm", "docs/protocol.schema.json"})
	var names []string
	for _, r := range got {
		if r.status != "WARN" {
			t.Errorf("rule %s is %s", r.name, r.status)
		}
		names = append(names, r.name)
	}
	if !slices.Equal(names, []string{"prompts", "protocol", "tests"}) {
		t.Fatalf("rules = %v", names)
	}
	// A console change is not covered by an unrelated Go test: it asks for a
	// browser check, which AGENTS.md asks for and a Go test does not run.
	t.Chdir("../..")
	got = docRules("", []string{"internal/frontend/web/assets/home.js", "internal/core/session_test.go"})
	if len(got) != 1 || got[0].name != "browser check" || got[0].status != "WARN" {
		t.Fatalf("console change with a Go test only: %+v", got)
	}
	if got := docRules("", []string{"internal/frontend/web/assets/home.js", "test/browser/checks/screens.js"}); len(got) != 0 {
		t.Fatalf("console change with its browser check: %+v", got)
	}
	if got := docRules("", []string{"internal/loop/prompts.scm", "docs/adr/0032-working-habits.md", "docs/protocol.schema.json", "docs/protocol.md", "internal/core/strategy_test.go"}); len(got) != 0 {
		t.Fatalf("a complete change: %+v", got)
	}
}

// A branch with no upstream, as a session's worktree branch is, is measured
// from where it left the main line, so its committed work is still a change
// to check: on 2026-10-04 validate said "nothing changed since HEAD" after
// every commit in a worktree, and models re-ran make accept and the browser
// checks by hand. On the main line itself, with no upstream, a commit is
// not a change, as before.
func TestABranchWithoutUpstreamIsMeasuredFromTheMainLine(t *testing.T) {
	repo := fixture(t, map[string]string{"a.txt": "a\n"})
	gitIn(t, repo, "checkout", "-q", "-b", "nodwyn/task")
	writeAll(t, repo, map[string]string{"b.txt": "b\n"})
	gitIn(t, repo, "add", "-A")
	gitIn(t, repo, "commit", "-q", "-m", "the task's work")
	t.Chdir(repo)
	base, against, err := baseCommit()
	if err != nil {
		t.Fatal(err)
	}
	main, _ := git("rev-parse", "main")
	if base != main || against != "main" {
		t.Fatalf("measured from %s (%s), want main's %s", base, against, main)
	}
	if changed, err := changedFiles(base); err != nil || !slices.Equal(changed, []string{"b.txt"}) {
		t.Fatalf("changed %v, %v", changed, err)
	}
	gitIn(t, repo, "checkout", "-q", "main")
	head, _ := git("rev-parse", "HEAD")
	if base, against, err := baseCommit(); err != nil || base != head || against != "HEAD" {
		t.Fatalf("on main: %s (%s), %v", base, against, err)
	}
}

// A browser check that fails is named with its FAILED line, run once more
// (one that passes then is flaky) and, failing again, run on the unchanged
// code: one that fails there too is not the change's. Only a check that
// fails with the change and passes without it fails validate. Until
// 2026-10-05 the summary showed the end of the log, the checks that passed,
// and telling a failure the unchanged code has too took sessions minutes
// (20261004T032020Z-442c04d4, with a wrong diagnosis).
func TestFailingBrowserChecksAreNamedAndSorted(t *testing.T) {
	out := `== screens
step: home
no page errors
== screens: ok in 9.1s
== approve
step: card shown
FAILED Error: no the switch offered after 10000 ms
    at until (lib.js:177:33)
== approve: FAILED (exit 1) in 14.5s
== models
FAILED Error: models.js:300 the task did not load
== models: FAILED (exit 1) in 21.0s
== remote
FAILED AssertionError: the pairing code
== remote: FAILED (exit 1) in 3.0s
`
	failed := failedChecks(out)
	if !slices.Equal(failed, []string{"approve", "models", "remote"}) {
		t.Fatalf("failed: %v", failed)
	}
	why := failureLines(out, failed)
	for _, want := range []string{"approve: FAILED Error: no the switch offered", "models: FAILED Error: models.js:300", "remote: FAILED AssertionError"} {
		if !strings.Contains(why, want) {
			t.Errorf("the failure lines lack %q:\n%s", want, why)
		}
	}
	// approve fails again and passes without the change: the change's.
	// models passed when run again: flaky. remote fails without the
	// change too.
	r := browserVerdict(failed, []string{"approve", "remote"}, []string{"remote"}, why)
	if r.status != "FAIL" || !strings.Contains(r.detail, "fail with the change and pass on the unchanged code: approve") ||
		!strings.Contains(r.detail, "(flaky): models") || !strings.Contains(r.detail, "not because of the change: remote") || r.tail != why {
		t.Fatalf("verdict: %+v", r)
	}
	// None of them the change's: a warning to answer, not a failure.
	if r := browserVerdict([]string{"models", "remote"}, []string{"remote"}, []string{"remote"}, ""); r.status != "WARN" || !strings.Contains(r.detail, "Say so in your answer") {
		t.Fatalf("verdict: %+v", r)
	}
}

// A branch pushed with -u tracks its own copy, which is HEAD once pushed.
// Measured from that, validate said "nothing to run" for a whole unchecked
// branch; the main line is what such a branch is measured from, and an
// upstream that is a main line (origin/main) still is the base.
func TestAPushedBranchIsStillMeasuredFromTheMainLine(t *testing.T) {
	repo := fixture(t, map[string]string{"a.txt": "a\n"})
	gitIn(t, repo, "checkout", "-q", "-b", "feature")
	writeAll(t, repo, map[string]string{"b.txt": "b\n"})
	gitIn(t, repo, "add", "-A")
	gitIn(t, repo, "commit", "-q", "-m", "the work")
	// A remote and its copy of the branch, equal to HEAD, as after a push.
	gitIn(t, repo, "config", "remote.origin.url", ".")
	gitIn(t, repo, "config", "remote.origin.fetch", "+refs/heads/*:refs/remotes/origin/*")
	gitIn(t, repo, "update-ref", "refs/remotes/origin/feature", "HEAD")
	gitIn(t, repo, "branch", "-q", "--set-upstream-to=origin/feature")
	t.Chdir(repo)
	main, _ := git("rev-parse", "main")
	base, against, err := baseCommit()
	if err != nil || base != main || against != "main" {
		t.Fatalf("pushed branch measured from %s (%s), %v; want main's %s", base, against, err, main)
	}
	if changed, err := changedFiles(base); err != nil || !slices.Equal(changed, []string{"b.txt"}) {
		t.Fatalf("changed %v, %v", changed, err)
	}
	// An upstream that is the main line is still the base: work committed
	// and not pushed to it counts, work already in it does not.
	gitIn(t, repo, "update-ref", "refs/remotes/origin/main", "main")
	gitIn(t, repo, "branch", "-q", "--set-upstream-to=origin/main")
	if base, against, err := baseCommit(); err != nil || base != main || against != "origin/main" {
		t.Fatalf("tracking origin/main: %s (%s), %v", base, against, err)
	}
	// With no main line at all the upstream is the best base there is.
	gitIn(t, repo, "branch", "-q", "-m", "main", "trunk")
	gitIn(t, repo, "update-ref", "-d", "refs/remotes/origin/main")
	gitIn(t, repo, "branch", "-q", "--set-upstream-to=origin/feature")
	head, _ := git("rev-parse", "HEAD")
	if base, against, err := baseCommit(); err != nil || base != head || against != "origin/feature" {
		t.Fatalf("no main line: %s (%s), %v", base, against, err)
	}
}

// A change that removes a test, an exfil scenario or a browser check, or
// edits the guard, the scenarios or the exfil tests, is said, since each
// makes make accept smaller or different without failing it. New files and
// changes elsewhere are not said, and a test that moved to another file is
// not removed.
func TestWeakeningTheChecksIsSaid(t *testing.T) {
	repo := fixture(t, map[string]string{
		"internal/guard/guard.go":         "package guard\n",
		"internal/core/a_test.go":         "package core\n\nimport \"testing\"\n\nfunc TestKept(t *testing.T) {}\n\nfunc TestGone(t *testing.T) {}\n\nfunc TestMoved(t *testing.T) {}\n",
		"internal/core/b_test.go":         "package core\n\nimport \"testing\"\n\nfunc TestOther(t *testing.T) {}\n",
		"test/exfil/scenarios/012-x.scm":  ";; expect: confirm\n",
		"test/exfil/scenarios/013-y.scm":  ";; expect: confirm\n",
		"test/browser/checks/screens.js":  "// check\n",
		"internal/prim/unrelated.go":      "package prim\n",
		"internal/prim/unrelated_test.go": "package prim\n\nimport \"testing\"\n\nfunc TestUnrelated(t *testing.T) {}\n",
	})
	writeAll(t, repo, map[string]string{
		"internal/guard/guard.go":          "package guard\n\nvar loosened = true\n",
		"internal/core/a_test.go":          "package core\n\nimport \"testing\"\n\nfunc TestKept(t *testing.T) {}\n",
		"internal/core/b_test.go":          "package core\n\nimport \"testing\"\n\nfunc TestOther(t *testing.T) {}\n\nfunc TestMoved(t *testing.T) {}\n",
		"test/exfil/scenarios/013-y.scm":   ";; expect: allow\n",
		"test/exfil/scenarios/020-new.scm": ";; expect: deny\n",
		"internal/prim/unrelated.go":       "package prim\n\nvar x = 1\n",
	})
	for _, p := range []string{"test/exfil/scenarios/012-x.scm", "test/browser/checks/screens.js"} {
		if err := os.Remove(filepath.Join(repo, p)); err != nil {
			t.Fatal(err)
		}
	}
	t.Chdir(repo)
	base, _, err := baseCommit()
	if err != nil {
		t.Fatal(err)
	}
	changed, err := changedFiles(base)
	if err != nil {
		t.Fatal(err)
	}
	got := weakened(base, changed)
	if len(got) != 2 || got[0].name != "removed checks" || got[1].name != "guarded code" {
		t.Fatalf("weakened = %+v", got)
	}
	for _, want := range []string{"test internal/core.TestGone", "test/exfil/scenarios/012-x.scm", "test/browser/checks/screens.js"} {
		if !strings.Contains(got[0].detail, want) {
			t.Errorf("removed checks lack %q: %s", want, got[0].detail)
		}
	}
	for _, not := range []string{"TestMoved", "TestKept", "020-new", "unrelated"} {
		if strings.Contains(got[0].detail+got[1].detail, not) {
			t.Errorf("%q is said though nothing was lost there: %+v", not, got)
		}
	}
	for _, want := range []string{"internal/guard/guard.go", "test/exfil/scenarios/013-y.scm"} {
		if !strings.Contains(got[1].detail, want) {
			t.Errorf("guarded code lacks %q: %s", want, got[1].detail)
		}
	}
	if got[0].status != "WARN" || got[1].status != "WARN" {
		t.Errorf("weakening is a warning to answer: %+v", got)
	}
	// docRules reports them with the rest.
	var names []string
	for _, r := range docRules(base, changed) {
		names = append(names, r.name)
	}
	if !slices.Contains(names, "removed checks") || !slices.Contains(names, "guarded code") {
		t.Errorf("docRules = %v", names)
	}
}

// The validate tool's description is what a model reads to decide what a
// PASS means; it said validate builds the books, which the release flow
// does (ADR 37, amended 2026-10-07).
func TestTheToolDescriptionDoesNotClaimTheBooksAreBuilt(t *testing.T) {
	raw, err := os.ReadFile("../../.nodwyn/init.scm")
	if err != nil {
		t.Fatal(err)
	}
	doc := string(raw)
	i := strings.Index(doc, `(register-tool "validate"`)
	if i < 0 {
		t.Fatal("init.scm registers no validate tool")
	}
	doc = doc[i:]
	doc = doc[:strings.Index(doc, "(params")]
	for _, banned := range []string{"book and expert guide for their sources", "builds the book"} {
		if strings.Contains(doc, banned) {
			t.Errorf("the tool's description says %q", banned)
		}
	}
	if !strings.Contains(doc, "does not build the book") {
		t.Errorf("the description should say the books are not built: %s", doc)
	}
}

// Every browser check is run by run.sh with no arguments and described in
// the README: the lists drifted (the README lacked schedule, retrieval and
// conversation-search, AGENTS.md six checks) until a session ran only the
// checks it was told of and missed the one that covers its change.
func TestEveryBrowserCheckIsRunAndDescribed(t *testing.T) {
	files, err := filepath.Glob("../../test/browser/checks/*.js")
	if err != nil || len(files) == 0 {
		t.Fatalf("no browser checks found: %v", err)
	}
	runSh, err := os.ReadFile("../../test/browser/run.sh")
	if err != nil {
		t.Fatal(err)
	}
	readme, err := os.ReadFile("../../test/browser/README.md")
	if err != nil {
		t.Fatal(err)
	}
	agents, err := os.ReadFile("../../AGENTS.md")
	if err != nil {
		t.Fatal(err)
	}
	var all string
	for _, line := range strings.Split(string(runSh), "\n") {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(line), `chromium) all="`); ok {
			all, _, _ = strings.Cut(rest, `"`)
		}
	}
	ran := strings.Fields(all)
	for _, f := range files {
		name := strings.TrimSuffix(filepath.Base(f), ".js")
		if !slices.Contains(ran, name) {
			t.Errorf("run.sh does not run the check %s", name)
		}
		if !strings.Contains(string(readme), "| `"+name+"` |") {
			t.Errorf("test/browser/README.md has no row for the check %s", name)
		}
	}
	if len(ran) != len(files) {
		t.Errorf("run.sh runs %d checks and %d files are in checks/: %v", len(ran), len(files), ran)
	}
	// AGENTS.md sends a session to the directory, not to a list that drifts.
	if !strings.Contains(string(agents), "test/browser/checks/") {
		t.Error("AGENTS.md does not point at test/browser/checks/")
	}
}

// A test line the change adds that drops the error of its own setup is
// said; the same line already in the repository, a checked call and a
// helper's call are not. The exfil harness had seven such lines, and its
// scenarios stayed green with the canary file never written.
func TestUncheckedSetupIsSaidForAddedLines(t *testing.T) {
	old := "package x\n\nimport (\n\t\"os\"\n\t\"testing\"\n)\n\nfunc TestOld(t *testing.T) {\n\tos.WriteFile(\"old\", nil, 0o644)\n}\n"
	repo := fixture(t, map[string]string{"x/x_test.go": old})
	writeAll(t, repo, map[string]string{
		"x/x_test.go": old + "\nfunc TestNew(t *testing.T) {\n\tos.MkdirAll(\"d\", 0o755)\n\tif err := os.WriteFile(\"f\", nil, 0o644); err != nil {\n\t\tt.Fatal(err)\n\t}\n\tmust.WriteFile(t, \"g\", nil, 0o644)\n\tos.Symlink(\"a\", \"b\")\n}\n",
		"x/y_test.go": "package x\n\nfunc helper() {\n\tos.Chmod(\"f\", 0o600)\n}\n",
		"x/z.go":      "package x\n\nfunc z() {\n\tos.WriteFile(\"not a test\", nil, 0o644)\n}\n",
	})
	t.Chdir(repo)
	base, _, err := baseCommit()
	if err != nil {
		t.Fatal(err)
	}
	changed, err := changedFiles(base)
	if err != nil {
		t.Fatal(err)
	}
	got := uncheckedSetup(base, changed)
	if len(got) != 1 || got[0].status != "WARN" || got[0].name != "unchecked setup" {
		t.Fatalf("uncheckedSetup = %+v", got)
	}
	for _, want := range []string{"x/x_test.go:13", "x/x_test.go:18", "x/y_test.go:4"} {
		if !strings.Contains(got[0].detail, want) {
			t.Errorf("the warning lacks %s: %s", want, got[0].detail)
		}
	}
	for _, not := range []string{"x/x_test.go:9", "x/x_test.go:14", "x/x_test.go:17", "z.go"} {
		if strings.Contains(got[0].detail, not) {
			t.Errorf("the warning names %s, which is checked, old or no test: %s", not, got[0].detail)
		}
	}
}
