package prim

import (
	"strings"
	"testing"

	"github.com/fclaude/nodwyn/internal/scheme"
)

// adr is an ADR as HEAD has it, with its first amendment.
const adr = "# ADR 25: redaction\n\n## Amendment 2026-10-01: the first\n\nScan finds tokens.\n"

func claimKinds(cs []addedClaim) []string {
	var out []string
	for _, c := range cs {
		out = append(out, c.kind+" "+c.path+": "+c.why)
	}
	return out
}

// A sentence the change adds that reports a run stands on a run of the
// command it names that exited 0 in the turn, printing its figures among its
// last lines. On commit-bench three builds copied the task's "40 s (9.4M
// executions) found nothing" into ADR 25: two never fuzzed, and A's own
// 29.5M-execution run had passed 9.3M at 12 s (ADR 32, amended 2026-10-15).
func TestARunClaimStandsOnARunThatPrintedIt(t *testing.T) {
	now := adr + "\n## Amendment 2026-10-01: in order\n\nFuzzing `Scan` with an ordering check for 40 s\n(9.4M executions) found nothing.\n"
	c := fakeChange(map[string]string{"docs/adr/0025.md": adr}, map[string]string{"docs/adr/0025.md": now})
	fuzz := func(status int, last string) []claimRun {
		return []claimRun{{line: "bash -c go test -fuzz FuzzScan -fuzztime 40s ./internal/detect/l1/", status: status,
			output: "fuzz: elapsed: 12s, execs: 9289317 (692977/sec)\n" + last + "\nPASS\nok  \tgithub.com/x/l1\t41.0s\n"}}
	}
	for _, tc := range []struct {
		name string
		runs []claimRun
		want string
	}{
		{"no fuzz ran", nil, "no run of fuzz of yours passed this turn"},
		{"the fuzz failed", fuzz(1, "fuzz: elapsed: 40s, execs: 9412345 (235308/sec)"), "no run of fuzz of yours passed this turn"},
		{"another count", fuzz(0, "fuzz: elapsed: 40s, execs: 29527198 (738179/sec)"), "no passing run of yours printed 9.4M executions"},
		{"its count", fuzz(0, "fuzz: elapsed: 40s, execs: 9412345 (235308/sec)"), ""},
	} {
		got := claimKinds(changeClaims(c, "Fuzzing Scan for 40 s (9.4M executions) found nothing.", tc.runs, nil, nil))
		if tc.want == "" && len(got) != 0 || tc.want != "" && (len(got) != 1 || !strings.Contains(got[0], tc.want) || !strings.HasPrefix(got[0], "run docs/adr/0025.md")) {
			t.Errorf("%s: claims %q, want %q", tc.name, got, tc.want)
		}
	}
}

// A sentence that names several commands needs each to have passed, and its
// figures among what one of them printed: dsl16's "`make validate` passed
// with zero warnings and `make accept` passed" stood on both runs, made
// after it was written. A comment of Go or Scheme is read as a document is.
// What reads as a run but is not one is no claim: a rule ("a test that
// passes on both does not test the fix"), a value passed on, a caveat.
func TestRunClaimsNameEachCommandAndLeaveProseAlone(t *testing.T) {
	head := map[string]string{"docs/adr/0008.md": adr, "calc/calc.go": "package calc\n\nfunc One() int { return 1 }\n"}
	now := map[string]string{
		"docs/adr/0008.md": adr + "\n`make validate` passed with zero warnings and `make accept` passed.\n" +
			"A test that passes on both does not test the fix. The key is passed to the server.\n" +
			"Not run here: a fuzz of Scan, which the guard refused.\n",
		"calc/calc.go": "package calc\n\n// One is 10: TestOne passed on the change.\nfunc One() int { return 10 }\n",
	}
	c := fakeChange(head, now)
	validate := claimRun{line: "make validate", status: 0, output: "PASS  gofmt\nvalidate: PASS (0 warnings)\n"}
	accept := claimRun{line: "bash -c make accept 2>&1 | tail -n 3", status: 0, output: "ok  \tgithub.com/x/calc\t0.1s\n"}
	goTest := claimRun{line: "go test ./calc", status: 0, output: "ok  \tgithub.com/x/calc\t0.1s\n"}
	if got := claimKinds(changeClaims(c, "", []claimRun{validate, accept, goTest}, nil, nil)); len(got) != 0 {
		t.Errorf("claims %q with every command passed", got)
	}
	got := claimKinds(changeClaims(c, "", nil, nil, nil))
	if len(got) != 2 || !strings.Contains(got[0], "calc/calc.go: no run of a test of yours passed") || !strings.Contains(got[1], "docs/adr/0008.md: no run of make accept of yours passed") {
		t.Errorf("claims %q, want the comment's test and the ADR's make accept", got)
	}
	// validate runs the tests, so it stands for the comment's; make accept
	// still has not run.
	got = claimKinds(changeClaims(c, "", []claimRun{validate}, nil, nil))
	if len(got) != 1 || !strings.Contains(got[0], "docs/adr/0008.md: no run of make accept of yours passed") {
		t.Errorf("claims %q, want the ADR's make accept", got)
	}
	warned := claimRun{line: "make validate", status: 0, output: "validate: PASS (2 warnings)\n"}
	if got := claimKinds(changeClaims(c, "", []claimRun{warned, accept, goTest}, nil, nil)); len(got) != 1 || !strings.Contains(got[0], "printed 0 warnings") {
		t.Errorf("claims %q, want zero warnings that validate did not print", got)
	}
}

