package guard

import (
	"context"
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
