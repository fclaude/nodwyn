package core

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	fake "github.com/fclaude/nodwyn/test/fakes/ollama"
)

// What the default loop asks when a change meets an existing test, second
// round (ADR 32, amended 2026-10-16, the original tests). Two benchmark misses
// of dsl21, both reproduced by reviewers. 5040f9fe: the request added the
// response's Content-Type to an error; the model reworded the error so that a
// phrase an existing test pins was split, and edited that test's
// `want := "..."` to match: nothing named a want string, and every run of its
// own passed, while the original test fails on the patch. 855e90e7: an old
// test failed after a correct change because its fixture used one folder as
// repository, configuration and data directory; the model added a skip to
// production code, and the finish review then asked for a test of the skip,
// and for revert experiments on it (53 calls).

// msgModule is a Go module whose Msg returns the phrase an existing test
// pins, as readResponses' error did.
func msgModule(t *testing.T, test string) string {
	t.Helper()
	repo := gitWorkTree(t)
	commitFiles(t, repo, map[string]string{
		"go.mod":            "module example.com/m\n\ngo 1.22\n",
		"calc/calc.go":      "package calc\n\nfunc Msg() string { return \"stream ended without a terminal response\" }\n",
		"calc/calc_test.go": test,
	})
	return repo
}

// msgTest is the existing test, with the phrase it pins in a want variable.
const msgTest = "package calc\n\nimport (\n\t\"strings\"\n\t\"testing\"\n)\n\n" +
	"func TestMsg(t *testing.T) {\n\twant := \"stream ended without a terminal response\"\n\tif !strings.Contains(Msg(), want) {\n\t\tt.Fatal(Msg())\n\t}\n}\n"

// pinnedTest is the same test with the phrase as a row of a slice.
const pinnedTest = "package calc\n\nimport (\n\t\"strings\"\n\t\"testing\"\n)\n\n" +
	"var pinned = []string{\n\t\"stream ended without a terminal response\",\n}\n\n" +
	"func TestMsg(t *testing.T) {\n\tfor _, want := range pinned {\n\t\tif !strings.Contains(Msg(), want) {\n\t\t\tt.Fatal(Msg())\n\t\t}\n\t}\n}\n"

const (
	// the model's reworded error: the phrase is split
	rewordedMsg = "package calc\n\nfunc Msg() string { return \"stream with Content-Type \\\"\\\" ended without a terminal response\" }\n"
	// the phrase appended to: the original test still passes
	appendedMsg = "package calc\n\nfunc Msg() string { return \"stream ended without a terminal response, Content-Type \\\"\\\"\" }\n"
	// the test as the model edits it to pass the reworded error
	editedWant = "package calc\n\nimport (\n\t\"strings\"\n\t\"testing\"\n)\n\n" +
		"func TestMsg(t *testing.T) {\n\twant := \"ended without a terminal response\"\n\tif !strings.Contains(Msg(), want) {\n\t\tt.Fatal(Msg())\n\t}\n}\n"
	// pinnedTest with its row moved into a direct check and a test added: the
	// phrase it pins is the same, and the original test still passes
	addedTest = "package calc\n\nimport (\n\t\"strings\"\n\t\"testing\"\n)\n\n" +
		"func TestMsg(t *testing.T) {\n\tif !strings.Contains(Msg(), \"stream ended without a terminal response\") {\n\t\tt.Fatal(Msg())\n\t}\n}\n\n" +
		"func TestMsgNamesTheContentType(t *testing.T) {\n\tif !strings.Contains(Msg(), \"Content-Type\") {\n\t\tt.Fatal(Msg())\n\t}\n}\n"
)

const originalTestsHead = "These original tests fail on your change (the last commit's version of each test file you edited, run against your code): "

