package prim

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fclaude/brote/scheme"
	"github.com/fclaude/nodwyn/internal/guard"
	nlog "github.com/fclaude/nodwyn/internal/log"
)

// newLeanHarness is newHarness for a lean session: the profile whose fs-edit
// shows the closest text, repairs literal escape sequences and says where the
// replaced text also occurs (ADR 32, amended 2026-10-17). newHarness's is a
// full one, whose tools are the ones they were.
func newLeanHarness(t *testing.T) *harness {
	t.Helper()
	return newHarnessFor(t, true)
}

// newHarnessFor is a harness whose tools are lean or full.
func newHarnessFor(t *testing.T, lean bool) *harness {
	t.Helper()
	root, _ := filepath.EvalSymlinks(t.TempDir())
	h := &harness{root: root, logBuf: &bytes.Buffer{}}
	h.in = scheme.New()
	o := DefaultOptions(root)
	o.MaxOutput = 4096
	o.Lean = lean
	if err := Register(h.in, o); err != nil {
		t.Fatal(err)
	}
	confirm := guard.ConfirmFunc(func(_ context.Context, p guard.Prompt) (bool, error) {
		h.prompts = append(h.prompts, p)
		return h.answer, nil
	})
	h.in.SetGate(guard.New(confirm, recorder{nlog.New(h.logBuf, "test")}, Checkers(o, nil, nil)...))
	return h
}

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
	h := newLeanHarness(t)
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

