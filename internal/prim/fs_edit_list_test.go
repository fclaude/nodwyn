package prim

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/fclaude/brote/scheme"
	"github.com/fclaude/nodwyn/internal/detect/l1"
	"github.com/fclaude/nodwyn/internal/guard"
)

// callJSON applies a tool call as the loop does: the model's named JSON
// arguments in the primitive's order, then the guard and the handler with
// model origin.
func (h *harness) callJSON(t *testing.T, name, raw string) error {
	t.Helper()
	p, ok := h.in.Lookup(name)
	if !ok {
		t.Fatalf("no primitive %s", name)
	}
	args, err := p.ArgsFromJSON([]byte(raw))
	if err != nil {
		return err
	}
	_, err = h.in.Apply(scheme.WithOrigin(context.Background(), scheme.OriginModel), scheme.Value{Kind: scheme.KPrim, Prim: p}, args)
	return err
}

func editsJSON(t *testing.T, path string, edits ...map[string]any) string {
	t.Helper()
	b, err := json.Marshal(map[string]any{"path": path, "edits": edits})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// Edits apply in order, each to the text the one before left, so a later
// edit may match what an earlier one inserted; the call asks once, and
// its preview is one diff from the file to the text all of them leave.
func TestFsEditEditsApplyInOrder(t *testing.T) {
	h := newHarness(t)
	h.answer = true
	file := filepath.Join(h.root, "code.py")
	before := "def f():\n    return 1\n\ndef g():\n    return 2\n"
	writeFile(t, file, before)
	err := h.callJSON(t, "fs-edit", editsJSON(t, "code.py",
		map[string]any{"old": "def f():\n    return 1\n", "new": "def f():\n    return helper()\n\ndef helper():\n    return 1\n"},
		map[string]any{"old": "def helper():\n    return 1\n", "new": "def helper():\n    return 10\n"},
		map[string]any{"old": "return 2", "new": "return 20"},
	))
	if err != nil {
		t.Fatal(err)
	}
	after := "def f():\n    return helper()\n\ndef helper():\n    return 10\n\ndef g():\n    return 20\n"
	if got := readFile(t, file); got != after {
		t.Fatalf("file:\n%s", got)
	}
	if len(h.prompts) != 1 {
		t.Fatalf("%d confirms for one call", len(h.prompts))
	}
	if want := Unified("code.py", before, after, 3); h.prompts[0].Detail != want {
		t.Fatalf("preview:\n%s\nwant one diff of the whole call:\n%s", h.prompts[0].Detail, want)
	}
}

// Nothing is written unless every edit applies. A failure names the edit
// and quotes the text it was matched against, which includes what the
// edits before it inserted.
func TestFsEditEditsAllOrNothing(t *testing.T) {
	h := newHarness(t)
	h.answer = true
	file := filepath.Join(h.root, "conf.py")
	before := "a = 1\nb = 2\n"
	writeFile(t, file, before)
	err := h.callJSON(t, "fs-edit", editsJSON(t, "conf.py",
		map[string]any{"old": "a = 1", "new": "a = 10"},
		map[string]any{"old": "c = 3", "new": "c = 30"},
	))
	if err == nil || !strings.Contains(err.Error(), "edit 2 of 2 failed, so nothing was written") || !strings.Contains(err.Error(), "old text not found") {
		t.Fatalf("a failing second edit: %v", err)
	}
	if !strings.HasPrefix(h.prompts[0].Detail, "edit would fail: edit 2 of 2 failed") {
		t.Fatalf("preview: %s", h.prompts[0].Detail)
	}
	err = h.callJSON(t, "fs-edit", editsJSON(t, "conf.py",
		map[string]any{"old": "b = 2\n", "new": "b = 2\nc = 3\n"},
		map[string]any{"old": "c  =  3", "new": "c = 30"},
		map[string]any{"old": "a = 1", "new": "a = 10"},
	))
	if err == nil || !strings.Contains(err.Error(), "edit 2 of 3 failed") || !strings.Contains(err.Error(), "Those lines are exactly:\nc = 3\n") {
		t.Fatalf("a whitespace miss on inserted text: %v", err)
	}
	if got := readFile(t, file); got != before {
		t.Fatalf("a failed call changed the file: %q", got)
	}
}

// replace_all belongs to the edit that gives it, not to the call.
func TestFsEditEditsReplaceAllPerEdit(t *testing.T) {
	h := newHarness(t)
	h.answer = true
	file := filepath.Join(h.root, "vars.py")
	before := "x = 1\ny = x\nz = x\nw = 2\nw = 3\n"
	writeFile(t, file, before)
	err := h.callJSON(t, "fs-edit", editsJSON(t, "vars.py",
		map[string]any{"old": "= x", "new": "= v", "replace_all": true},
		map[string]any{"old": "w", "new": "u"},
	))
	if err == nil || !strings.Contains(err.Error(), "edit 2 of 2") || !strings.Contains(err.Error(), "occurs 2 times") {
		t.Fatalf("the second edit has no replace_all: %v", err)
	}
	if got := readFile(t, file); got != before {
		t.Fatalf("a failed call changed the file: %q", got)
	}
	if err := h.callJSON(t, "fs-edit", editsJSON(t, "vars.py",
		map[string]any{"old": "= x", "new": "= v", "replace_all": true},
		map[string]any{"old": "w = 2", "new": "u = 2", "replace_all": false},
	)); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, file); got != "x = 1\ny = v\nz = v\nu = 2\nw = 3\n" {
		t.Fatalf("file: %q", got)
	}
}