// The finish review runs the test files the turn edited as the last commit
// had them against the change and names the tests that fail, with the way
// out: change the code, appending to what a test pins, or change the setup
// and cite the request. Here the turn's own run passes (the test was edited
// to match), as on 5040f9fe. The question about the edited want line was
// asked at the edit as well, since a want string is an expectation now.
func TestTheReviewNamesOriginalTestsTheChangeBreaks(t *testing.T) {
	t.Parallel()
	r := unattendedRigIn(t, msgModule(t, msgTest),
		write("w", "calc/calc.go", rewordedMsg),
		write("t", "calc/calc_test.go", editedWant),
		shellArgs("r", "go", "test", "./calc/"),
		fake.Turn{Content: "done"}, // the review: TestMsg fails as the last commit had it
		fake.Turn{Content: "I append to the phrase instead."},
	)
	if e := r.turn("add the response's Content-Type to the error"); e != "" {
		t.Fatal(e)
	}
	if got := r.results()["r"]; got != "" {
		t.Fatalf("the turn's own run failed: %s", got)
	}
	if len(r.srv.Requests) != 5 {
		t.Fatalf("%d requests, want 5; last message %q", len(r.srv.Requests), lastMessage(r, len(r.srv.Requests)-1))
	}
	if msg := lastMessage(r, 2); !strings.Contains(msg, editedLines+" (the line numbers are the last commit's):\n- calc/calc_test.go:9: want := \"stream ended without a terminal response\"") {
		t.Errorf("the edited want line was not asked about at the edit: %q", msg)
	}
	msg := lastMessage(r, 4)
	for _, want := range []string{originalTestsHead + "TestMsg (calc/calc_test.go).",
		"Either your change is wrong (change the code, appending to what a test pins rather than rewording it); or the request names the new result for that input or case",
		"and the test's assertion changes: say which sentence of the request requires it; or the request contradicts the test's setup (a fixture): then change the setup, not its assertions, and say which sentence of the request requires it.",
		"Your next reply without tool calls ends the turn"} {
		if !strings.Contains(msg, want) {
			t.Errorf("the review lacks %q: %q", want, msg)
		}
	}
	if n := requestsMention(r, originalTestsHead); n != 1 {
		t.Errorf("%d requests carry the original tests point, want 1", n)
	}
	if got, _ := os.ReadFile(filepath.Join(r.repo, "calc", "calc_test.go")); string(got) != editedWant {
		t.Errorf("the test file was not left as the model made it:\n%s", got)
	}
}

// Nothing is said when the original tests pass: here the turn appends to the
// phrase the test pins, moves the row that pins it into a direct check (the
// same literal, so no expectation is removed, as the replay of 94f5cac4 named
// one that had only moved) and adds a test of its own, which the last commit
// does not declare, so it is not run against the change as one of the
// originals, whatever it does.
func TestTheReviewIsQuietWhenTheOriginalTestsPass(t *testing.T) {
	t.Parallel()
	r := unattendedRigIn(t, msgModule(t, pinnedTest),
		write("w", "calc/calc.go", appendedMsg),
		write("t", "calc/calc_test.go", addedTest),
		write("n", "calc/own_test.go", "package calc\n\nimport \"testing\"\n\nfunc TestOwnFile(t *testing.T) { t.Log(Msg()) }\n"),
		shellArgs("r", "go", "test", "./calc/"),
		fake.Turn{Content: "done"},                       // the review names the new file
		fake.Turn{Content: "own_test.go is mine; kept."}, // and ends
	)
	if e := r.turn("add the response's Content-Type to the error"); e != "" {
		t.Fatal(e)
	}
	if len(r.srv.Requests) != 6 {
		t.Fatalf("%d requests, want 6; last message %q", len(r.srv.Requests), lastMessage(r, len(r.srv.Requests)-1))
	}
	if msg := lastMessage(r, 5); !strings.Contains(msg, "git does not track them: calc/own_test.go") {
		t.Fatalf("the review: %q", msg)
	}
	if n := requestsMention(r, "original tests fail"); n != 0 {
		t.Fatalf("%d requests named original tests that pass", n)
	}
	if n := requestsMention(r, editedLines) + requestsMention(r, "removes or rewrites these expectations"); n != 0 {
		t.Fatalf("%d requests asked about a row that only moved into a direct check", n)
	}
}

// A turn that edits a test of another language is not asked: only Go's
// tests are run against the change, and the log says so, rather than a
// silent nothing.
func TestOriginalTestsAreRunForGoOnly(t *testing.T) {
	t.Parallel()
	repo := gitWorkTree(t)
	commitFiles(t, repo, map[string]string{
		"calc.py":      "def one():\n    return 1\n",
		"test_calc.py": "def test_one():\n    assert one() == 1\n",
	})
	r := unattendedRigIn(t, repo,
		write("w", "calc.py", "def one():\n    return 2\n"),
		write("t", "test_calc.py", "def test_one():\n    assert one() == 2\n"),
		shell("r", "true # pytest"),
		fake.Turn{Content: "done"},                       // the review names the removed assertion
		fake.Turn{Content: "The request changes one()."}, // and ends
	)
	if e := r.turn("make one return 2"); e != "" {
		t.Fatal(e)
	}
	if n := requestsMention(r, "original tests fail"); n != 0 {
		t.Fatalf("%d requests named original tests of a language that is not run", n)
	}
	var noted bool
	for _, n := range r.notes() {
		noted = noted || strings.Contains(n, "original-tests: not run, it runs Go tests only")
	}
	if !noted {
		t.Errorf("the log does not say the original tests were not run: %q", r.notes())
	}
}

