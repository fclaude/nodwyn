package plain

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fclaude/nodwyn/internal/config"
	"github.com/fclaude/nodwyn/internal/core"
	"github.com/fclaude/nodwyn/internal/llm"
	fake "github.com/fclaude/nodwyn/test/fakes/ollama"
)

// Line mode's :eval goes through the session's recorded entry, as the TUI's
// and the console's do, so the log holds the Scheme the person ran and what
// it gave (P2-041). It used to call the unrecorded Session.Eval.
func TestLineModeEvalIsRecorded(t *testing.T) {
	srv := fake.NewT(t)
	defer srv.Close()
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, "notes.md"), []byte("# Notes\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.Control.URL, cfg.Control.Model = srv.URL, "test-model"
	var logged bytes.Buffer
	s, err := core.New(core.Options{Config: cfg, Repo: repo, Version: "test", Provider: llm.NewOllama(srv.URL), NoConfirm: true, LogWriter: &logged})
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	chat := &Chat{Session: s, In: strings.NewReader(":eval (+ 1 2)\n:eval (error \"boom\")\n"), Out: &out}
	done := make(chan error, 1)
	go func() { done <- chat.Run(context.Background()) }()
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatalf("line mode did not return:\n%s", out.String())
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); !strings.Contains(got, "you> 3\n") || !strings.Contains(got, "error: boom") {
		t.Fatalf("the results were not shown:\n%s", got)
	}
	for _, want := range []string{`"kind":"eval.req"`, `(+ 1 2)`, `"kind":"eval.result"`} {
		if !strings.Contains(logged.String(), want) {
			t.Fatalf("the log lacks %s:\n%s", want, logged.String())
		}
	}
}
