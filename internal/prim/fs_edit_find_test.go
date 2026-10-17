package prim

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fclaude/nodwyn/internal/guard"
	nlog "github.com/fclaude/nodwyn/internal/log"
	"github.com/fclaude/nodwyn/internal/scheme"
)

// call runs a tool as a model's call, with its named JSON arguments.
func (h *harness) call(t *testing.T, tool, raw string) (string, error) {
	t.Helper()
	return h.callIn(t, context.Background(), tool, raw)
}

// callIn is call with the context the call runs in.
func (h *harness) callIn(t *testing.T, ctx context.Context, tool, raw string) (string, error) {
	t.Helper()
	p, ok := h.in.Lookup(tool)
	if !ok {
		t.Fatalf("no tool %s", tool)
	}
	args, err := p.ArgsFromJSON([]byte(raw))
	if err != nil {
		return "", err
	}
	v, err := h.in.Apply(scheme.WithOrigin(ctx, scheme.OriginModel), scheme.Value{Kind: scheme.KPrim, Prim: p}, args)
	return v.S, err
}

func (h *harness) put(t *testing.T, rel, text string) string {
	t.Helper()
	path := filepath.Join(h.root, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func (h *harness) read(t *testing.T, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(h.root, rel))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func numbered(n int, f func(i int) string) string {
	var b strings.Builder
	for i := 1; i <= n; i++ {
		b.WriteString(f(i) + "\n")
	}
	return b.String()
}

// An old that is nowhere in the file, even ignoring whitespace, gets the
// lines of the file closest to it, numbered, instead of the advice to read the
// region with a tool the two-tool configuration does not have (24 of the 73
// failed edits of its benchmark arms).
func TestFsEditNotFoundShowsTheClosestText(t *testing.T) {
	h := newHarness(t)
	h.answer = true
	h.put(t, "m.py", numbered(40, func(i int) string {
		if i == 21 {
			return "    result = compute_total(items, tax_rate, discount)"
		}
		return fmt.Sprintf("    filler_%d = %d", i, i)
	}))

	_, err := h.call(t, "fs-edit", `{"path":"m.py","old":"result = compute_total(items, tax)\nreturn result","new":"x"}`)
	if err == nil {
		t.Fatal("an edit that matches nowhere was applied")
	}
	for _, want := range []string{
		"old text not found",
		"the closest text is at lines 19-23",
		"21\t    result = compute_total(items, tax_rate, discount)",
		"19\t    filler_19 = 19",
		"23\t    filler_23 = 23",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error lacks %q:\n%v", want, err)
		}
	}
	if strings.Contains(err.Error(), "fs-read") {
		t.Errorf("the error names a tool that may not be offered:\n%v", err)
	}
	if strings.Count(err.Error(), "\n") != 5 {
		t.Errorf("the error shows %d lines, want 5:\n%v", strings.Count(err.Error(), "\n"), err)
	}

	// A single line gets the same.
	_, err = h.call(t, "fs-edit", `{"path":"m.py","old":"result = compute_total(items)","new":"x"}`)
	if err == nil || !strings.Contains(err.Error(), "the closest text is at lines 19-23") {
		t.Fatalf("a single-line miss: %v", err)
	}
	if h.read(t, "m.py") != numbered(40, func(i int) string {
		if i == 21 {
			return "    result = compute_total(items, tax_rate, discount)"
		}
		return fmt.Sprintf("    filler_%d = %d", i, i)
	}) {
		t.Fatal("a failed edit changed the file")
	}
}

// Nothing is shown for a text nothing in the file resembles, and the error
// says so; and the window of lines stays inside the file at its ends.
func TestFsEditNotFoundSaysWhenNothingResembles(t *testing.T) {
	h := newHarness(t)
	h.answer = true
	h.put(t, "m.py", "import os\nimport sys\n\ndef main():\n    return 0\n")

	_, err := h.call(t, "fs-edit", `{"path":"m.py","old":"class Frobnicator(Widget):\n    pass","new":"x"}`)
	if err == nil || !strings.Contains(err.Error(), "no line of the file resembles the first line of old (`class Frobnicator(Widget):`)") {
		t.Fatalf("unrelated old: %v", err)
	}
	if strings.Contains(err.Error(), "closest") || strings.Contains(err.Error(), "fs-read") {
		t.Fatalf("unrelated old: %v", err)
	}
	// One shared word is no resemblance.
	if _, err := h.call(t, "fs-edit", `{"path":"m.py","old":"import json","new":"x"}`); err == nil || !strings.Contains(err.Error(), "no line of the file resembles") {
		t.Fatalf("one word in common: %v", err)
	}
	// Near the top and the bottom the window moves instead of shrinking.
	_, err = h.call(t, "fs-edit", `{"path":"m.py","old":"import os.path\nimport sys","new":"x"}`)
	if err == nil || !strings.Contains(err.Error(), "the closest text is at lines 1-5") {
		t.Fatalf("top of the file: %v", err)
	}
	_, err = h.call(t, "fs-edit", `{"path":"m.py","old":"    return 0 + 1","new":"x"}`)
	if err == nil || !strings.Contains(err.Error(), "the closest text is at lines 1-5") || !strings.HasSuffix(err.Error(), "5\t    return 0") {
		t.Fatalf("a file of five lines: %v", err)
	}
}

// Of equally close lines the one whose following lines resemble the rest of
// old is shown, then the first.
func TestFsEditNotFoundBreaksTiesByWhatFollows(t *testing.T) {
	h := newHarness(t)
	h.answer = true
	h.put(t, "m.py", "class A:\n    def get(self, key):\n        a = 1\n\nclass B:\n    def get(self, key):\n        return self.data[key]\n")
	_, err := h.call(t, "fs-edit", `{"path":"m.py","old":"def get(self, key, d):\n    return self.data[key]","new":"x"}`)
	if err == nil || !strings.Contains(err.Error(), "old's first line resembles line 6") {
		t.Fatalf("the following lines did not decide: %v", err)
	}
	h.put(t, "n.py", "def put(self, key):\n    pass\ndef put(self, key):\n    pass\n")
	_, err = h.call(t, "fs-edit", `{"path":"n.py","old":"def put(self, key, v):","new":"x"}`)
	if err == nil || !strings.Contains(err.Error(), "old's first line resembles line 1") {
		t.Fatalf("the first of equal lines was not taken: %v", err)
	}
}

// What the error quotes is bounded: a very long line is cut, and a miss in a
// large file is as long as one in a small file.
func TestFsEditNotFoundIsBounded(t *testing.T) {
	h := newHarness(t)
	h.answer = true
	long := "value = compute(" + strings.Repeat("a_very_long_argument_name_", 40) + ")"
	h.put(t, "m.py", numbered(3, func(i int) string { return long }))
	_, err := h.call(t, "fs-edit", `{"path":"m.py","old":"value = compute(argument, other)\nmore","new":"x"}`)
	if err == nil || len(err.Error()) > editHintBytes || !strings.Contains(err.Error(), "the closest text") || !strings.Contains(err.Error(), "…") {
		t.Fatalf("a long line: %v", err)
	}
}

// No error of fs-edit names another tool: the lean configuration offers
// shell-exec and fs-edit, and an error that says to use fs-read sent the
// model to sed -n after a call that could not work.
func TestFsEditErrorsNameNoTool(t *testing.T) {
	h := newHarness(t)
	h.answer = true
	big := strings.Repeat("    x   =   1\n", 200)
	h.put(t, "m.py", "a = 1\nb = 2\na = 1\n")
	h.put(t, "big.py", big)
	for name, raw := range map[string]string{
		"not found":           `{"path":"m.py","old":"nothing like it","new":"x"}`,
		"unrelated":           `{"path":"m.py","old":"class Zebra(Mammal):\n  pass","new":"x"}`,
		"close":               `{"path":"m.py","old":"a = 1.0\nb = 2","new":"x"}`,
		"ambiguous":           `{"path":"m.py","old":"a = 1","new":"x"}`,
		"spaces, many places": `{"path":"m.py","old":"a   =   1","new":"x"}`,
		"spaces, long region": `{"path":"big.py","old":"` + strings.TrimSuffix(strings.Repeat("x = 1\\n", 200), "\\n") + `","new":"x"}`,
		"empty":               `{"path":"m.py","old":"","new":"x"}`,
	} {
		_, err := h.call(t, "fs-edit", raw)
		if err == nil {
			t.Errorf("%s: applied", name)
			continue
		}
		if strings.Contains(err.Error(), "fs-read") {
			t.Errorf("%s names a tool: %v", name, err)
		}
	}
	// The long region's error still says what to do.
	_, err := h.call(t, "fs-edit", `{"path":"big.py","old":"`+strings.TrimSuffix(strings.Repeat("x = 1\\n", 200), "\\n")+`","new":"x"}`)
	if err == nil || !strings.Contains(err.Error(), "look at them again and copy them exactly") {
		t.Fatalf("long region: %v", err)
	}
}

// With old occurring more than once, line names the occurrence: the one that
// holds it or else the nearest, the earlier on a tie. 23 of the 73 failed
// edits of the two-tool arms were an old that occurred 33 or 40 times, and
// the model knew the line from grep -n.
func TestFsEditLineChoosesAmongOccurrences(t *testing.T) {
	h := newHarness(t)
	h.answer = true
	body := func(marks ...int) string {
		return numbered(30, func(i int) string {
			for _, m := range marks {
				if i == m {
					return "    count += 1"
				}
			}
			return fmt.Sprintf("    other_%d()", i)
		})
	}
	text := body(5, 12, 13, 25)
	check := func(raw string, wantLines ...int) {
		t.Helper()
		h.put(t, "m.py", text)
		if _, err := h.call(t, "fs-edit", raw); err != nil {
			t.Fatalf("%s: %v", raw, err)
		}
		var marks []int
		for _, m := range []int{5, 12, 13, 25} {
			keep := true
			for _, w := range wantLines {
				keep = keep && m != w
			}
			if keep {
				marks = append(marks, m)
			}
		}
		if got := h.read(t, "m.py"); got != strings.Replace(body(marks...), fmt.Sprintf("    other_%d()", wantLines[0]), "    count += 2", 1) {
			t.Fatalf("%s edited the wrong occurrence:\n%s", raw, got)
		}
	}
	check(`{"path":"m.py","old":"    count += 1","new":"    count += 2","line":25}`, 25)
	check(`{"path":"m.py","old":"    count += 1","new":"    count += 2","line":6}`, 5)    // nearest
	check(`{"path":"m.py","old":"    count += 1","new":"    count += 2","line":9}`, 12)   // 3 from 12, 4 from 5
	check(`{"path":"m.py","old":"    count += 1","new":"    count += 2","line":100}`, 25) // past the end: the last
	check(`{"path":"m.py","old":"    count += 1","new":"    count += 2","line":1}`, 5)
	// Equally near: the earlier.
	check(`{"path":"m.py","old":"    count += 1","new":"    count += 2","line":19}`, 13) // 6 from 13, 6 from 25 -> 13

	// Without it the edit is refused and the error says how to choose.
	h.put(t, "m.py", text)
	_, err := h.call(t, "fs-edit", `{"path":"m.py","old":"    count += 1","new":"x"}`)
	if err == nil || !strings.Contains(err.Error(), "occurs 4 times") || !strings.Contains(err.Error(), "line 25: count += 1") || !strings.Contains(err.Error(), "pass line to choose one, or replace_all") {
		t.Fatalf("ambiguous: %v", err)
	}
	// A line that is not there for an old that occurs once is ignored.
	h.put(t, "m.py", "a = 1\nb = 2\n")
	if _, err := h.call(t, "fs-edit", `{"path":"m.py","old":"b = 2","new":"b = 3","line":1}`); err != nil {
		t.Fatalf("a line for a single occurrence: %v", err)
	}
	if got := h.read(t, "m.py"); got != "a = 1\nb = 3\n" {
		t.Fatalf("a line for a single occurrence: %q", got)
	}
	// replace_all replaces every one whatever the line.
	h.put(t, "m.py", text)
	if _, err := h.call(t, "fs-edit", `{"path":"m.py","old":"    count += 1","new":"    count += 2","replace_all":true,"line":5}`); err != nil {
		t.Fatal(err)
	}
	if got := h.read(t, "m.py"); strings.Contains(got, "count += 1") || strings.Count(got, "count += 2") != 4 {
		t.Fatalf("replace_all with a line:\n%s", got)
	}
}

// A line inside a multi-line occurrence names it, even when another begins
// nearer.
func TestFsEditLineInsideAMultiLineOccurrence(t *testing.T) {
	h := newHarness(t)
	h.answer = true
	text := "if a:\n    go()\n    stop()\nif b:\n    go()\n    stop()\n"
	h.put(t, "m.py", text)
	// The occurrence at line 2 spans lines 2-3; line 3 is inside it, and the
	// other begins at line 5.
	if _, err := h.call(t, "fs-edit", `{"path":"m.py","old":"    go()\n    stop()\n","new":"    pass\n","line":3}`); err != nil {
		t.Fatal(err)
	}
	if got := h.read(t, "m.py"); got != "if a:\n    pass\nif b:\n    go()\n    stop()\n" {
		t.Fatalf("got %q", got)
	}
}

// line inside each edit of a list, counted in the text the edits before it
// leave; line beside edits is refused, as is a line that is not a line number.
func TestFsEditLineInAList(t *testing.T) {
	h := newHarness(t)
	h.answer = true
	text := numbered(20, func(i int) string {
		if i%5 == 0 {
			return "    done()"
		}
		return fmt.Sprintf("    step_%d()", i)
	})
	h.put(t, "m.py", text)
	_, err := h.call(t, "fs-edit", `{"path":"m.py","edits":[{"old":"    done()","new":"    done(1)","line":20},{"old":"    done()","new":"    done(2)","line":5}]}`)
	if err != nil {
		t.Fatal(err)
	}
	got := h.read(t, "m.py")
	if !strings.Contains(got, "    step_19()\n    done(1)\n") || !strings.Contains(got, "    step_4()\n    done(2)\n") || strings.Count(got, "    done()") != 2 {
		t.Fatalf("got:\n%s", got)
	}
	// An edit of a list that is still ambiguous names itself.
	h.put(t, "m.py", text)
	_, err = h.call(t, "fs-edit", `{"path":"m.py","edits":[{"old":"    step_1()","new":"    step_1(1)"},{"old":"    done()","new":"    done(2)"}]}`)
	if err == nil || !strings.Contains(err.Error(), "edit 2 of 2 failed") || !strings.Contains(err.Error(), "pass line to choose one") {
		t.Fatalf("an ambiguous edit in a list: %v", err)
	}
	if got := h.read(t, "m.py"); got != text {
		t.Fatal("a failed list changed the file")
	}
	for raw, want := range map[string]string{
		`{"path":"m.py","edits":[{"old":"    done()","new":"x"}],"line":5}`:                               "give line inside each edit",
		`{"path":"m.py","edits":[{"old":"    done()","new":"x","line":-1}]}`:                              "line must be a line number from 1",
		`{"path":"m.py","old":"    done()","new":"x","line":-3}`:                                          "line must be a line number from 1",
		`{"path":"m.py","edits":[{"old":"    done()","new":"x","line":"five"}]}`:                          "line must be a line number from 1",
		`{"path":"m.py","edits":[{"old":"    done()","new":"x","lines":5}]}`:                              "unknown field",
		`{"path":"m.py","old":"    done()","new":"x","line":5,"edits":[{"old":"a","new":"b"}]}`:           "not both",
		`{"path":"m.py","edits":[{"old":"    done()","new":"x","line":null}],"old":"","new":"","line":0}`: "",
	} {
		_, err := h.call(t, "fs-edit", raw)
		switch {
		case want == "" && err != nil && !strings.Contains(err.Error(), "occurs 4 times"):
			t.Errorf("%s: %v", raw, err)
		case want != "" && (err == nil || !strings.Contains(err.Error(), want)):
			t.Errorf("%s: want %q, got %v", raw, want, err)
		}
	}
	// Positional use: line is the last parameter, after edits.
	h.put(t, "m.py", text)
	if _, err := h.eval(t, `(fs-edit "m.py" "    done()" "    done(9)" #f '() 10)`); err != nil {
		t.Fatalf("positional line: %v", err)
	}
	if got := h.read(t, "m.py"); !strings.Contains(got, "    step_9()\n    done(9)\n") {
		t.Fatalf("positional line:\n%s", got)
	}
}

// line picks among occurrences in a file that holds a credential too, where
// the match is made in the text the model reads (ADR 43).
func TestFsEditLineInAFileWithACredential(t *testing.T) {
	h := newHarness(t)
	h.answer = true
	const key = "sk-ant-abcdefgh1234"
	text := "token = \"" + key + "\"\nuse(token)\nmid = 1\nuse(token)\n"
	h.put(t, "m.py", text)
	_, err := h.call(t, "fs-edit", `{"path":"m.py","old":"use(token)","new":"use(token, 1)"}`)
	if err == nil || !strings.Contains(err.Error(), "line 4: use(token)") || !strings.Contains(err.Error(), "pass line to choose one") {
		t.Fatalf("ambiguous: %v", err)
	}
	if _, err := h.call(t, "fs-edit", `{"path":"m.py","old":"use(token)","new":"use(token, 1)","line":4}`); err != nil {
		t.Fatal(err)
	}
	if got := h.read(t, "m.py"); got != "token = \""+key+"\"\nuse(token)\nmid = 1\nuse(token, 1)\n" {
		t.Fatalf("got %q", got)
	}
}

// fn is a Python function of two parameters with the lines of body.
func fn(name string, body ...string) string {
	return "def " + name + "(obj, field):\n" + strings.Join(body, "\n") + "\n"
}

const sibling = "    related = getattr(obj, field.name)\n    return related.select_related().only('pk').iterator()"

// After an edit of some size, the answer says where else the same text
// occurs, in another file and in the same one: django-16938 needed one change
// in two files, and every failing session edited one.
func TestFsEditSaysWhereTheReplacedTextAlsoOccurs(t *testing.T) {
	h := newHarness(t)
	h.answer = true
	h.put(t, "pkg/python.py", "import x\n\n"+fn("handle", sibling)+"\n"+fn("handle_again", "    pass"))
	// The sibling is indented otherwise and has a different tail of spaces.
	h.put(t, "pkg/xml.py", "class S:\n    "+strings.ReplaceAll(fn("queryset", "\t"+strings.ReplaceAll(sibling, "\n", "\n\t")+"  "), "\n", "\n    "))
	h.put(t, "other/deep/third.py", "if True:\n    "+strings.ReplaceAll(sibling, "\n", "\n    ")+"\n")
	h.put(t, "pkg/same.py", fn("one", sibling)+"\n"+fn("two", sibling))

	got, err := h.call(t, "fs-edit", `{"path":"pkg/python.py","old":"    related = getattr(obj, field.name)\n    return related.select_related().only('pk').iterator()","new":"    related = getattr(obj, field.name)\n    return related.only('pk').iterator()"}`)
	if err != nil {
		t.Fatal(err)
	}
	// Folder first, then the rest in path order; three locations and the count.
	want := "the replaced text also occurs at: pkg/same.py:2, pkg/same.py:6, pkg/xml.py:3 (and 1 more); check whether the same change applies there"
	if !strings.HasSuffix(got, "\n"+want) {
		t.Fatalf("answer:\n%s\nwant it to end with\n%s", got, want)
	}
	// The note speaks of where the text occurs, never of editing it: the same
	// text is not always the same fault.
	if note := got[strings.Index(got, "the replaced text"):]; strings.Contains(note, "edit") {
		t.Fatalf("the note speaks of editing:\n%s", note)
	}

	// A short old is not searched for: one line of 31 characters.
	got, err = h.call(t, "fs-edit", `{"path":"pkg/xml.py","old":"related = getattr(obj, field.name)","new":"related = getattr(obj, field.name, None)"}`)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got, "also occurs") {
		t.Fatalf("a short old was searched for:\n%s", got)
	}
}

