package prim

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fclaude/brote/scheme"
)

// registered is the interpreter of a repository with one file, with the tools
// registered for options that are lean or not.
func registered(t *testing.T, lean bool) (*scheme.Interp, string) {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.py"), []byte("def f():\n    return 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	o := DefaultOptions(root)
	o.Lean = lean
	in := scheme.New()
	if err := Register(in, o); err != nil {
		t.Fatal(err)
	}
	return in, root
}

func toolNames(in *scheme.Interp) string {
	var names []string
	for _, p := range in.Tools() {
		names = append(names, p.Name)
	}
	return strings.Join(names, " ")
}

// The lean profile adds the outline, tests-for and scheme tools to the
// catalogue; without it the catalogue is the one TestToolsExposedToModel
// lists, since a full session's request carries every tool's schema.
func TestOnlyALeanSessionHasTheOutlineTestsForAndSchemeTools(t *testing.T) {
	in, _ := registered(t, true)
	if got, want := toolNames(in), "doc-export fs-edit fs-find fs-list fs-read fs-write index-grep outline project-notes-save scheme shell-exec tests-for"; got != want {
		t.Fatalf("lean tools: %s, want %s", got, want)
	}
	in, _ = registered(t, false)
	if got := toolNames(in); strings.Contains(got, "outline") || strings.Contains(got, "scheme") || strings.Contains(got, "tests-for") {
		t.Fatalf("a full session has the lean tools: %s", got)
	}
}

// fs-read takes a symbol only in a lean session: otherwise it would be one
// more parameter and a sentence of description in every request.
func TestFsReadTakesASymbolOnlyInALeanSession(t *testing.T) {
	for lean, params := range map[bool]int{false: 3, true: 4} {
		in, _ := registered(t, lean)
		p, ok := in.Lookup("fs-read")
		if !ok {
			t.Fatal("no fs-read")
		}
		if len(p.Meta.Params) != params || strings.Contains(p.Meta.Doc, "symbol") != lean {
			t.Errorf("lean %v: fs-read has %d parameters and doc %q, want %d parameters", lean, len(p.Meta.Params), p.Meta.Doc, params)
		}
	}
}

// fs-write answers "ok" as it did, and says what it wrote in a lean session.
func TestFsWriteAnswersOkUnlessLean(t *testing.T) {
	for lean, want := range map[bool]string{false: "ok", true: "wrote b.py: a new file of 1 lines"} {
		in, root := registered(t, lean)
		v, err := in.EvalString(t.Context(), `(fs-write "b.py" "x = 1\n")`)
		if err != nil {
			t.Fatal(err)
		}
		got := "ok" // what a void answer is rendered as
		if v.Kind == scheme.KString {
			got = v.S
		} else if v.Kind != scheme.KVoid {
			t.Fatalf("lean %v: fs-write answered %s", lean, scheme.Write(v))
		}
		if got != want {
			t.Errorf("lean %v: fs-write answered %q, want %q", lean, got, want)
		}
		if b, _ := os.ReadFile(filepath.Join(root, "b.py")); string(b) != "x = 1\n" {
			t.Errorf("lean %v: the file holds %q", lean, b)
		}
	}
}

// An edit says how many lines it added and removed in a lean session, and how
// many more or fewer the file has otherwise: a swapped line leaves the second
// count at nothing.
func TestEditedRegionsCountAddedAndRemovedLinesWhenCounting(t *testing.T) {
	var before strings.Builder
	for i := 1; i <= 60; i++ {
		before.WriteString("line " + string(rune('a'+i%26)) + string(rune('a'+i/26)) + "\n")
	}
	swap := strings.Replace(before.String(), "line cb\n", "line CB\n", 1)
	if got := editedRegionsCounting("a.txt", before.String(), swap, true); !strings.Contains(got, "now 60 lines (+1 -1);") {
		t.Errorf("a swapped line, counting: %s", got)
	}
	if got := editedRegionsCounting("a.txt", before.String(), swap, false); !strings.Contains(got, "now 60 lines;") {
		t.Errorf("a swapped line, not counting: %s", got)
	}
	grown := strings.Replace(before.String(), "line cb\n", "line cb\nline more\n", 1)
	if got := editedRegionsCounting("a.txt", before.String(), grown, true); !strings.Contains(got, "now 61 lines (+1 -0);") {
		t.Errorf("a line added, counting: %s", got)
	}
	if got := editedRegionsCounting("a.txt", before.String(), grown, false); !strings.Contains(got, "now 61 lines (+1);") {
		t.Errorf("a line added, not counting: %s", got)
	}
}

// index-grep refuses a tree of more candidate files than its limit unless the
// session is lean, which searches the source files of the tree and says so.
func TestIndexGrepRefusesALargeTreeUnlessLean(t *testing.T) {
	was := searchFileLimit
	searchFileLimit = 3
	defer func() { searchFileLimit = was }()
	for lean, refused := range map[bool]bool{false: true, true: false} {
		in, root := registered(t, lean)
		for _, name := range []string{"b.py", "c.py", "d.txt", "e.txt"} {
			if err := os.WriteFile(filepath.Join(root, name), []byte("needle\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		_, err := in.EvalString(t.Context(), `(index-grep "." "needle")`)
		if refused != (err != nil) {
			t.Errorf("lean %v: err = %v, want refused %v", lean, err, refused)
		}
	}
}