// An amendment date the file did not have stands on a command that printed
// it, or on the request: on commit-bench models guessed "the day after the
// last amendment" (dsl18m, 556d6d12) and "2026-10-03" (dsl19, 48f90ea8).
// Each date is named once. A date the request gives (in a session's name,
// 20261007T152356Z), a date the file had, and a date in a sentence that is
// not an amendment's are not named.
func TestAnAmendmentDateStandsOnACommandThatPrintedIt(t *testing.T) {
	now := adr + "\n## Amendment 2026-10-06: in order\n\nScan keeps order (ADR 25, amended 2026-10-06).\n" +
		"\n## Amendment 2026-10-01: again\n\nOn commit-bench (2026-10-14) it was slow.\n"
	c := fakeChange(map[string]string{"docs/adr/0025.md": adr}, map[string]string{"docs/adr/0025.md": now})
	got := claimKinds(changeClaims(c, "", nil, nil, nil))
	if len(got) != 1 || !strings.Contains(got[0], "no command of yours printed 2026-10-06") || !strings.Contains(got[0], "date +%F") {
		t.Fatalf("claims %q, want 2026-10-06 once", got)
	}
	for name, backed := range map[string]struct {
		request string
		runs    []claimRun
	}{
		"date printed":        {"", []claimRun{{line: "date +%F", status: 0, output: "2026-10-06\n"}}},
		"git log printed":     {"", []claimRun{{line: "git log -1 --format=%cs", status: 0, output: "2026-10-06\n"}}},
		"in a scratch name":   {"", []claimRun{{line: "ls", status: 0, output: "/tmp/nodwyn/20261006T101010Z-ab\n"}}},
		"the request says so": {"Session 20261006T152356Z-a337f2a9 refused it.", nil},
	} {
		if got := changeClaims(c, backed.request, backed.runs, nil, nil); len(got) != 0 {
			t.Errorf("%s: claims %q", name, claimKinds(got))
		}
	}
}

// A document's edit that inserts three or more lines (120 characters or
// more) standing word for word elsewhere in it is named: on 5040f9fe one
// fs-edit put an amendment of ADR 9 in twice. Shorter runs, blank lines and
// code are not read for it.
func TestARepeatedParagraphIsNamed(t *testing.T) {
	para := "The reply is read as a stream when it opens with an event,\nand as JSON when its first byte opens an object, whatever\nits Content-Type says, as the plan server sends both.\n"
	head := map[string]string{
		"docs/adr/0009.md": "# ADR 9\n\n## Amendment 2026-10-01: streams\n\n" + para,
		"docs/short.md":    "# Short\n\na\nb\nc\n",
		"calc/calc.go":     "package calc\n\nfunc a() {\n\tx := 1\n\t_ = x\n}\n",
	}
	now := map[string]string{
		"docs/adr/0009.md": head["docs/adr/0009.md"] + "\n## Amendment 2026-10-01: streams\n\n" + para + "\nAnd one more line.\n",
		"docs/short.md":    head["docs/short.md"] + "\na\nb\nc\n",
		"calc/calc.go":     head["calc/calc.go"] + "\nfunc b() {\n\tx := 1\n\t_ = x\n}\n",
	}
	got := claimKinds(changeClaims(fakeChange(head, now), "", nil, nil, nil))
	if len(got) != 1 || !strings.Contains(got[0], "repeat docs/adr/0009.md: lines 11-13 repeat lines 5-7 word for word") {
		t.Fatalf("claims %q, want ADR 9's paragraph only", got)
	}
}