// A want string beside a Contains check is an expectation: the question is
// asked as the edit is made, with the line as the last commit had it. Before,
// only the cases of a table and testify's asserts were (ADR 32, amended
// 2026-10-16, the original tests).
func TestAnEditedWantStringOfAnExistingTestIsAskedAbout(t *testing.T) {
	t.Parallel()
	repo := gitWorkTree(t)
	commitFiles(t, repo, map[string]string{
		"calc/msg_test.go": "package calc\n\nfunc TestMsg(t *testing.T) {\n\twant := \"Responses stream ended without a terminal response\"\n\tif !strings.Contains(err.Error(), want) {\n\t\tt.Fatal(err)\n\t}\n}\n",
	})
	r := unattendedRigIn(t, repo,
		write("t", "calc/msg_test.go", "package calc\n\nfunc TestMsg(t *testing.T) {\n\twant := \"ended without a terminal response\"\n\tif !strings.Contains(err.Error(), want) {\n\t\tt.Fatal(err)\n\t}\n}\n"),
		shell("r", "true # go test ./..."),
		fake.Turn{Content: "done"},
		fake.Turn{Content: "The request does not require it; restored."},
	)
	if e := r.turn("add the response's Content-Type to the error"); e != "" {
		t.Fatal(e)
	}
	msg := lastMessage(r, 1)
	for _, want := range []string{editedLines + " (the line numbers are the last commit's):\n- calc/msg_test.go:4: want := \"Responses stream ended without a terminal response\"",
		"Which sentence of the request requires the new expectation, for that input or case?",
		"to add to what a test pins, append to the phrase it checks; do not reword it"} {
		if !strings.Contains(msg, want) {
			t.Errorf("the question lacks %q: %q", want, msg)
		}
	}
}

// The check check-without takes apart is the
// passing command that runs the most of what the turn changed, not the last
// passing one. On 855e90e7 the last was the exfiltration suite alone: check-
// without named four hunks no test of it ran, and the run spent 53 calls on
// revert experiments. Here a command that runs the changed package passes,
// then one that runs another package: the first is the check, which fails
// without the change, so nothing is named.
func TestTheReviewTakesApartTheBroadestPassingCheck(t *testing.T) {
	t.Parallel()
	const changed = "package calc\n\nfunc evalOr(p string) string { return p }\n\nfunc Root(p string) string { return evalOr(p) + \"x\" }\n"
	r := unattendedRigIn(t, goRepo(t),
		write("w", "calc/calc.go", changed),
		shellArgs("a", "bash", "-c", "grep -q 'evalOr(p) + \"x\"' calc/calc.go # go test ./calc/"), // the changed package's tests
		shellArgs("b", "bash", "-c", "true # go test ./test/exfil/"),                               // another package's, last
		fake.Turn{Content: "done"},
	)
	if e := r.turn("make Root add an x to its path"); e != "" {
		t.Fatal(e)
	}
	if len(r.srv.Requests) != 4 {
		t.Fatalf("%d requests, want 4; last message %q", len(r.srv.Requests), lastMessage(r, len(r.srv.Requests)-1))
	}
	if n := requestsMention(r, "still passes with each of these parts"); n != 0 {
		t.Fatalf("%d requests named parts the changed package's tests cover", n)
	}
}

// With nothing else to choose by, the last passing command stays the check.
func TestTheLastOfEqualPassingChecksIsTheOneTakenApart(t *testing.T) {
	t.Parallel()
	const changed = "package calc\n\nfunc evalOr(p string) string { return p }\n\nfunc Root(p string) string { return evalOr(p) + \"x\" }\n"
	r := unattendedRigIn(t, goRepo(t),
		write("w", "calc/calc.go", changed),
		shellArgs("a", "bash", "-c", "true # go test ./elsewhere/"),
		shellArgs("b", "bash", "-c", "true # go test ./test/exfil/"),
		fake.Turn{Content: "done"},
		fake.Turn{Content: "The check does not cover Root."},
	)
	if e := r.turn("make Root add an x to its path"); e != "" {
		t.Fatal(e)
	}
	if msg := lastMessage(r, len(r.srv.Requests)-1); !strings.Contains(msg, "Your check (bash -c true # go test ./test/exfil/) still passes with each of these parts") {
		t.Fatalf("the last of equal checks is not the one taken apart: %q", msg)
	}
}