// Nothing is shown for a text no line of the file is close to, and the error
// says there is no close match; and the window of lines stays inside the file
// at its ends.
func TestFsEditNotFoundSaysWhenNothingResembles(t *testing.T) {
	h := newLeanHarness(t)
	h.answer = true
	h.put(t, "m.py", "import os\nimport sys\n\ndef main():\n    return 0\n")

	_, err := h.call(t, "fs-edit", `{"path":"m.py","old":"class Frobnicator(Widget):\n    pass","new":"x"}`)
	if err == nil || !strings.Contains(err.Error(), "there is no close match for the first line of old (`class Frobnicator(Widget):`) in this file") {
		t.Fatalf("unrelated old: %v", err)
	}
	if strings.Contains(err.Error(), "closest") || strings.Contains(err.Error(), "fs-read") {
		t.Fatalf("unrelated old: %v", err)
	}
	// One shared word is no resemblance.
	if _, err := h.call(t, "fs-edit", `{"path":"m.py","old":"import json","new":"x"}`); err == nil || !strings.Contains(err.Error(), "there is no close match") {
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
	h := newLeanHarness(t)
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
	h := newLeanHarness(t)
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
	h := newLeanHarness(t)
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

// A line that shares two of old's words among four others (0.44) is not shown:
// in the transcripts the closest text was wrong once in four, a line that
// shared a word or two; one with three of them among two others (0.67) is.
func TestFsEditShowsOnlyACloseMatch(t *testing.T) {
	h := newLeanHarness(t)
	h.answer = true
	h.put(t, "m.py", "x = 1\nresult = other_function(items, extra, more, args)\ny = 2\n")
	_, err := h.call(t, "fs-edit", `{"path":"m.py","old":"result = compute(items)","new":"x"}`)
	if err == nil || !strings.Contains(err.Error(), "there is no close match") || strings.Contains(err.Error(), "closest text") {
		t.Fatalf("a weak match: %v", err)
	}
	h.put(t, "m.py", "x = 1\nresult = compute_total(items, tax_rate)\ny = 2\n")
	_, err = h.call(t, "fs-edit", `{"path":"m.py","old":"result = compute_total(items, tax)","new":"x"}`)
	if err == nil || !strings.Contains(err.Error(), "the closest text is at lines 1-3") {
		t.Fatalf("a close match: %v", err)
	}
}

// An old written with literal \n sequences (a model that escaped its JSON
// twice) that matches nowhere as written, but once unescaped, is applied
// unescaped, new with it, and the answer says so: five lean sessions lost three
// or four calls each to it and gave fs-edit up for a script.
func TestFsEditRepairsLiteralEscapeSequences(t *testing.T) {
	h := newLeanHarness(t)
	h.answer = true
	h.put(t, "m.py", "def f():\n    s = \"a\"\n    return s\n")
	// The JSON carries \\n: old reaches the tool with a backslash and an n.
	got, err := h.call(t, "fs-edit", `{"path":"m.py","old":"    s = \\\"a\\\"\\n    return s","new":"    s = \\\"b\\\"\\n    return s.strip()"}`)
	if err != nil {
		t.Fatal(err)
	}
	if want := "def f():\n    s = \"b\"\n    return s.strip()\n"; h.read(t, "m.py") != want {
		t.Fatalf("the file:\n%q\nwant\n%q", h.read(t, "m.py"), want)
	}
	if !strings.Contains(got, "\nold held literal \\n and \\\" sequences; they were matched as real newlines and quotes") {
		t.Fatalf("answer:\n%s", got)
	}
	// \n alone says it as the plan has it.
	h.put(t, "n.py", "a = 1\nb = 2\n")
	got, err = h.call(t, "fs-edit", `{"path":"n.py","old":"a = 1\\nb = 2","new":"a = 1\\nb = 3"}`)
	if err != nil || h.read(t, "n.py") != "a = 1\nb = 3\n" || !strings.HasSuffix(got, "\nold held literal \\n sequences; they were matched as real newlines") {
		t.Fatalf("\\n alone: %v\n%s\n%q", err, got, h.read(t, "n.py"))
	}
	// A code \n that new keeps escaped (\\\\n in the twice-escaped JSON) stays one.
	h.put(t, "j.py", "x = 1\ny = 2\n")
	if _, err := h.call(t, "fs-edit", `{"path":"j.py","old":"x = 1\\ny = 2","new":"x = '\\\\n'.join(a)\\ny = 2"}`); err != nil {
		t.Fatal(err)
	}
	if got := h.read(t, "j.py"); got != "x = '\\n'.join(a)\ny = 2\n" {
		t.Fatalf("an escaped backslash in new: %q", got)
	}
	// In a list, the note names the edit.
	h.put(t, "l.py", "a = 1\nb = 2\nc = 3\n")
	got, err = h.call(t, "fs-edit", `{"path":"l.py","edits":[{"old":"c = 3","new":"c = 4"},{"old":"a = 1\\nb = 2","new":"a = 0"}]}`)
	if err != nil || !strings.HasSuffix(got, "\nedit 2: old held literal \\n sequences; they were matched as real newlines") {
		t.Fatalf("in a list: %v\n%s", err, got)
	}
	// Twice unescaped, or not at all, is no repair: the miss is told as before.
	h.put(t, "t.py", "a = 1\nb = 2\na = 1\nb = 2\n")
	if _, err := h.call(t, "fs-edit", `{"path":"t.py","old":"a = 1\\nb = 2","new":"x"}`); err == nil || !strings.Contains(err.Error(), "old text not found") {
		t.Fatalf("two unescaped matches: %v", err)
	}
	if got := h.read(t, "t.py"); got != "a = 1\nb = 2\na = 1\nb = 2\n" {
		t.Fatalf("a refused repair changed the file: %q", got)
	}
	// A full session's fs-edit does not repair.
	f := newHarnessFor(t, false)
	f.answer = true
	f.put(t, "n.py", "a = 1\nb = 2\n")
	if _, err := f.call(t, "fs-edit", `{"path":"n.py","old":"a = 1\\nb = 2","new":"x"}`); err == nil || !strings.Contains(err.Error(), "read the region again with fs-read") {
		t.Fatalf("full: %v", err)
	}
}

// track makes the harness's root a git work tree with every file in it added,
// for the note that searches only what git tracks.
func (h *harness) track(t *testing.T) {
	t.Helper()
	for _, args := range [][]string{{"init", "-q"}, {"add", "-A"}} {
		if args[0] == "init" {
			if _, err := os.Stat(filepath.Join(h.root, ".git")); err == nil {
				continue
			}
		}
		cmd := exec.Command("git", args...)
		cmd.Dir = h.root
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
}

// fn is a Python function of two parameters with the lines of body.
func fn(name string, body ...string) string {
	return "def " + name + "(obj, field):\n" + strings.Join(body, "\n") + "\n"
}

const sibling = "    related = getattr(obj, field.name)\n    return related.select_related().only('pk').iterator()"

// indented is sibling inside an if: the same lines once whitespace is
// ignored, but not the same text to an exact match.
var indented = "    if True:\n" + strings.ReplaceAll("    "+sibling, "\n", "\n    ")

// After an edit of some size, the answer says where else the same text
// occurs, in another file and in the same one: django-16938 needed one change
// in two files, and every failing session edited one. A fourth other place is
// one too many, and there is no note.
func TestFsEditSaysWhereTheReplacedTextAlsoOccurs(t *testing.T) {
	h := newLeanHarness(t)
	h.answer = true
	h.put(t, "pkg/python.py", "import x\n\n"+fn("handle", sibling)+"\n"+fn("handle_again", "    pass"))
	// The sibling is indented otherwise and has a different tail of spaces.
	h.put(t, "pkg/xml.py", "class S:\n    "+strings.ReplaceAll(fn("queryset", "\t"+strings.ReplaceAll(sibling, "\n", "\n\t")+"  "), "\n", "\n    "))
	h.put(t, "pkg/same.py", fn("one", sibling)+"\n"+fn("two", sibling))
	h.track(t)
	old := `"old":"` + strings.ReplaceAll(sibling, "\n", `\n`) + `"`
	got, err := h.call(t, "fs-edit", `{"path":"pkg/python.py",`+old+`,"new":"    related = getattr(obj, field.name)\n    return related.only('pk').iterator()"}`)
	if err != nil {
		t.Fatal(err)
	}
	// Folder first, in path order.
	want := "the replaced text also occurs at: pkg/same.py:2, pkg/same.py:6, pkg/xml.py:3; check whether the same change applies there"
	if !strings.HasSuffix(got, "\n"+want) {
		t.Fatalf("answer:\n%s\nwant it to end with\n%s", got, want)
	}
	if note := got[strings.Index(got, "the replaced text"):]; strings.Contains(note, "edit") {
		t.Fatalf("the note speaks of editing:\n%s", note)
	}
	// A fourth: no note.
	h.put(t, "pkg/python.py", "import x\n\n"+fn("handle", sibling)+"\n")
	h.put(t, "other/deep/third.py", "if True:\n"+indented+"\n")
	h.track(t)
	got, err = h.call(t, "fs-edit", `{"path":"pkg/python.py",`+old+`,"new":"    pass"}`)
	if err != nil || strings.Contains(got, "also occurs") {
		t.Fatalf("four other places: %v\n%s", err, got)
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
	h := newLeanHarness(t)
	h.answer = true
	h.put(t, "m.py", fn("a", sibling)+"\n"+fn("c", indented))
	h.track(t)
	got, err := h.call(t, "fs-edit", `{"path":"m.py","old":"`+strings.ReplaceAll(sibling, "\n", `\n`)+`","new":"    related = getattr(obj, field.name)\n    return related.only('pk').iterator()"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(got, "the replaced text also occurs at: m.py:7; check whether the same change applies there") {
		t.Fatalf("answer:\n%s", got)
	}
}

// New text that holds the old text, an insertion after an anchor, is the
// edit's own place, and is not named as another; in a list it stays the
// edit's own after a later edit moved it.
func TestFsEditAlsoOccursLeavesOutTheEditsOwnNewText(t *testing.T) {
	h := newLeanHarness(t)
	h.answer = true
	anchor := strings.ReplaceAll(sibling+"\n", "\n", `\n`)
	want := "the replaced text also occurs at: n.py:1; check whether the same change applies there"
	h.put(t, "m.py", fn("a", sibling)+"\n"+fn("c", "    pass"))
	h.put(t, "n.py", sibling+"\n")
	h.track(t)
	got, err := h.call(t, "fs-edit", `{"path":"m.py","old":"`+anchor+`","new":"`+anchor+`    log(related)\n"}`)
	if err != nil || !strings.HasSuffix(got, want) {
		t.Fatalf("an insertion: %v\n%s", err, got)
	}
	h.put(t, "m.py", fn("a", sibling)+"\n"+fn("c", "    pass"))
	got, err = h.call(t, "fs-edit", `{"path":"m.py","edits":[{"old":"`+anchor+`","new":"`+anchor+`    log(related)\n"},{"old":"def a(obj, field):\n","new":"# a longer comment\n# and another\ndef a(obj, field):\n"}]}`)
	if err != nil || !strings.HasSuffix(got, "edit 1: "+want) {
		t.Fatalf("insertions in a list: %v\n%s", err, got)
	}
}

// Under two lines or 40 characters nothing is searched, nor for import lines
// and decorators however long; no other copy gives no note, and replace_all,
// which has changed every copy, gives none.
func TestFsEditAlsoOccursStaysQuiet(t *testing.T) {
	h := newLeanHarness(t)
	h.answer = true
	imports := "from django.db.models import Model, QuerySet\nfrom django.utils.functional import cached_property"
	decorator := "@pytest.mark.parametrize('value', [1, 2, 3, 4, 5, 6, 7, 8, 9])"
	h.put(t, "a.py", "x = compute(1)\ny = compute(1)\n"+imports+"\n"+decorator+"\n")
	h.put(t, "b.py", "x = compute(1)\ny = compute(1)\n"+sibling+"\n"+imports+"\n"+decorator+"\n")
	h.put(t, "c.py", sibling+"\n")
	h.put(t, "e.py", "value = compute_something(1234, 5678)\n")
	h.put(t, "f.py", "value = compute_something(1234, 5678)\nvalue = compute_something(1234, 5678901234)\n")
	h.put(t, "d.py", "unique_thing_here = some_function_with_a_long_name(argument_one, argument_two)\n")
	h.track(t)

	for name, raw := range map[string]string{
		"one line of 12 characters": `{"path":"a.py","old":"x = compute(1)","new":"x = compute(2)"}`,
		"34 characters":             `{"path":"e.py","old":"value = compute_something(1234, 5678)","new":"x"}`,
		"imports":                   `{"path":"a.py","old":"` + strings.ReplaceAll(imports, "\n", `\n`) + `","new":"import os"}`,
		"a decorator":               `{"path":"a.py","old":"` + decorator + `","new":"@pytest.mark.skip"}`,
		"no other copy":             `{"path":"d.py","old":"unique_thing_here = some_function_with_a_long_name(argument_one, argument_two)","new":"unique_thing_here = 1"}`,
		"replace_all":               `{"path":"c.py","old":"` + strings.ReplaceAll(sibling, "\n", `\n`) + `","new":"pass","replace_all":true}`,
	} {
		got, err := h.call(t, "fs-edit", raw)
		if err != nil || strings.Contains(got, "also occurs") {
			t.Errorf("%s: %v\n%s", name, err, got)
		}
	}
	// Two short lines are enough, and 40 characters on one.
	got, err := h.call(t, "fs-edit", `{"path":"a.py","old":"x = compute(2)\ny = compute(1)","new":"z = 0"}`)
	if err != nil || strings.Contains(got, "also occurs") {
		t.Fatalf("two lines, no other copy: %v\n%s", err, got)
	}
	h.put(t, "a.py", "x = compute(1)\ny = compute(1)\n")
	got, err = h.call(t, "fs-edit", `{"path":"a.py","old":"x = compute(1)\ny = compute(1)","new":"z = 0"}`)
	if err != nil || !strings.HasSuffix(got, "the replaced text also occurs at: b.py:1; check whether the same change applies there") {
		t.Fatalf("two lines: %v\n%s", err, got)
	}
	h.put(t, "e.py", "value = compute_something(1234, 5678901234)\n")
	got, err = h.call(t, "fs-edit", `{"path":"e.py","old":"value = compute_something(1234, 5678901234)","new":"x"}`)
	if err != nil || !strings.HasSuffix(got, "the replaced text also occurs at: f.py:2; check whether the same change applies there") {
		t.Fatalf("40 characters: %v\n%s", err, got)
	}
}

// Only the files git tracks are searched, outside build, dist, vendor,
// node_modules and hidden folders, and not binary files, files over a
// mebibyte, files that are not source, nor those the denylist or a checker of
// the user's refuses; outside a git work tree nothing is.
func TestFsEditAlsoOccursSkipsWhatItShould(t *testing.T) {
	h := newLeanHarness(t)
	h.answer = true
	h.put(t, "pkg/edit.py", sibling+"\n")
	h.put(t, "pkg/binary.py", "\x00\x01"+sibling+"\n")
	h.put(t, "pkg/big.py", sibling+"\n"+strings.Repeat("# padding padding padding\n", 50000))
	for _, dir := range []string{".hidden", "vendor", "node_modules/n", "build", "dist", "src/build"} {
		h.put(t, dir+"/copy.py", sibling+"\n")
	}
	h.put(t, "pkg/notes.txt", sibling+"\n")
	h.put(t, "pkg/credentials.yml", sibling+"\n")
	h.put(t, "pkg/keep.py", sibling+"\n")
	h.track(t)
	h.put(t, "pkg/untracked.py", sibling+"\n")
	if st, _ := os.Stat(filepath.Join(h.root, "pkg/big.py")); st.Size() <= 1<<20 {
		t.Fatalf("the big file is %d bytes", st.Size())
	}
	old := `"old":"` + strings.ReplaceAll(sibling, "\n", `\n`) + `"`
	got, err := h.call(t, "fs-edit", `{"path":"pkg/edit.py",`+old+`,"new":"pass"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(got, "the replaced text also occurs at: pkg/keep.py:1; check whether the same change applies there") {
		t.Fatalf("answer:\n%s", got)
	}
	// No git: no note.
	n := newLeanHarness(t)
	n.answer = true
	n.put(t, "a.py", sibling+"\n")
	n.put(t, "b.py", sibling+"\n")
	if got, err := n.call(t, "fs-edit", `{"path":"a.py",`+old+`,"new":"pass"}`); err != nil || strings.Contains(got, "also occurs") {
		t.Fatalf("outside git: %v\n%s", err, got)
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
	h := newLeanHarness(t)
	h.answer = true
	o := DefaultOptions(h.root)
	o.Lean = true
	h.in.SetGate(guard.New(guard.ConfirmFunc(func(context.Context, guard.Prompt) (bool, error) { return true, nil }), recorder{nlog.New(h.logBuf, "test")}, append(Checkers(o, nil, nil), private{})...))
	h.put(t, "pkg/edit.py", sibling+"\n")
	h.put(t, "pkg/private_copy.py", sibling+"\n")
	h.put(t, "pkg/public_copy.py", sibling+"\n")
	h.track(t)
	got, err := h.call(t, "fs-edit", `{"path":"pkg/edit.py","old":"`+strings.ReplaceAll(sibling, "\n", `\n`)+`","new":"pass"}`)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got, "private") || !strings.HasSuffix(got, "the replaced text also occurs at: pkg/public_copy.py:1; check whether the same change applies there") {
		t.Fatalf("answer:\n%s", got)
	}
}

// A scan that runs out of files or of time says nothing, since it cannot say
// how many other places there are, and does not fail the edit. The limits are
// 5,000 files and 400 ms.
func TestFsEditAlsoOccursRunsOutQuietly(t *testing.T) {
	h := newLeanHarness(t)
	h.answer = true
	old := `"old":"` + strings.ReplaceAll(sibling, "\n", `\n`) + `"`
	h.put(t, "edit.py", sibling+"\n")
	h.put(t, "a/f0001.py", sibling+"\n")
	h.track(t)
	got, err := h.call(t, "fs-edit", `{"path":"edit.py",`+old+`,"new":"pass"}`)
	if err != nil || !strings.HasSuffix(got, "the replaced text also occurs at: a/f0001.py:1; check whether the same change applies there") {
		t.Fatalf("within the limits: %v\n%s", err, got)
	}
	// Past 5,000 files: nothing, though the copy is among the first.
	for i := 2; i < 5100; i++ {
		h.put(t, fmt.Sprintf("a/f%04d.py", i), "# nothing here\n")
	}
	h.put(t, "edit.py", sibling+"\n")
	h.track(t)
	got, err = h.call(t, "fs-edit", `{"path":"edit.py",`+old+`,"new":"pass"}`)
	if err != nil || strings.Contains(got, "also occurs") || !strings.HasPrefix(got, "edited edit.py") {
		t.Fatalf("past the file limit: %v\n%s", err, got)
	}
	// Out of time while it searches: the call's context ends when the copy is
	// read, and there is no note (the 400 ms limit ends a search the same way).
	small := newLeanHarness(t)
	small.answer = true
	small.put(t, "edit.py", sibling+"\n")
	small.put(t, "a/f0002.py", sibling+"\n")
	small.put(t, "a/f0003.py", sibling+"\n")
	small.track(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	o := DefaultOptions(small.root)
	o.Lean = true
	small.in.SetGate(guard.New(guard.ConfirmFunc(func(context.Context, guard.Prompt) (bool, error) { return true, nil }), recorder{nlog.New(small.logBuf, "test")}, append(Checkers(o, nil, nil), endsAtRead{cancel})...))
	got, err = small.callIn(t, ctx, "fs-edit", `{"path":"edit.py",`+old+`,"new":"pass"}`)
	if strings.Contains(got, "also occurs") || strings.Contains(fmt.Sprint(err), "also occurs") {
		t.Fatalf("out of time: %v\n%s", err, got)
	}
	if b := small.read(t, "edit.py"); b != "pass\n" {
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
	h := newLeanHarness(t)
	h.answer = true
	a := "    first_line_of_a = compute(alpha, beta)\n    second_line_of_a = compute(gamma)"
	b := "    first_line_of_b = compute(delta, beta)\n    second_line_of_b = compute(gamma)"
	h.put(t, "m.py", fn("one", a)+fn("two", b)+fn("three", "    pass"))
	h.put(t, "n.py", fn("one", a)+fn("two", b))
	h.track(t)
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
