package core

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"github.com/fclaude/nodwyn/internal/config"
	"github.com/fclaude/nodwyn/internal/llm"
	fake "github.com/fclaude/nodwyn/test/fakes/ollama"
)

func TestProjectNotesSurviveCanonicalPaths(t *testing.T) {
	base := t.TempDir()
	repo, alias := filepath.Join(base, "repo"), filepath.Join(base, "alias")
	if err := os.Mkdir(repo, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(repo, alias); err != nil {
		t.Fatal(err)
	}
	paths := config.PathsUnder(filepath.Join(base, "config"), filepath.Join(base, "data"))
	sum := sha256.Sum256([]byte(alias))
	old := filepath.Join(paths.DataDir, "projects", hex.EncodeToString(sum[:8]), "notes.md")
	if err := os.MkdirAll(filepath.Dir(old), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(old, []byte("keep these notes"), 0600); err != nil {
		t.Fatal(err)
	}
	srv := fake.NewT(t)
	defer srv.Close()
	s, err := New(Options{Config: config.Default(), Paths: paths, Repo: alias, Provider: llm.NewOllama(srv.URL), LogWriter: &bytes.Buffer{}})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if got, err := os.ReadFile(notesFile(paths.DataDir, repo)); err != nil || string(got) != "keep these notes" {
		t.Fatalf("notes lost: %q %v", got, err)
	}
	if got, err := os.ReadFile(old + ".before-path-migration"); err != nil || string(got) != "keep these notes" {
		t.Fatalf("backup missing: %q %v", got, err)
	}
}
