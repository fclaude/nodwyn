package plain

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fclaude/nodwyn/internal/binding"
	"github.com/fclaude/nodwyn/internal/config"
	"github.com/fclaude/nodwyn/internal/core"
	"github.com/fclaude/nodwyn/internal/keychain"
	"github.com/fclaude/nodwyn/internal/llm"
	fakedrive "github.com/fclaude/nodwyn/test/fakes/drive"
	fake "github.com/fclaude/nodwyn/test/fakes/ollama"
)

// When the input ends at a question with set answers (how uploads to
// Google Docs are handled, asked the first time), line mode stops the turn
// and returns. It used to wait for the session to decline the question as
// it closed, which it does only once the turn waiting on the answer ends:
// line mode waited forever.
func TestInputEndingAtAChoiceEndsTheTurn(t *testing.T) {
	drive := fakedrive.New()
	defer drive.Close()
	root := t.TempDir()
	repo := filepath.Join(root, "work")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "notes.md"), []byte("# Notes\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	paths := config.PathsUnder(filepath.Join(root, "config"), filepath.Join(root, "data"))
	if err := os.MkdirAll(paths.ConfigDir, 0o700); err != nil {
		t.Fatal(err)
	}
	store, err := keychain.Create(filepath.Join(root, "keychain"), keychain.FastKDF, []byte("passphrase"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Add("google", []byte("ya29.test-only-token")); err != nil {
		t.Fatal(err)
	}
	row, _ := binding.Exact("google", "127.0.0.1", "POST", "header:Authorization:Bearer")
	if err := binding.AppendExact(context.Background(), paths.Bindings, row); err != nil {
		t.Fatal(err)
	}
	srv := fake.NewT(t, fake.Turn{ToolCalls: []fake.ToolCall{{ID: "a", Name: "google-doc-upload", Args: map[string]any{"path": "notes.md"}}}}, fake.Turn{Content: "stopped"})
	defer srv.Close()
	cfg := config.Default()
	cfg.Control.URL, cfg.Control.Model = srv.URL, "test-model"
	cfg.Docs.GoogleUploadURL = drive.UploadURL()
	s, err := core.New(core.Options{Config: cfg, Repo: repo, Paths: paths, Keychain: store, Version: "test", Provider: llm.NewOllama(srv.URL), LogWriter: &bytes.Buffer{}})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var out bytes.Buffer
	chat := &Chat{Session: s, In: strings.NewReader("upload the notes\n"), Out: &out}
	done := make(chan error, 1)
	go func() { done <- chat.Run(context.Background()) }()
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatalf("line mode still waits at the question:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "Uploads to Google Docs") || drive.Requests() != 0 {
		t.Fatalf("the question was not asked, or the upload went:\n%s", out.String())
	}
}