// A second copy in the same file is named, and the edit's own lines are not.
func TestFsEditAlsoOccursInTheSameFile(t *testing.T) {
	h := newHarness(t)
	h.answer = true
	h.put(t, "m.py", fn("a", sibling)+"\n"+fn("b", "    x = 1")+"\n"+fn("c", sibling))
	got, err := h.call(t, "fs-edit", `{"path":"m.py","old":"def a(obj, field):\n    related = getattr(obj, field.name)\n    return related.select_related().only('pk').iterator()\n","new":"def a(obj, field):\n    return 1\n"}`)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got, "also occurs") {
		t.Fatalf("the def line differs, so the text is not elsewhere:\n%s", got)
	}
	// An old that is a part of the function: the copy in c is named, a is not
	// (its text is gone), and neither is the new text itself.
	h.put(t, "m.py", fn("a", sibling)+"\n"+fn("c", sibling))
	got, err = h.call(t, "fs-edit", `{"path":"m.py","old":"    related = getattr(obj, field.name)\n    return related.select_related().only('pk').iterator()","new":"    related = getattr(obj, field.name)\n    return related.only('pk').iterator()","line":2}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(got, "the replaced text also occurs at: m.py:6; check whether the same change applies there") {
		t.Fatalf("answer:\n%s", got)
	}
}

// New text that holds the old text, an insertion after an anchor, is the
// edit's own place, and is not named as another; in a list it stays the
// edit's own after a later edit moved it.
func TestFsEditAlsoOccursLeavesOutTheEditsOwnNewText(t *testing.T) {
	h := newHarness(t)
	h.answer = true
	anchor := strings.ReplaceAll("    related = getattr(obj, field.name)\n    return related.select_related().only('pk').iterator()\n", "\n", `\n`)
	want := "the replaced text also occurs at: n.py:1; check whether the same change applies there"
	h.put(t, "m.py", fn("a", sibling)+"\n"+fn("c", "    pass"))
	h.put(t, "n.py", sibling+"\n")
	got, err := h.call(t, "fs-edit", `{"path":"m.py","old":"`+anchor+`","new":"`+anchor+`    log(related)\n"}`)
	if err != nil || !strings.HasSuffix(got, want) {
		t.Fatalf("an insertion: %v\n%s", err, got)
	}
	h.put(t, "m.py", fn("a", sibling)+"\n"+fn("c", "    pass"))
	got, err = h.call(t, "fs-edit", `{"path":"m.py","edits":[{"old":"`+anchor+`","new":"`+anchor+`    log(related)\n"},{"old":"def a(obj, field):\n","new":"# a longer comment\n# and another\ndef a(obj, field):\n"}]}`)
	if err != nil || !strings.HasSuffix(got, "edit 1: "+want) {
		t.Fatalf("insertions in a list: %v\n%s", err, got)
	}
	// Other places in the same file still count, beside the edit's own.
	h.put(t, "m.py", fn("a", sibling)+"\n"+fn("c", sibling))
	got, err = h.call(t, "fs-edit", `{"path":"m.py","old":"`+anchor+`","new":"`+anchor+`    log(related)\n","line":2}`)
	if err != nil || !strings.HasSuffix(got, "the replaced text also occurs at: m.py:7, n.py:1; check whether the same change applies there") {
		t.Fatalf("an insertion with another copy in the file: %v\n%s", err, got)
	}
}

// Under two lines or 40 characters nothing is searched, no other copy gives
// no note, and replace_all, which has changed every copy, gives none.
func TestFsEditAlsoOccursStaysQuiet(t *testing.T) {
	h := newHarness(t)
	h.answer = true
	h.put(t, "a.py", "x = compute(1)\ny = compute(1)\n")
	h.put(t, "b.py", "x = compute(1)\ny = compute(1)\n"+sibling+"\n")
	h.put(t, "c.py", sibling+"\n")

	// One line of 12 characters, and the same text is in b.py.
	got, err := h.call(t, "fs-edit", `{"path":"a.py","old":"x = compute(1)","new":"x = compute(2)"}`)
	if err != nil || strings.Contains(got, "also occurs") {
		t.Fatalf("below the threshold: %v\n%s", err, got)
	}
	// 34 non-space characters on one line is below the threshold, 40 is not.
	h.put(t, "e.py", "value = compute_something(1234, 5678)\n")
	h.put(t, "f.py", "value = compute_something(1234, 5678)\n")
	got, err = h.call(t, "fs-edit", `{"path":"e.py","old":"value = compute_something(1234, 5678)","new":"x"}`)
	if err != nil || strings.Contains(got, "also occurs") { // 34 characters, not counting spaces
		t.Fatalf("34 characters: %v\n%s", err, got)
	}
	h.put(t, "e.py", "value = compute_something(1234, 5678901234)\n")
	h.put(t, "f.py", "value = compute_something(1234, 5678901234)\n")
	got, err = h.call(t, "fs-edit", `{"path":"e.py","old":"value = compute_something(1234, 5678901234)","new":"x"}`)
	if err != nil || !strings.HasSuffix(got, "the replaced text also occurs at: f.py:1; check whether the same change applies there") { // 40
		t.Fatalf("40 characters: %v\n%s", err, got)
	}
	// Two short lines are enough.
	got, err = h.call(t, "fs-edit", `{"path":"a.py","old":"x = compute(2)\ny = compute(1)","new":"z = 0"}`)
	if err != nil || strings.Contains(got, "also occurs") {
		t.Fatalf("two lines, no other copy: %v\n%s", err, got)
	}
	h.put(t, "a.py", "x = compute(1)\ny = compute(1)\n")
	got, err = h.call(t, "fs-edit", `{"path":"a.py","old":"x = compute(1)\ny = compute(1)","new":"z = 0"}`)
	if err != nil || !strings.HasSuffix(got, "the replaced text also occurs at: b.py:1; check whether the same change applies there") {
		t.Fatalf("two lines: %v\n%s", err, got)
	}
	// No other copy.
	h.put(t, "d.py", "unique_thing_here = some_function_with_a_long_name(argument_one, argument_two)\n")
	got, err = h.call(t, "fs-edit", `{"path":"d.py","old":"unique_thing_here = some_function_with_a_long_name(argument_one, argument_two)","new":"unique_thing_here = 1"}`)
	if err != nil || strings.Contains(got, "also occurs") {
		t.Fatalf("no other copy: %v\n%s", err, got)
	}
	// replace_all.
	got, err = h.call(t, "fs-edit", `{"path":"c.py","old":"`+strings.ReplaceAll(sibling, "\n", `\n`)+`","new":"pass","replace_all":true}`)
	if err != nil || strings.Contains(got, "also occurs") {
		t.Fatalf("replace_all: %v\n%s", err, got)
	}
	got, err = h.call(t, "fs-edit", `{"path":"b.py","edits":[{"old":"`+strings.ReplaceAll(sibling, "\n", `\n`)+`","new":"pass","replace_all":true}]}`)
	if err != nil || strings.Contains(got, "also occurs") {
		t.Fatalf("replace_all in a list: %v\n%s", err, got)
	}
}

// Binary files, files over a mebibyte, hidden folders, vendored code, files
// that are not source, and files the denylist or a checker of the user's
// refuses are never searched, nor named.
func TestFsEditAlsoOccursSkipsWhatItShould(t *testing.T) {
	h := newHarness(t)
	h.answer = true
	h.put(t, "pkg/edit.py", sibling+"\n")
	h.put(t, "pkg/binary.py", "\x00\x01"+sibling+"\n")
	h.put(t, "pkg/big.py", sibling+"\n"+strings.Repeat("# padding padding padding\n", 50000))
	h.put(t, ".hidden/h.py", sibling+"\n")
	h.put(t, "vendor/v.py", sibling+"\n")
	h.put(t, "node_modules/n/n.py", sibling+"\n")
	h.put(t, "pkg/notes.txt", sibling+"\n")
	h.put(t, "pkg/credentials.yml", sibling+"\n")
	h.put(t, "pkg/keep.py", sibling+"\n")
	if st, _ := os.Stat(filepath.Join(h.root, "pkg/big.py")); st.Size() <= 1<<20 {
		t.Fatalf("the big file is %d bytes", st.Size())
	}
	got, err := h.call(t, "fs-edit", `{"path":"pkg/edit.py","old":"`+strings.ReplaceAll(sibling, "\n", `\n`)+`","new":"pass"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(got, "the replaced text also occurs at: pkg/keep.py:1; check whether the same change applies there") {
		t.Fatalf("answer:\n%s", got)
	}
}

// private is a checker of the user's that refuses reads of a name.
type private struct{}

func (private) Name() string { return "private" }
func (private) Check(_ context.Context, p *scheme.Prim, args []scheme.Value) guard.Decision {
	if p.Name == "fs-read" && len(args) > 0 && args[0].Kind == scheme.KString && strings.Contains(filepath.Base(args[0].S), "private") {
		return guard.Decision{Verdict: guard.Deny, Reason: "private"}
	}
	return guard.Decision{Verdict: guard.Allow}
}

// A file a checker of the user's refuses to the model's own read does not give
// away that it holds the text.
func TestFsEditAlsoOccursAsksTheGuardAboutWhatItNames(t *testing.T) {
	h := newHarness(t)
	h.answer = true
	o := DefaultOptions(h.root)
	h.in.SetGate(guard.New(guard.ConfirmFunc(func(context.Context, guard.Prompt) (bool, error) { return true, nil }), recorder{nlog.New(h.logBuf, "test")}, append(Checkers(o, nil, nil), private{})...))
	h.put(t, "pkg/edit.py", sibling+"\n")
	h.put(t, "pkg/private_copy.py", sibling+"\n")
	h.put(t, "pkg/public_copy.py", sibling+"\n")
	got, err := h.call(t, "fs-edit", `{"path":"pkg/edit.py","old":"`+strings.ReplaceAll(sibling, "\n", `\n`)+`","new":"pass"}`)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got, "private") || !strings.HasSuffix(got, "the replaced text also occurs at: pkg/public_copy.py:1; check whether the same change applies there") {
		t.Fatalf("answer:\n%s", got)
	}
}

