package prim

import (
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/fclaude/nodwyn/internal/scheme"
)

// The changes to fs-edit of 2026-10-17 (a line, the closest text, where the
// replaced text also occurs) are the lean profile's (ADR 32, amended
// 2026-10-17): a full session's tool, schema and answers are the ones
// origin/main has. These tests fail if one of them reaches it.

var wordLine = regexp.MustCompile(`\bline\b`)

func paramNames(params []scheme.Param) string {
	var names []string
	for _, p := range params {
		names = append(names, p.Name)
	}
	return strings.Join(names, " ")
}

// A full session's fs-edit has no line parameter, in its schema, its
// description or its edits, and a call that gives one is refused as an unknown
// argument; a lean session's has it.
func TestFsEditHasNoLineParameterInAFullSession(t *testing.T) {
	for _, lean := range []bool{false, true} {
		h := newHarnessFor(t, lean)
		p, _ := h.in.Lookup("fs-edit")
		wantTop, wantItem := "path old new replace_all edits", "old new replace_all"
		if lean {
			wantTop, wantItem = wantTop+" line", wantItem+" line"
		}
		if got := paramNames(p.Meta.Params); got != wantTop {
			t.Errorf("lean %v: parameters %q, want %q", lean, got, wantTop)
		}
		if got := paramNames(p.Meta.Params[4].Items); got != wantItem {
			t.Errorf("lean %v: edit fields %q, want %q", lean, got, wantItem)
		}
		doc := p.Meta.Doc + p.Meta.Params[1].Doc + p.Meta.Params[4].Items[0].Doc
		if wordLine.MatchString(doc) != lean {
			t.Errorf("lean %v: the description mentions line: %v", lean, !lean)
		}
		if strings.Contains(p.Meta.Doc, "also occurs") != lean {
			t.Errorf("lean %v: the description speaks of where the text also occurs: %v", lean, !lean)
		}
	}
	h := newHarnessFor(t, false)
	h.answer = true
	h.put(t, "m.py", "a = 1\nb = 2\na = 1\n")
	if _, err := h.call(t, "fs-edit", `{"path":"m.py","old":"a = 1","new":"x","line":1}`); err == nil || !strings.Contains(err.Error(), "unknown argument(s): line") {
		t.Fatalf("a line for a full session's fs-edit: %v", err)
	}
	if _, err := h.call(t, "fs-edit", `{"path":"m.py","edits":[{"old":"a = 1","new":"x","line":1}]}`); err == nil || !strings.Contains(err.Error(), `unknown field "line": an edit has old, new and replace_all`) {
		t.Fatalf("a line in an edit of a full session's fs-edit: %v", err)
	}
	if _, err := h.eval(t, `(fs-edit "m.py" "a = 1" "x" #f '() 1)`); err == nil {
		t.Fatal("a sixth positional argument of a full session's fs-edit was accepted")
	}
}

// A full session's errors are the ones they were: the advice to read the
// region again, the ambiguity without line, no closest text, and the same
// for a file that holds a credential.
func TestFsEditErrorsAreUnchangedInAFullSession(t *testing.T) {
	h := newHarnessFor(t, false)
	h.answer = true
	h.put(t, "m.py", "a = 1\nb = 2\na = 1\n")
	_, err := h.call(t, "fs-edit", `{"path":"m.py","old":"nothing like it","new":"x"}`)
	if err == nil || err.Error() != "fs-edit: old text not found; it must match exactly, including whitespace; read the region again with fs-read (start, count) and copy it" {
		t.Fatalf("not found: %v", err)
	}
	_, err = h.call(t, "fs-edit", `{"path":"m.py","old":"a = 1","new":"x"}`)
	if err == nil || err.Error() != "fs-edit: old text occurs 2 times (line 1: a = 1; line 3: a = 1); include more context so it is unique (a line before or after the one you mean), or pass replace_all to replace every one" {
		t.Fatalf("ambiguous: %v", err)
	}
	h.put(t, "big.py", strings.Repeat("    x   =   1\n", 200))
	_, err = h.call(t, "fs-edit", `{"path":"big.py","old":"`+strings.TrimSuffix(strings.Repeat("x = 1\\n", 200), "\\n")+`","new":"x"}`)
	if err == nil || !strings.Contains(err.Error(), "read them with fs-read and copy them exactly") {
		t.Fatalf("a long region: %v", err)
	}
	h.put(t, "k.py", "token = \"sk-ant-abcdefgh1234\"\nuse(token)\nmid = 1\nuse(token)\n")
	_, err = h.call(t, "fs-edit", `{"path":"k.py","old":"use(token)","new":"x"}`)
	if err == nil || err.Error() != "fs-edit: old text occurs 2 times; include more context so it is unique, or pass replace_all to replace every one" {
		t.Fatalf("ambiguous in a file with a credential: %v", err)
	}
	// And the lean session's say what they say.
	l := newHarnessFor(t, true)
	l.answer = true
	l.put(t, "m.py", "a = 1\nb = 2\na = 1\n")
	if _, err := l.call(t, "fs-edit", `{"path":"m.py","old":"nothing like it","new":"x"}`); err == nil || strings.Contains(err.Error(), "fs-read") {
		t.Fatalf("lean, not found: %v", err)
	}
	if _, err := l.call(t, "fs-edit", `{"path":"m.py","old":"a = 1","new":"x"}`); err == nil || !strings.Contains(err.Error(), "pass line to choose one, or replace_all") {
		t.Fatalf("lean, ambiguous: %v", err)
	}
}

// A full session's answer to an edit does not say where the replaced text
// also occurs, nor does it change the other answer it has: the many places an
// edit changed are read with fs-read there, and viewed in a lean session.
func TestFsEditAnswerIsUnchangedInAFullSession(t *testing.T) {
	answers := map[bool]string{}
	for _, lean := range []bool{false, true} {
		h := newHarnessFor(t, lean)
		h.answer = true
		h.put(t, "pkg/a.py", fn("handle", sibling))
		h.put(t, "pkg/b.py", fn("other", sibling))
		got, err := h.call(t, "fs-edit", `{"path":"pkg/a.py","old":"`+strings.ReplaceAll(sibling, "\n", `\n`)+`","new":"pass"}`)
		if err != nil {
			t.Fatal(err)
		}
		if lean != strings.Contains(got, "the replaced text also occurs at: pkg/b.py:2") {
			t.Errorf("lean %v: answer\n%s", lean, got)
		}
		answers[lean] = got
	}
	if strings.Contains(answers[false], "also occurs") || !strings.HasPrefix(answers[false], "edited pkg/a.py, now 2 lines (-1);") {
		t.Errorf("a full session's answer:\n%s", answers[false])
	}
	// Many changed places: the line that says how to see the rest.
	var before, after strings.Builder
	for i := 1; i <= 400; i++ {
		fmt.Fprintf(&before, "line %d\n", i)
		if i%10 == 0 {
			fmt.Fprintf(&after, "changed %d\n", i)
		} else {
			fmt.Fprintf(&after, "line %d\n", i)
		}
	}
	for counting, want := range map[bool]string{false: "read them with fs-read", true: "view them"} {
		if got := editedRegionsCounting("a.txt", before.String(), after.String(), counting); !strings.Contains(got, "more changed places, from line") || !strings.HasSuffix(got, ": "+want) {
			t.Errorf("counting %v: want %q in\n%.300s ... %s", counting, want, got, got[max(0, len(got)-120):])
		}
	}
}
