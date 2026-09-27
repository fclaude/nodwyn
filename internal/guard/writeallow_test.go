package guard

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fclaude/nibb/internal/scheme"
)

var writePrim = &scheme.Prim{Name: "fs-edit", Meta: scheme.Meta{Doc: "d", Irreversible: true, Params: []scheme.Param{
	{Name: "path", Type: "string", Role: "path"}, {Name: "old", Type: "string"}, {Name: "new", Type: "string", Role: "payload"},
}}}

func writeArgs(path string) []scheme.Value {
	return []scheme.Value{scheme.Str(path), scheme.Str("a"), scheme.Str("b")}
}

func TestWriteAllowPrefixesAndProtected(t *testing.T) {
	w := NewWriteAllow("internal/", "docs/scheme.md", ".nibb/", "")
	if got := strings.Join(w.List(), ","); got != ".nibb/,docs/scheme.md,internal/" {
		t.Fatalf("list: %s", got)
	}
	for rel, want := range map[string]string{
		"internal/scheme/x.go": "internal/",
		"internal":             "",
		"docs/scheme.md":       "docs/scheme.md",
		"docs/other.md":        "",
		".nibb/allow.toml":     "", // protected even though listed
		"scripts/promote.sh":   "",
		"Makefile":             "",
		"internal/../.nibb/x":  "",
	} {
		if got := w.Match(rel); got != want {
			t.Errorf("Match(%q) = %q, want %q", rel, got, want)
		}
	}
	for rel, want := range map[string]string{"internal/scheme/x.go": "internal/scheme/", "README.md": "README.md"} {
		if got := DirPrefix(rel); got != want {
			t.Errorf("DirPrefix(%q) = %q, want %q", rel, got, want)
		}
	}
}

func TestStandingWriteOutranksConfirmAndLogsDetail(t *testing.T) {
	asked := 0
	g := New(ConfirmFunc(func(context.Context, Prompt) (bool, error) { asked++; return false, nil }), nil,
		MetaPolicy{}, fixed{"preview", Decision{Verdict: Confirm, Reason: "changes x", Detail: "--- diff ---"}}, NewWriteAllow("internal/"))
	if err := g.Before(context.Background(), writePrim, writeArgs("internal/a.go")); err != nil || asked != 0 {
		t.Fatalf("standing write: %v asked=%d", err, asked)
	}
	if err := g.Before(context.Background(), writePrim, writeArgs("cmd/main.go")); err == nil || asked != 1 {
		t.Fatalf("unapproved write should prompt: %v asked=%d", err, asked)
	}
	if err := g.Before(context.Background(), writePrim, writeArgs(".nibb/allow.toml")); err == nil || asked != 2 {
		t.Fatalf("protected path must prompt even if listed: %v asked=%d", err, asked)
	}
}

// A write to a protected path is a security confirm however the path is
// spelled: relative, absolute, through "..", in another case (macOS file
// systems ignore it), or through a symbolic link.
func TestProtectedPathsAreSecurityConfirms(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, ".git", "hooks"), 0o755)
	os.MkdirAll(filepath.Join(root, "src"), 0o755)
	if err := os.Symlink(filepath.Join(root, ".git"), filepath.Join(root, "src", "meta")); err != nil {
		t.Fatal(err)
	}
	pp := ProtectedPaths{Root: root}
	for _, path := range []string{
		".nibb/init.scm",
		"./.nibb/init.scm",
		filepath.Join(root, ".git", "hooks", "pre-commit"),
		"src/../.git/hooks/pre-commit",
		".GIT/hooks/pre-commit",
		"src/meta/hooks/pre-commit",
		"Makefile",
		"scripts/release.sh",
	} {
		d := pp.Check(context.Background(), writePrim, writeArgs(path))
		if d.Verdict != Confirm || !d.Security {
			t.Errorf("%s: %+v", path, d)
		}
	}
	for _, path := range []string{"src/main.go", "docs/scripts.md", "Makefile.bak"} {
		if d := pp.Check(context.Background(), writePrim, writeArgs(path)); d.Verdict != Allow {
			t.Errorf("%s should be an ordinary write: %+v", path, d)
		}
	}
	// A standing approval never waives it.
	g := New(AutoAllow, nil, pp, NewWriteAllow("src/"))
	if err := g.Before(context.Background(), writePrim, writeArgs("src/meta/hooks/pre-commit")); err == nil ||
		!strings.Contains(err.Error(), "unattended runs (--yolo) never approve it") {
		t.Fatalf("unattended protected write: %v", err)
	}
}
