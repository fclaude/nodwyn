package main

import (
	"bytes"
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
// console needs the browser checks as well as make accept, and the browser
// checks' own scripts need only those. The books are documentation here:
// the release flow builds them. A Go or Scheme file, test or deleted, needs
// the books' source inventory checked: its counts include them all.
func TestPlanFollowsTheChangedFiles(t *testing.T) {
	t.Chdir("../..")
	for _, c := range []struct {
		changed []string
		want    []string
	}{
		{[]string{"AGENTS.md", "docs/adr/0001-scheme-on-go.md", "docs/manual.md"}, nil},
		{[]string{"docs/scheme.md"}, []string{"make accept"}},
		{[]string{"internal/core/session.go"}, []string{"gofmt", "book inventory", "make accept"}},
		{[]string{"internal/frontend/web/assets/home.js"}, []string{"node --check", "make accept", "browser checks"}},
		{[]string{"test/browser/checks/screens.js"}, []string{"node --check", "browser checks"}},
		{[]string{"internal/frontend/rpc/sessions.go"}, []string{"gofmt", "book inventory", "make accept", "browser checks"}},
		{[]string{"internal/frontend/rpc/server_test.go"}, []string{"gofmt", "book inventory", "make accept"}},
		{[]string{"docs/book/parts/scheme.tex", "docs/expert/parts/09-primitives.tex", "docs/book/source/catalogue.tex"}, nil},
		{[]string{"internal/core/deleted.go"}, []string{"book inventory", "make accept"}},
		{[]string{"internal/loop/strategy.scm"}, []string{"book inventory", "make accept"}},
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

// The books' source inventory is checked as a commit of the tree would
// hold it: a new file git does not track yet counts, and a deleted one does
// not, while git's own index, the object store and TMPDIR are left as they
// were. On commit-bench (2026-10-14) eight of twenty changes left the
// inventory stale and validate said PASS; on 3422faf1 the inventory had
// been regenerated with the new file untracked, so generate.py --check
// passed.
func TestTheBookInventoryIsCheckedWithNewFiles(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 is not installed")
	}
	gen, err := os.ReadFile(filepath.Join("..", "..", inventoryScript))
	if err != nil {
		t.Fatal(err)
	}
	repo := fixture(t, map[string]string{
		inventoryScript:    string(gen),
		"cmd/tool/main.go": "package main\n\nfunc main() {}\n",
		"dev/tool.scm":     "(define tool 1)\n",
	})
	t.Chdir(repo)
	generate := func() {
		t.Helper()
		if out, err := exec.Command("python3", inventoryScript).CombinedOutput(); err != nil {
			t.Fatalf("generate.py: %v\n%s", err, out)
		}
	}
	generate()
	gitIn(t, repo, "add", "-A")
	gitIn(t, repo, "commit", "-q", "-m", "the inventory")
	logs, scratch := t.TempDir(), t.TempDir()
	t.Setenv("TMPDIR", scratch)
	check := func() result {
		t.Helper()
		r := bookInventory(logs)
		if left, _ := os.ReadDir(scratch); len(left) > 0 {
			t.Fatalf("the check left %v in TMPDIR", left)
		}
		return r
	}
	if r := check(); r.status != "PASS" {
		t.Fatalf("a fresh inventory: %+v", r)
	}

	// A new file, regenerated without telling git, as on 3422faf1.
	writeAll(t, repo, map[string]string{"cmd/tool/extra.go": "package main\n\nvar extra = 1\n"})
	generate()
	index := filepath.Join(repo, ".git", "index")
	before, _ := os.ReadFile(index)
	objects, _ := git("count-objects")
	r := check()
	if r.status != "FAIL" || !strings.Contains(r.detail, "catalogue.tex is stale") ||
		!strings.Contains(r.detail, "git add -N cmd/tool/extra.go, then run python3 docs/book/source/generate.py") {
		t.Fatalf("an inventory without the new file: %+v", r)
	}
	if after, _ := os.ReadFile(index); !bytes.Equal(before, after) {
		t.Fatal("the check changed git's index")
	}
	if now, _ := git("count-objects"); now != objects {
		t.Fatalf("the check wrote objects: %s, then %s", objects, now)
	}
	if status, _ := git("status", "--porcelain"); !strings.Contains(status, "?? cmd/tool/extra.go") {
		t.Fatalf("the new file is no longer untracked: %q", status)
	}

	gitIn(t, repo, "add", "-N", "cmd/tool/extra.go")
	generate()
	if r := check(); r.status != "PASS" {
		t.Fatalf("an inventory with the new file: %+v", r)
	}

	// The file deleted again, with git still tracking it.
	if err := os.Remove(filepath.Join(repo, "cmd/tool/extra.go")); err != nil {
		t.Fatal(err)
	}
	if r := check(); r.status != "FAIL" || !strings.Contains(r.detail, "git rm --cached cmd/tool/extra.go, then run") {
		t.Fatalf("an inventory with a deleted file: %+v", r)
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