// failingTests prints what go test prints for a test that failed.
func failingTest(id, name string, n int) fake.Turn {
	return shell(id, "echo '--- FAIL: "+name+" (0.00s)'; echo FAIL; exit 1 # go test ./calc/ -run "+string(rune('a'+n)))
}

// Runs that fail with a test failing are runs of tests: six in a row are not
// "test commands that did not run successfully", the way out for a broken
// environment. On 855e90e7 six revert experiments in a row were told so
// (ADR 32, amended 2026-10-16, the original tests).
func TestFailingTestsAreNotCommandsThatDidNotRun(t *testing.T) {
	t.Parallel()
	turns := []fake.Turn{writeFile("w")}
	for i := 0; i < 6; i++ {
		turns = append(turns, failingTest("t"+string(rune('a'+i)), "TestScratchSearch", i))
	}
	turns = append(turns, fake.Turn{Content: "done"}, fake.Turn{Content: "done"}) // told the last run failed, then ends
	r := unattendedRig(t, turns...)
	if e := r.turn("fix it"); e != "" {
		t.Fatal(e)
	}
	if len(r.srv.Requests) != 9 {
		t.Fatalf("%d requests, want 9", len(r.srv.Requests))
	}
	if n := requestsMention(r, "did not run successfully"); n != 0 {
		t.Fatalf("%d requests said the tests did not run, after runs that failed tests", n)
	}
}

// A failing run after an edit that took a part of the model's own change out
// is a revert experiment, expected to fail, though its output names no test
// (the run was cut with tail, or the package did not build without the
// part). Runs of them in a row are no sign that tests do not run. The way out
// is offered after two failures in these tests, not six, to keep them short:
// they ran for over a minute under make accept's load once.
const (
	twoUnrunnable = "(set! unrunnable-after 2)\n"
	grownCalc     = "package calc\n\nfunc evalOr(p string) string { return p }\n\nfunc Root(p string) string { return evalOr(p) }\n\nfunc A() int { return 1 }\n\nfunc B() int { return 2 }\n"
	shrunkCalc    = "package calc\n\nfunc evalOr(p string) string { return p }\n\nfunc Root(p string) string { return evalOr(p) }\n\nfunc A() int { return 1 }\n"
)

func TestRevertExperimentsAreNotCommandsThatDidNotRun(t *testing.T) {
	t.Parallel()
	turns := []fake.Turn{write("g", "calc/calc.go", grownCalc), shell("first", "true # go test ./calc/")}
	for i := 0; i < 2; i++ {
		id := string(rune('a' + i))
		turns = append(turns,
			write("s"+id, "calc/calc.go", shrunkCalc), // the part taken out
			shell("t"+id, "exit 1 # go test ./calc/ -run Case"+id),
			write("r"+id, "calc/calc.go", grownCalc), // and put back
		)
	}
	turns = append(turns, shell("ok", "true # go test ./calc/"),
		fake.Turn{Content: "done"},                                  // the review: the check passes without the new functions
		fake.Turn{Content: "A and B are the change; no check yet."}) // and ends
	r := trustedRigIn(t, goRepo(t), twoUnrunnable, turns...)
	if e := r.turn("add A and B"); e != "" {
		t.Fatal(e)
	}
	if n := requestsMention(r, "did not run successfully"); n != 0 {
		t.Fatalf("%d requests said the tests did not run, after revert experiments", n)
	}
}

// Edits that add to the change, each followed by a run that fails without a
// sign that tests ran, are what the way out is for: they count as before.
func TestGrowingEditsFollowedByFailingRunsStillCountAsUnrunnable(t *testing.T) {
	t.Parallel()
	turns := []fake.Turn{write("g", "calc/calc.go", shrunkCalc), shell("first", "true # go test ./calc/")}
	body := shrunkCalc
	for i := 0; i < 2; i++ {
		body += "\nfunc C" + string(rune('a'+i)) + "() int { return 3 }\n" // the change only grows
		turns = append(turns, write("w"+string(rune('a'+i)), "calc/calc.go", body), shell("t"+string(rune('a'+i)), "exit 4 # go test ./calc/ -run Case"+string(rune('a'+i))))
	}
	turns = append(turns, fake.Turn{Content: "The tests cannot run here; the change is unverified."})
	r := trustedRigIn(t, goRepo(t), twoUnrunnable, turns...)
	if e := r.turn("add A and B"); e != "" {
		t.Fatal(e)
	}
	if n := requestsMention(r, "did not run successfully"); n != 1 {
		t.Fatalf("%d requests said the tests did not run, want 1", n)
	}
}