// A call gives old and new or edits: both, neither, an empty list or an
// edit that is not {old, new} is an error that says so, and writes nothing.
func TestFsEditOneFormOnly(t *testing.T) {
	h := newHarness(t)
	h.answer = true
	file := filepath.Join(h.root, "f.txt")
	writeFile(t, file, "a\n")
	for raw, want := range map[string]string{
		`{"path":"f.txt"}`:                                                                     "neither was given",
		`{"path":"f.txt","old":"a"}`:                                                           "new is missing",
		`{"path":"f.txt","edits":[]}`:                                                          "edits is empty",
		`{"path":"f.txt","edits":[{"old":"a"}]}`:                                               "edit 1: an edit needs both old and new",
		`{"path":"f.txt","edits":["a"]}`:                                                       "edit 1: expected an object with old and new",
		`{"path":"f.txt","old":"a","new":"b","edits":[{"old":"a","new":"b"}]}`:                 "not both",
		`{"path":"f.txt","new":"b","edits":[{"old":"a","new":"b"}]}`:                           "not both",
		`{"path":"f.txt","replace_all":true,"edits":[{"old":"a","new":"b"}]}`:                  "replace_all goes inside each edit",
		`{"path":"f.txt","edits":[{"old":"a","new":"b"},{"old":"a","new":"b","content":"c"}]}`: `edit 2: unknown field "content"`,
		`{"path":"f.txt","edits":[{"old":"a","new":"b","new":"c"}]}`:                           "edit 1: new is given twice",
		`{"path":"f.txt","edits":[{"old":"a","new":"b","replace_all":"yes"}]}`:                 "replace_all must be true or false",
	} {
		if err := h.callJSON(t, "fs-edit", raw); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: got %v, want %q", raw, err, want)
		}
	}
	// From Scheme the list is the fifth argument, after the empty values a
	// JSON call that skips old, new and replace_all gets. Its elements are
	// alists with string keys, as json-parse makes them.
	for src, want := range map[string]string{
		`(fs-edit "f.txt" "" "" #f '(((old . "a") (new . "b"))))`:       "edit 1: expected an object with old and new",
		`(fs-edit "f.txt" "" "" #f '((("old" . "a") ("new" . 1))))`:     "edit 1: new must be a string",
		`(fs-edit "f.txt" "" "" #f "a")`:                                "edits must be a list",
		`(fs-edit "f.txt")`:                                             "neither was given",
		`(fs-edit "f.txt" "a" "b" #f '((("old" . "a") ("new" . "b"))))`: "not both",
	} {
		if _, err := h.eval(t, src); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: got %v, want %q", src, err, want)
		}
	}
	if got := readFile(t, file); got != "a\n" {
		t.Fatalf("a refused call changed the file: %q", got)
	}
	h.mustEval(t, `(fs-edit "f.txt" "" "" #f '((("old" . "a") ("new" . "b")) (("old" . "b") ("new" . "c") ("replace_all" . #t))))`)
	if got := readFile(t, file); got != "c\n" {
		t.Fatalf("the Scheme form: %q", got)
	}
}