// A scan that runs out of files or of time says nothing and does not fail
// the edit; one that found something before it ran out says what it found,
// without a count of what it did not reach. The limits are 5,000 files and
// 400 ms.
func TestFsEditAlsoOccursRunsOutQuietly(t *testing.T) {
	h := newHarness(t)
	h.answer = true
	old := `"old":"` + strings.ReplaceAll(sibling, "\n", `\n`) + `"`
	for i := 0; i < 5100; i++ {
		h.put(t, fmt.Sprintf("a/f%04d.py", i), "# nothing here\n")
	}
	h.put(t, "edit.py", sibling+"\n")

	// A copy among the first 5,000 files is found; one after them is not
	// looked for, and the edit goes through.
	h.put(t, "a/f0001.py", sibling+"\n")
	got, err := h.call(t, "fs-edit", `{"path":"edit.py",`+old+`,"new":"pass"}`)
	if err != nil || !strings.HasSuffix(got, "the replaced text also occurs at: a/f0001.py:1; check whether the same change applies there") {
		t.Fatalf("within the file limit: %v\n%s", err, got)
	}
	h.put(t, "a/f0001.py", "# nothing here\n")
	h.put(t, "edit.py", sibling+"\n")
	h.put(t, "z/late.py", sibling+"\n")
	got, err = h.call(t, "fs-edit", `{"path":"edit.py",`+old+`,"new":"pass"}`)
	if err != nil || strings.Contains(got, "also occurs") || !strings.HasPrefix(got, "edited edit.py") {
		t.Fatalf("past the file limit: %v\n%s", err, got)
	}

	// A call whose time runs out while it searches: its context ends when
	// the first copy is read, and the copy after it is not looked for (the
	// 400 ms limit ends the search the same way).
	h.put(t, "edit.py", sibling+"\n")
	h.put(t, "a/f0002.py", sibling+"\n")
	h.put(t, "a/f0003.py", sibling+"\n")
	got, err = h.call(t, "fs-edit", `{"path":"edit.py",`+old+`,"new":"pass"}`)
	if err != nil || !strings.Contains(got, "also occurs at: a/f0002.py:1, a/f0003.py:1") {
		t.Fatalf("with time: %v\n%s", err, got)
	}
	h.put(t, "edit.py", sibling+"\n")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	o := DefaultOptions(h.root)
	h.in.SetGate(guard.New(guard.ConfirmFunc(func(context.Context, guard.Prompt) (bool, error) { return true, nil }), recorder{nlog.New(h.logBuf, "test")}, append(Checkers(o, nil, nil), endsAtRead{cancel})...))
	got, err = h.callIn(t, ctx, "fs-edit", `{"path":"edit.py",`+old+`,"new":"pass"}`)
	if strings.Contains(got, "f0003") || strings.Contains(fmt.Sprint(err), "f0003") {
		t.Fatalf("out of time: %v\n%s", err, got)
	}
	if err != nil {
		t.Logf("the call's own context ended, so the call may say so: %v", err)
	}
	if b := h.read(t, "edit.py"); b != "pass\n" {
		t.Fatalf("the edit was not made: %q", b)
	}
}

