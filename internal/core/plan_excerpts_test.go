package core

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	fake "github.com/fclaude/nodwyn/test/fakes/ollama"
)

// excerptRepo holds the files of the excerpt tests: big.py (300 numbered
// lines), small.py (3), huge.txt (400 long lines, past one excerpt's cap),
// ranged.py (100 numbered lines) and unread.py.
func excerptRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	numbered := func(prefix string, n int) string {
		var b strings.Builder
		for i := 1; i <= n; i++ {
			fmt.Fprintf(&b, "%s_%03d = %d\n", prefix, i, i)
		}
		return b.String()
	}
	files := map[string]string{
		"big.py":    numbered("big_line", 300),
		"small.py":  "small_a = 1\nsmall_b = 2\nsmall_c = 3\n",
		"huge.txt":  strings.Repeat("huge_line padded to make the file long enough to pass one excerpt's cap\n", 400),
		"ranged.py": numbered("ranged_line", 100),
		"unread.py": numbered("unread_line", 20),
	}
	for name, text := range files {
		if err := os.WriteFile(filepath.Join(repo, name), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return repo
}

func exploring(id, path string, rng ...int) fake.Turn {
	args := map[string]any{"path": path}
	if len(rng) == 2 {
		args["start"], args["count"] = rng[0], rng[1]
	}
	return fake.Turn{ToolCalls: []fake.ToolCall{{ID: id, Name: "fs-read", Args: args}}}
}

// excerptReport is the explorer's report: it cites lines of two files the
// explorer read whole, a file it read in a range (one line inside the range,
// one outside), a long file and an unread one without lines, and a file it
// never read.
const excerptReport = "Facts: the change goes at `big.py:100-102` and `big.py:250`; it uses `small.py`. " +
	"`ranged.py:45` is the other site (`ranged.py:90` too). huge.txt is long. `unread.py:5` was not read."

// handedOver returns the harness message that carries the explorer's
// findings, from the first request that holds it: the work's first, after
// the explorer's requests and the planner's.
func handedOver(t *testing.T, r *rig) string {
	t.Helper()
	for i, req := range r.srv.Requests {
		for _, m := range requestMessages(t, req) {
			if strings.Contains(m.Content, "What a read-only look at the repository found") {
				if i < 6 {
					t.Fatalf("request %d already holds the findings: the explorer's and the planner's are 0 to 5", i)
				}
				return m.Content
			}
		}
	}
	t.Fatalf("the model doing the work was told nothing of what exploring found (%d requests)", len(r.srv.Requests))
	return ""
}

func excerptRig(t *testing.T, repo string) *rig {
	return unattendedRigIn(t, repo,
		exploring("a", "big.py"), exploring("b", "small.py"), exploring("c", "huge.txt"), exploring("d", "ranged.py", 40, 20),
		fake.Turn{Content: excerptReport}, // the explorer
		fake.Turn{Content: "1. Edit big.py."},
		fake.Turn{Content: "done"}, fake.Turn{Content: "done"}, fake.Turn{Content: "done"}, fake.Turn{Content: "done"},
	)
}

// The explorer's context is folded away when it answers, and the model doing
// the work read again what the explorer had read (commit-bench, dsl20r,
// 2026-10-16: 73 to 100% of its reads in eight of nine runs). The lines
// around the places the findings cite go to it with them, copied from what
// the explorer read, as the file has them.
func TestWhatTheExplorerReadGoesWithItsFindingsToTheModelDoingTheWork(t *testing.T) {
	t.Parallel()
	repo := excerptRepo(t)
	r := excerptRig(t, repo)
	if e := r.planned("change big.py"); e != "" {
		t.Fatal(e)
	}
	got := handedOver(t, r)
	if !strings.Contains(got, excerptReport) {
		t.Fatalf("the findings are not in the message:\n%s", got)
	}
	lines := func(prefix string, from, to int) string {
		var b strings.Builder
		for i := from; i <= to; i++ {
			fmt.Fprintf(&b, "%s_%03d = %d\n", prefix, i, i)
		}
		return strings.TrimSuffix(b.String(), "\n")
	}
	for _, want := range []string{
		"== big.py lines 98-114 ==\n" + lines("big_line", 98, 114) + "\n",
		"== big.py lines 248-262 ==\n" + lines("big_line", 248, 262) + "\n",
		"== small.py lines 1-3 ==\nsmall_a = 1\nsmall_b = 2\nsmall_c = 3\n",
		"== ranged.py lines 43-57 ==\n" + lines("ranged_line", 43, 57) + "\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the excerpts lack:\n%s\nin:\n%s", want, got)
		}
	}
	for _, not := range []string{
		"big_line_097", "big_line_115", "big_line_247", "big_line_263", // the margins end where they should
		"huge.txt lines", "huge_line", // named without a line and too long to carry whole
		"ranged_line_058", "ranged_line_090", // outside what the explorer read of it
		"unread.py lines", "unread_line", // never read
	} {
		if strings.Contains(got, not) {
			t.Errorf("the message holds %q, which it should not carry", not)
		}
	}
	if !strings.Contains(got, "fs-read with start and count") {
		t.Error("the message does not say how to read more of a file")
	}
}

