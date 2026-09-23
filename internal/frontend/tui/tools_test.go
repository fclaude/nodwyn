package tui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/fclaude/nibb/internal/event"
	"github.com/fclaude/nibb/internal/guard"
	"github.com/fclaude/nibb/internal/llm"
)

func TestToolInspectionThroughBothBackends(t *testing.T) {
	for _, mode := range []string{"local", "rpc"} {
		t.Run(mode, func(t *testing.T) {
			o := testOptions(t)
			if err := os.MkdirAll(filepath.Join(o.Repo, ".nibb"), 0700); err != nil {
				t.Fatal(err)
			}
			init := `(register-tool "greet" (lambda (name) (error "must not execute"))
  '((doc . "Greet a person") (params . (("name" "string" "who")))))`
			if err := os.WriteFile(filepath.Join(o.Repo, ".nibb", "init.scm"), []byte(init), 0600); err != nil {
				t.Fatal(err)
			}
			var b backend
			if mode == "local" {
				b = newLocal(o)
			} else {
				b, _ = testRemote(t, o)
			}
			defer b.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			snap, err := b.Attach(ctx, o.Repo, "")
			if err != nil {
				t.Fatal(err)
			}
			m := newModel(ctx, b, o.Repo, snap.ID, false)
			m.restore(snap)
			before, err := b.History(ctx, snap.ID)
			if err != nil {
				t.Fatal(err)
			}
			// Disk edits must not misrepresent the catalog already sent to the model.
			if err := os.WriteFile(filepath.Join(o.Repo, ".nibb", "init.scm"), nil, 0600); err != nil {
				t.Fatal(err)
			}
			for _, tc := range []struct{ command, want string }{
				{":tools", "Greet a person"},
				{":tools greet", `"name"`},
				{":tools +", "no model tool named"},
				{":allow", "Current command approvals"},
			} {
				m.Update(m.inspect(tc.command)())
				if m.inspector.loading || !strings.Contains(m.inspector.content, tc.want) {
					t.Fatalf("%s: %s", tc.command, m.inspector.content)
				}
			}
			after, err := b.History(ctx, snap.ID)
			if err != nil {
				t.Fatal(err)
			}
			if len(before) != len(after) {
				t.Fatal("tool or approval inspection executed or recorded an operation")
			}
		})
	}
}

func TestToolMetadataUsesSafeInspectorRendering(t *testing.T) {
	catalog := []llm.Tool{{Name: "example", Description: "sk-ant-abcdefgh1234\x1b[2J", Parameters: map[string]any{"type": "object", "description": "sk-ant-abcdefgh1234"}}}
	for _, name := range []string{"", "example"} {
		content, err := formatTools(catalog, name)
		if err != nil {
			t.Fatal(err)
		}
		m := testModel()
		m.inspector = &inspector{token: 1}
		m.Update(inspectResult{token: 1, generation: m.generation, content: content})
		if strings.Contains(m.inspector.content, "sk-ant-abcdefgh1234") || strings.Contains(m.inspector.content, "\x1b") {
			t.Fatal("unsafe metadata reached inspector")
		}
	}
	content, err := formatTools(nil, "")
	if err != nil || !strings.Contains(content, "0 tools") {
		t.Fatal("empty catalog is not explained")
	}
	if _, _, err := inspectorArgs(strings.Fields(":tools one two")); err == nil {
		t.Fatal("extra tool arguments ignored")
	}
}

func TestPlainCommandAliasesRespectPrompts(t *testing.T) {
	for _, command := range []string{":q", ":quit", ":exit"} {
		m := testModel()
		m.busy = true
		m.composer.SetValue(command)
		cmd := m.submit()
		if _, ok := cmd().(tea.QuitMsg); !ok {
			t.Fatal("quit alias does not use normal frontend shutdown")
		}
		m.consume(wire(event.PromptConfirm, event.Confirm{ID: "p", Prompt: guard.Prompt{Prim: "fs-write", Path: "note"}}, 0))
		m.promptInput.SetValue(command)
		if m.submit() != nil || m.prompt == nil {
			t.Fatal("command bypassed pending approval")
		}
	}
	m := testModel()
	m.composer.SetValue(":h")
	m.submit()
	if m.inspector == nil || m.inspector.title != "help" {
		t.Fatal("help alias failed")
	}
}