// endsAtRead ends the call's context when a file named f0002.py is read.
type endsAtRead struct{ cancel func() }

func (endsAtRead) Name() string { return "ends-at-read" }
func (e endsAtRead) Check(_ context.Context, p *scheme.Prim, args []scheme.Value) guard.Decision {
	if p.Name == "fs-read" && len(args) > 0 && args[0].Kind == scheme.KString && filepath.Base(args[0].S) == "f0002.py" {
		e.cancel()
	}
	return guard.Decision{Verdict: guard.Allow}
}

// Each edit of a list that replaced enough text gets its own line.
func TestFsEditAlsoOccursInAList(t *testing.T) {
	h := newHarness(t)
	h.answer = true
	a := "    first_line_of_a = compute(alpha, beta)\n    second_line_of_a = compute(gamma)"
	b := "    first_line_of_b = compute(delta, beta)\n    second_line_of_b = compute(gamma)"
	h.put(t, "m.py", fn("one", a)+fn("two", b)+fn("three", "    pass"))
	h.put(t, "n.py", fn("one", a)+fn("two", b))
	got, err := h.call(t, "fs-edit", `{"path":"m.py","edits":[{"old":"`+strings.ReplaceAll(a, "\n", `\n`)+`","new":"    pass"},{"old":"`+strings.ReplaceAll(b, "\n", `\n`)+`","new":"    pass"}]}`)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"edit 1: the replaced text also occurs at: n.py:2;", "edit 2: the replaced text also occurs at: n.py:5;"} {
		if !strings.Contains(got, want) {
			t.Errorf("answer lacks %q:\n%s", want, got)
		}
	}
}