// The review's point on a part the check does not test also offers a way out
// for a skip the request does not ask for: take it out, or cite the request.
func TestTheUntestedPartsPointDoesNotTestASkipIntoPlace(t *testing.T) {
	t.Parallel()
	r := unattendedRigIn(t, codeRepo(t),
		write("c", "$TMPDIR/check.sh", "grep -q 'return 10' calc.py\n"),
		write("w", "calc.py", "def one():\n    return 10\n\n\ndef two():\n    return 20\n"),
		shellArgs("t", "bash", "$TMPDIR/check.sh"),
		fake.Turn{Content: "done"},
		fake.Turn{Content: "two() is part of the change."},
	)
	if e := r.turn("make one return 10 and two return 20"); e != "" {
		t.Fatal(e)
	}
	msg := lastMessage(r, len(r.srv.Requests)-1)
	for _, want := range []string{"A part the request does not ask for (a flag, a rename, a refactor, a skip or an exception added to make an existing test pass) gets no test: remove it, or defend it in your report with the sentence of the request that requires it.",
		"do not test anything else because of this message"} {
		if !strings.Contains(msg, want) {
			t.Errorf("the point lacks %q: %q", want, msg)
		}
	}
}

// The command that runs the most of the changed directories is the check,
// whatever order they ran in: ./... and the whole-check words name them all, a
// run narrowed to some tests runs less than the package it narrows, and
// of equals the newest stands. A turn with no passing command has none.
func TestBroadestPassingPicksTheRunThatRunsTheMostOfTheChange(t *testing.T) {
	t.Parallel()
	r := newRig(t, true)
	const call = `(define (c line) (list (cons 'name "shell-exec") (cons 'id "x") (cons 'args (json-emit (list (cons "cmd" "sh") (cons "args" (list "-c" line)))))))`
	if _, err := r.s.Eval(t.Context(), call); err != nil {
		t.Fatal(err)
	}
	for src, want := range map[string]string{
		// newest first
		`(command-line (broadest-passing (list (c "go test ./test/exfil/") (c "go test ./calc/") (c "go test ./util/")) '("calc/calc.go")))`: `"sh -c go test ./calc/"`,
		`(command-line (broadest-passing (list (c "go test ./calc/") (c "go test ./...")) '("calc/calc.go" "util/util.go")))`:                `"sh -c go test ./..."`,
		`(command-line (broadest-passing (list (c "go test ./calc/") (c "make test")) '("calc/calc.go" "util/util.go")))`:                    `"sh -c make test"`,
		`(command-line (broadest-passing (list (c "go test -run TestX ./calc/") (c "go test ./calc/")) '("calc/calc.go")))`:                  `"sh -c go test ./calc/"`,
		`(command-line (broadest-passing (list (c "go test ./calc/ ./util/") (c "go test ./calc/")) '("calc/calc.go" "util/util.go")))`:      `"sh -c go test ./calc/ ./util/"`,
		`(command-line (broadest-passing (list (c "go test ./b/") (c "go test ./a/")) '("calc/calc.go")))`:                                   `"sh -c go test ./b/"`,
		`(command-line (broadest-passing (list (c "pytest")) '()))`:                                                                          `"sh -c pytest"`,
		`(broadest-passing '() '("calc/calc.go"))`:                                                           `#f`,
		`(map path-dir '("a.go" "calc/calc.go" "internal/prim/x.go"))`:                                       `("" "calc" "internal/prim")`,
		`(changed-dirs '("calc/calc.go" "calc/calc_test.go" "calc/b.go" "docs/a.md" "util/u.go" "main.go"))`: `("calc" "util" "")`,
	} {
		v, err := r.s.Eval(t.Context(), src)
		if err != nil {
			t.Fatal(src, err)
		}
		if got := v.String(); got != want {
			t.Errorf("%s = %s, want %s", src, got, want)
		}
	}
}
