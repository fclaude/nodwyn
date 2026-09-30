package guard

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/fclaude/nodwyn/internal/scheme"
)

// The scratch directory is a second root for absolute paths only: relative
// paths stay relative to the repository, and a link out of the scratch
// directory does not escape the pin.
func TestScratchIsASecondRootForAbsolutePaths(t *testing.T) {
	base := t.TempDir()
	repo, scratch := filepath.Join(base, "repo"), filepath.Join(base, "scratch")
	os.MkdirAll(repo, 0o755)
	os.MkdirAll(scratch, 0o755)
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(scratch, "out")); err != nil {
		t.Fatal(err)
	}
	pin := RepoPin{Root: repo, Scratch: scratch}
	for path, inside := range map[string]bool{
		filepath.Join(scratch, "repro.py"):        true,
		"src/main.go":                             true, // the repository, as always
		"../scratch/repro.py":                     false,
		filepath.Join(scratch, "out", "leak.txt"): false,
		filepath.Join(outside, "x"):               false,
	} {
		if got := pin.Resolve(path) != ""; got != inside {
			t.Errorf("%s: inside = %v, want %v", path, got, inside)
		}
	}

	sw := ScratchWrites{Dir: scratch}
	if d := sw.Check(context.Background(), writePrim, writeArgs(filepath.Join(scratch, "notes.md"))); d.Verdict != Allow || !d.Standing {
		t.Errorf("scratch write not approved: %+v", d)
	}
	for _, path := range []string{"notes.md", filepath.Join(outside, "notes.md")} {
		if d := sw.Check(context.Background(), writePrim, writeArgs(path)); d.Standing {
			t.Errorf("%s approved as a scratch write", path)
		}
	}

	shell := &scheme.Prim{Name: "shell-exec", Meta: scheme.Meta{Doc: "d", Irreversible: true, Params: []scheme.Param{
		{Name: "cmd", Type: "string", Role: "argv"}, {Name: "args", Type: "list", Role: "argv"}}}}
	argv := DefaultArgvTrigger(repo)
	argv.Scratch = scratch
	run := []scheme.Value{scheme.Str("python3"), scheme.List(scheme.Str(filepath.Join(scratch, "repro.py")))}
	if d := argv.Check(context.Background(), shell, run); d.Verdict != Allow {
		t.Errorf("a command naming a scratch file was flagged: %+v", d)
	}
	other := []scheme.Value{scheme.Str("python3"), scheme.List(scheme.Str(filepath.Join(outside, "x.py")))}
	if d := argv.Check(context.Background(), shell, other); d.Verdict != Confirm {
		t.Errorf("a command naming a path outside both was not flagged: %+v", d)
	}
}
