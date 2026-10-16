package plain

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fclaude/nodwyn/internal/binding"
	"github.com/fclaude/nodwyn/internal/config"
	"github.com/fclaude/nodwyn/internal/core"
	"github.com/fclaude/nodwyn/internal/guard"
	"github.com/fclaude/nodwyn/internal/llm"
	"github.com/fclaude/nodwyn/internal/log"
	fake "github.com/fclaude/nodwyn/test/fakes/ollama"
)

// The question lists the answers the guard offers, each independently, as
// the TUI does: a prompt with a binding proposal and a command offers the
// binding and the session scope, not only the binding.
func TestApprovalQuestionListsWhatTheGuardOffers(t *testing.T) {
	row := binding.Binding{Class: "service", Dest: "api.test", Methods: []string{"POST"}, Positions: []string{"body"}}
	for _, c := range []struct {
		name   string
		prompt guard.Prompt
		want   string
	}{
		{"plain", guard.Prompt{Prim: "http-request"}, "allow? [y/N] "},
		{"security", guard.Prompt{Prim: "fs-write", Path: "notes/a", Security: true}, "allow? [y/N] "},
		{"protected", guard.Prompt{Prim: "fs-write", Path: ".nodwyn/allow.toml"}, "allow? [y/N] "},
		{"write", guard.Prompt{Prim: "fs-write", Path: "notes/a"}, "allow? [y/N/a=always under notes//t=all writes this turn] "},
		{"command", guard.Prompt{Prim: "shell", Argv: []string{"go", "test"}}, "allow? [y/N/a=always go test this session] "},
		{"command with a binding", guard.Prompt{Prim: "shell", Argv: []string{"go", "test"}, Binding: &row}, "allow? [y/N/b=bind this exact scope persistently and allow/a=always go test this session] "},
	} {
		if got := approvalQuestion(c.prompt.ApprovalOptions()); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
}

// A wider answer is the explicit scope operation, attributed to the
// terminal, not a loose answer line: the approval record names its actor.
func TestLineModeWiderApprovalIsAnAttributedScope(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "work")
	if err := os.MkdirAll(filepath.Join(repo, "notes"), 0o755); err != nil {
		t.Fatal(err)
	}
	srv := fake.NewT(t,
		fake.Turn{ToolCalls: []fake.ToolCall{{ID: "w", Name: "fs-write", Args: map[string]any{"path": "notes/a.txt", "content": "a"}}}},
		fake.Turn{Content: "done"})
	defer srv.Close()
	cfg := config.Default()
	cfg.Approval.Writes = config.ApproveAsk
	cfg.Control.URL, cfg.Control.Model = srv.URL, "test-model"
	paths := config.PathsUnder(filepath.Join(root, "config"), filepath.Join(root, "data"))
	s, err := core.New(core.Options{Config: cfg, Repo: repo, Paths: paths, Version: "test", Provider: llm.NewOllama(srv.URL), LogWriter: &bytes.Buffer{}})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var out bytes.Buffer
	chat := &Chat{Session: s, In: strings.NewReader("write a note\na\n"), Out: &out}
	done := make(chan error, 1)
	go func() { done <- chat.Run(context.Background()) }()
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatalf("line mode did not finish:\n%s", out.String())
	}
	if got := strings.Join(s.AllowedWrites(), ","); got != "notes/" {
		t.Fatalf("standing writes %q:\n%s", got, out.String())
	}
	history, err := s.History()
	if err != nil {
		t.Fatal(err)
	}
	for _, ev := range history {
		if ev.Kind != log.KindApprovalChange {
			continue
		}
		var change log.ApprovalChange
		if err := json.Unmarshal(ev.Payload, &change); err != nil {
			t.Fatal(err)
		}
		if change.Actor != "terminal" || change.Scope != "session" {
			t.Fatalf("approval record %+v", change)
		}
		return
	}
	t.Fatal("no approval record")
}