// Entries added by hand to a generated file stand on a command after the
// last hand edit that makes or fetches it: on dc679e46 four builds wrote
// Baseten's catalogue entries from memory into models.json, whose head
// names models.dev as its source. go generate, the generator its head
// names, the source's address or host back it; a file only a command wrote
// is no hand edit, and one whose head says nothing is not generated unless
// the repository declares it.
func TestHandEditsOfAGeneratedFileStandOnItsGenerator(t *testing.T) {
	head := map[string]string{
		"internal/catalogue/models.json": "{\"source\":\"https://models.dev/api.json\",\"fetched\":\"2026-10-05\",\"providers\":{\n\"a\":{}\n}}\n",
		"docs/book/source/catalogue.tex": "% Generated by docs/book/source/generate.py; edit the generator, not this inventory.\n456 files\n",
		"config/plain.json":              "{\"a\":1}\n",
		"config/declared.csv":            "a,1\n",
	}
	now := map[string]string{}
	for p, s := range head {
		now[p] = s + "more\n"
	}
	c := fakeChange(head, now)
	declared := map[string]string{"config/declared.csv": "make prices"}
	byHand := map[string][]string{"internal/catalogue/models.json": nil, "docs/book/source/catalogue.tex": {"go test ./..."}, "config/plain.json": nil, "config/declared.csv": nil}
	got := claimKinds(changeClaims(c, "", nil, byHand, declared))
	want := []string{
		"generated config/declared.csv: is generated by make prices",
		"generated docs/book/source/catalogue.tex: is generated by docs/book/source/generate.py",
		"generated internal/catalogue/models.json: says its data comes from https://models.dev/api.json",
	}
	if len(got) != len(want) {
		t.Fatalf("claims %q, want %q", got, want)
	}
	for i := range want {
		if !strings.HasPrefix(got[i], want[i]) {
			t.Errorf("claim %q, want %q", got[i], want[i])
		}
	}
	made := map[string][]string{
		"internal/catalogue/models.json": {"bash -c curl -s https://models.dev/api.json | head"},
		"docs/book/source/catalogue.tex": {"python3 docs/book/source/generate.py"},
		"config/declared.csv":            {"make prices"},
	}
	if got := changeClaims(c, "", nil, made, declared); len(got) != 0 {
		t.Errorf("claims %q after each file's generator or source ran", claimKinds(got))
	}
	if got := changeClaims(c, "", nil, nil, declared); len(got) != 0 {
		t.Errorf("claims %q with no file edited by hand", claimKinds(got))
	}
}

// (change-claims request runs edits declared) reads the working tree against
// HEAD and takes the turn's runs as the evidence loop keeps them.
func TestChangeClaimsReadsTheWorkingTree(t *testing.T) {
	root, k, git := keepRepo(t)
	write(t, root, "docs/adr/0001.md", adr, 0o644)
	git("add", ".")
	git("commit", "-q", "-m", "adr")
	write(t, root, "docs/adr/0001.md", adr+"\n## Amendment 2026-10-06: more\n\n`make validate` passed with 0 warnings.\n", 0o644)
	in := scheme.New()
	if err := in.Register(changeClaimsPrim(k.o)); err != nil {
		t.Fatal(err)
	}
	v, err := in.EvalString(t.Context(), `(change-claims "" '(("date +%F" 0 "2026-10-06\n") ("make validate" #f "")) '() '())`)
	if err != nil {
		t.Fatal(err)
	}
	got := v.String()
	if !strings.Contains(got, `("kind" . "run")`) || !strings.Contains(got, "no run of make validate of yours passed") || strings.Contains(got, `"date"`) {
		t.Errorf("change-claims: %s", got)
	}
}