// One call is one write: the file's previous content is kept once, however
// many edits the call makes.
func TestFsEditEditsKeepOneVersion(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "notes.md")
	writeFile(t, file, "one\ntwo\nthree\n")
	var kept []Version
	o := DefaultOptions(root)
	o.Versions = &Versions{Dir: VersionsDir(root), Record: func(v Version) { kept = append(kept, v) }}
	in := scheme.New()
	if err := Register(in, o); err != nil {
		t.Fatal(err)
	}
	p, _ := in.Lookup("fs-edit")
	args, err := p.ArgsFromJSON([]byte(`{"path":"notes.md","edits":[{"old":"one","new":"1"},{"old":"two","new":"2"},{"old":"three","new":"3"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := in.Apply(context.Background(), scheme.Value{Kind: scheme.KPrim, Prim: p}, args); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, file); got != "1\n2\n3\n" {
		t.Fatalf("file: %q", got)
	}
	list, err := ListVersions(o.Versions.Dir, file)
	if err != nil || len(kept) != 1 || len(list) != 1 {
		t.Fatalf("versions kept: %d recorded, %d listed, %v", len(kept), len(list), err)
	}
	if prev, err := ReadVersion(o.Versions.Dir, list[0].Hash); err != nil || string(prev) != "one\ntwo\nthree\n" {
		t.Fatalf("the kept version: %q %v", prev, err)
	}
}

// The tool list shows an edit's fields, so the model can see what edits
// holds; old and new are no longer required, since edits replaces them.
func TestFsEditSchemaShowsEditFields(t *testing.T) {
	p := fsEdit(DefaultOptions(t.TempDir()), guard.RepoPin{})
	b, err := json.Marshal(p.Schema())
	if err != nil {
		t.Fatal(err)
	}
	type prop struct {
		Type  string `json:"type"`
		Items *struct {
			Type       string          `json:"type"`
			Properties map[string]prop `json:"properties"`
			Required   []string        `json:"required"`
		} `json:"items"`
	}
	var s struct {
		Properties map[string]prop `json:"properties"`
		Required   []string        `json:"required"`
	}
	if err := json.Unmarshal(b, &s); err != nil {
		t.Fatal(err)
	}
	edits := s.Properties["edits"]
	if edits.Type != "array" || edits.Items == nil || edits.Items.Type != "object" {
		t.Fatalf("edits: %s", b)
	}
	f := edits.Items.Properties
	if f["old"].Type != "string" || f["new"].Type != "string" || f["replace_all"].Type != "boolean" || !slices.Equal(edits.Items.Required, []string{"old", "new"}) {
		t.Fatalf("an edit's fields: %s", b)
	}
	if !slices.Equal(s.Required, []string{"path"}) {
		t.Fatalf("required: %v", s.Required)
	}
}

// The guard judges a call with edits as it judges one edit: each edit's
// new is what is written, so a credential in the second is a security
// confirm, while the old text an edit replaces is not written and is not
// scanned; there is still one path, so standing write approvals and
// protected paths apply to it unchanged.
func TestFsEditEditsGuard(t *testing.T) {
	h := newHarness(t)
	p, _ := h.in.Lookup("fs-edit")
	args := func(raw string) []scheme.Value {
		t.Helper()
		a, err := p.ArgsFromJSON([]byte(raw))
		if err != nil {
			t.Fatal(err)
		}
		return a
	}
	ctx := context.Background()
	const key = "sk-ant-api03-AbCdEfGhIjKlMnOpQrStUv"
	secret := guard.TextSecret{Scanner: l1.New()}
	single := secret.Check(ctx, p, args(`{"path":"README.md","old":"world","new":"x-api-key: `+key+`"}`))
	second := secret.Check(ctx, p, args(`{"path":"README.md","edits":[{"old":"hello","new":"hi"},{"old":"world","new":"x-api-key: `+key+`"}]}`))
	if single.Verdict != guard.Confirm || !single.Security || second.Verdict != single.Verdict || second.Security != single.Security || second.Reason != single.Reason {
		t.Fatalf("a key in the second edit: %+v, in a single edit: %+v", second, single)
	}
	if d := secret.Check(ctx, p, args(`{"path":"README.md","edits":[{"old":"x-api-key: `+key+`","new":"x-api-key: (in the keychain)"}]}`)); d.Verdict != guard.Allow {
		t.Fatalf("a key only in the text an edit removes: %+v", d)
	}
	one := `,"edits":[{"old":"a","new":"b"},{"old":"b","new":"c"}]}`
	writes := guard.NewWriteAllow("docs/")
	if d := writes.Check(ctx, p, args(`{"path":"docs/a.md"`+one)); d.Verdict != guard.Allow || !d.Standing {
		t.Fatalf("a standing write approval: %+v", d)
	}
	if d := writes.Check(ctx, p, args(`{"path":"src/a.md"`+one)); d.Standing {
		t.Fatalf("approved outside its prefix: %+v", d)
	}
	if d := (guard.ProtectedPaths{Root: h.root}).Check(ctx, p, args(`{"path":".git/hooks/pre-commit"`+one)); d.Verdict != guard.Confirm || !d.Security {
		t.Fatalf("a protected path: %+v", d)
	}
}
