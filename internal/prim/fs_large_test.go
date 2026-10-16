package prim

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fclaude/nodwyn/internal/guard"
	"github.com/fclaude/nodwyn/internal/scheme"
)

// fs-edit read a file whole however large it was (a 500 MB log, scanned for
// credentials several times over), and the fs-write preview read it to diff
// it. Past what a version can hold they say so instead.
func TestFsEditAndTheWritePreviewRefuseAFileTooLarge(t *testing.T) {
	root := t.TempDir()
	big := filepath.Join(root, "big.log")
	if err := os.WriteFile(big, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(big, maxVersionFile+1); err != nil {
		t.Fatal(err)
	}
	in := scheme.New()
	if err := Register(in, DefaultOptions(root)); err != nil {
		t.Fatal(err)
	}
	_, err := in.EvalString(context.Background(), `(fs-edit "big.log" "a" "b")`)
	if err == nil || !strings.Contains(err.Error(), "too large to edit") {
		t.Fatalf("fs-edit: %v", err)
	}
	got := previewWrite(guard.RepoPin{Root: root}, []scheme.Value{scheme.Str("big.log"), scheme.Str("new\n")})
	if !strings.Contains(got, "too large to show a diff") {
		t.Fatalf("preview: %q", got)
	}
}