// The excerpts are bounded: plan-excerpts-cap characters in all, an excerpt
// that does not fit left out and a shorter one after it kept, and none at 0.
func TestTheExcerptsAreBounded(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		cap  int
		have []string
		not  []string
	}{
		// big.py's first excerpt is 347 characters with its heading, its
		// second 296 and small.py's 61: room for the first and the third.
		{cap: 420, have: []string{"== big.py lines 98-114 ==", "== small.py lines 1-3 =="}, not: []string{"== big.py lines 248-262 ==", "== ranged.py"}},
		{cap: 70, have: []string{"== small.py lines 1-3 =="}, not: []string{"== big.py", "== ranged.py"}},
		{cap: 0, not: []string{"==", "The lines of those files"}},
	} {
		t.Run(fmt.Sprintf("cap %d", tc.cap), func(t *testing.T) {
			r := excerptRig(t, excerptRepo(t))
			r.eval(fmt.Sprintf("(define plan-excerpts-cap %d)", tc.cap))
			if e := r.planned("change big.py"); e != "" {
				t.Fatal(e)
			}
			got := handedOver(t, r)
			for _, want := range tc.have {
				if !strings.Contains(got, want) {
					t.Errorf("the message lacks %q:\n%s", want, got)
				}
			}
			for _, not := range tc.not {
				if strings.Contains(got, not) {
					t.Errorf("the message holds %q:\n%s", not, got)
				}
			}
			intro := r.evalInt("(string-length plan-excerpts-intro)")
			section := got[strings.Index(got, excerptReport)+len(excerptReport):]
			if len(section) > tc.cap+intro {
				t.Errorf("the excerpts are %d characters, past the cap %d and the introduction (%d)", len(section), tc.cap, intro)
			}
		})
	}
}

// With the default cap, a report that cites far more than fits gets the
// first places it cites, up to plan-excerpts-cap characters and not past it.
func TestAReportThatCitesMoreThanFitsGetsTheCapNotMore(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	var file, cites strings.Builder
	for i := 1; i <= 2400; i++ {
		fmt.Fprintf(&file, "wide_line_%04d = %d\n", i, i)
	}
	for i := 10; i <= 2400; i += 30 {
		fmt.Fprintf(&cites, "wide.py:%d ", i)
	}
	if err := os.WriteFile(filepath.Join(repo, "wide.py"), []byte(file.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	report := strings.TrimSpace("Places: " + cites.String())
	// The explorer reads the file in ranges, as a window shows no more of a
	// long one in a read.
	turns := []fake.Turn{}
	for start := 1; start <= 2400; start += 350 {
		turns = append(turns, exploring(fmt.Sprintf("r%d", start), "wide.py", start, 350))
	}
	turns = append(turns, fake.Turn{Content: report}, // the explorer
		fake.Turn{Content: "1. Edit wide.py."},
		fake.Turn{Content: "done"}, fake.Turn{Content: "done"}, fake.Turn{Content: "done"}, fake.Turn{Content: "done"})
	r := unattendedRigIn(t, repo, turns...)
	if e := r.planned("change wide.py"); e != "" {
		t.Fatal(e)
	}
	got := ""
	for _, m := range requestMessages(t, r.srv.Requests[len(r.srv.Requests)-1]) {
		if strings.Contains(m.Content, "What a read-only look at the repository found") {
			got = m.Content
		}
	}
	section := got[strings.Index(got, report)+len(report):]
	intro := r.evalInt("(string-length plan-excerpts-intro)")
	if capacity := r.evalInt("plan-excerpts-cap"); capacity != 12000 {
		t.Fatalf("the cap is %d, not the 12000 this test is written for", capacity)
	}
	if len(section) > 12000+intro || len(section) < 11000 {
		t.Errorf("the excerpts are %d characters: want most of the 12000 cap, and not past it (introduction %d)", len(section), intro)
	}
	if !strings.Contains(section, "wide_line_0010 = 10") || strings.Contains(section, "wide_line_2400") || strings.Contains(section, "wide_line_2000") {
		t.Error("the excerpts are not the first places the report cites")
	}
}

// The excerpts are a help: if making them fails, the findings and the plan
// stand, as they did before there were any. The same run without the failure
// is the control: it carries the excerpts.
func TestAFailureToMakeExcerptsLeavesTheFindingsAndThePlan(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		break_   string
		excerpts bool
	}{
		{"control", "", true},
		{"failing", `(define (plan-excerpts findings reads) (error "no excerpts today"))`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := excerptRig(t, excerptRepo(t))
			if tc.break_ != "" {
				r.eval(tc.break_)
			}
			if e := r.planned("change big.py"); e != "" {
				t.Fatal(e)
			}
			got := handedOver(t, r)
			if !strings.Contains(got, excerptReport) {
				t.Fatalf("the findings are not in the message:\n%s", got)
			}
			if has := strings.Contains(got, "== big.py lines 98-114 =="); has != tc.excerpts {
				t.Fatalf("excerpts in the message = %v, want %v:\n%s", has, tc.excerpts, got)
			}
		})
	}
}
