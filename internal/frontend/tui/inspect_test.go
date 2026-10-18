package tui

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/fclaude/nodwyn/internal/binding"
	"github.com/fclaude/nodwyn/internal/core"
	"github.com/fclaude/nodwyn/internal/event"
	"github.com/fclaude/nodwyn/internal/guard"
	"github.com/fclaude/nodwyn/internal/log"
	"github.com/fclaude/nodwyn/internal/scheme"
	"github.com/fclaude/nodwyn/test/fakes/must"
)

func TestInspectorsWorkOverTheDaemon(t *testing.T) {
	o := testOptions(t)
	o.Paths.Bindings = "" // explicitly in-memory test policy
	o.Bindings = binding.Table{Bind: []binding.Binding{{Class: "service", Dest: "api.example.test", Methods: []string{"POST"}, Positions: []string{"header:Authorization"}}}}
	must.MkdirAll(t, filepath.Join(o.Repo, "docs"), 0700)
	const canary = "sk-ant-abcdefgh1234"
	must.WriteFile(t, filepath.Join(o.Repo, "docs", "friction.md"), []byte("# Friction\nline with "+canary+"\n\x1b[2Jterminal escape"), 0600)
	b, _ := testRemote(t, o)
	defer b.Close()
	ctx := context.Background()
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
	for _, command := range []string{":bindings", ":log", ":replay"} {
		cmd := m.inspect(command)
		if cmd == nil {
			t.Fatal("inspector not opened")
		}
		m.Update(cmd())
		if m.inspector.loading || strings.Contains(m.inspector.content, "Cannot inspect") {
			t.Fatal(m.inspector.content)
		}
		if command == ":bindings" && !strings.Contains(m.inspector.content, "api.example.test") {
			t.Fatal("loaded policy missing")
		}
	}
	after, err := b.History(ctx, snap.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(before) != len(after) {
		t.Fatal("metadata/replay views appended to history")
	}
	cmd := m.inspect(":friction")
	m.Update(cmd())
	for {
		ev, err := b.Next(ctx)
		if err != nil {
			t.Fatal(err)
		}
		m.consume(ev)
		if ev.Kind == event.Status {
			status, _ := decode[event.StatusInfo](ev.Payload)
			if status.State == "idle" {
				break
			}
		}
	}
	if m.inspector.loading || !strings.Contains(m.inspector.content, "# Friction") || strings.Contains(m.inspector.content, canary) || strings.Contains(m.inspector.content, "\x1b") {
		t.Fatal("unsafe/incomplete friction view")
	}
	history, err := b.History(ctx, snap.ID)
	if err != nil {
		t.Fatal(err)
	}
	guarded := false
	for _, e := range history {
		if e.Kind == log.KindGuardDecision {
			var p log.GuardDecision
			e.Decode(&p)
			if p.Prim == "fs-read" && p.Origin == "user" {
				guarded = true
			}
		}
	}
	if !guarded {
		t.Fatal("friction bypassed guard")
	}
}
func TestInspectorDoesNotHidePromptOrReopenAfterEscape(t *testing.T) {
	m := testModel()
	m.inspectionToken = 1
	m.inspector = &inspector{title: "log", token: 1, loading: true}
	m.consume(wire(event.PromptConfirm, event.Confirm{ID: "p", Prompt: guard.Prompt{Prim: "fs-write", Path: "notes/a", Detail: "review this diff"}}, 0))
	m.Update(inspectResult{token: 1, content: "log payload", generation: m.generation})
	if !strings.Contains(m.viewport.GetContent(), "review this diff") {
		t.Fatal("late inspector hid prompt")
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.prompt == nil {
		t.Fatal("Escape dismissed authority prompt")
	}
	m.clearPrompt("p")
	m.refresh(false)
	if !strings.Contains(m.viewport.GetContent(), "log payload") {
		t.Fatal("inspector did not survive prompt")
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	m.Update(inspectResult{token: 1, content: "late result", generation: m.generation})
	if m.inspector != nil {
		t.Fatal("late result reopened inspector")
	}
}
func TestFrictionCaptureRequiresMatchingEvaluation(t *testing.T) {
	m := testModel()
	m.inspector = &inspector{title: "friction", reading: true, loading: true}
	m.frictionResult(log.EvalResult{Value: scheme.Write(scheme.Str("unrelated"))})
	if !m.inspector.loading {
		t.Fatal("captured an unrelated result")
	}
	m.frictionRequest("(+ 1 2)")
	m.frictionResult(log.EvalResult{Value: "3"})
	if m.inspector.reading || strings.Contains(m.inspector.content, "3") {
		t.Fatal("misattributed another evaluation")
	}
}
func TestLogFilterAndFixedPagination(t *testing.T) {
	for _, bad := range []string{":log 0", ":log 1001", ":log 5 guard. extra", ":replay other"} {
		if _, _, err := logFilter(strings.Fields(bad)); err == nil {
			t.Fatal("accepted", bad)
		}
	}
	n, kind, err := logFilter(strings.Fields(":log 1 guard."))
	if err != nil || n != 1 || kind != "guard." {
		t.Fatal("filter parse")
	}
	events := []log.Event{{Seq: 1, Kind: log.KindNote, Payload: json.RawMessage(`{"text":"omit"}`)}, {Seq: 2, Kind: log.KindGuardDecision, Payload: json.RawMessage(`{"prim":"first"}`)}, {Seq: 3, Kind: log.KindGuardDecision, Payload: json.RawMessage(`{"prim":"last"}`)}}
	text := formatLog(events, n, kind)
	if strings.Contains(text, "first") || strings.Contains(text, "omit") || !strings.Contains(text, "last") {
		t.Fatal(text)
	}
	calls := 0
	result, err := readHistory(context.Background(), func(after, through uint64) (core.LogPage, error) {
		calls++
		if calls == 1 {
			if after != 0 || through != 0 {
				t.Fatal("initial cursor")
			}
			return core.LogPage{Events: events[:1], Next: 1, Through: 3, More: true}, nil
		}
		if after != 1 || through != 3 {
			t.Fatal("snapshot watermark changed")
		}
		return core.LogPage{Events: events[1:], Next: 3, Through: 3}, nil
	})
	if err != nil || len(result) != 3 || calls != 2 {
		t.Fatal("paging failed", err)
	}
}

func TestFrictionInspectorHonorsGuardAndRepositoryBoundary(t *testing.T) {
	for _, denial := range []string{"checker", "symlink"} {
		t.Run(denial, func(t *testing.T) {
			o := testOptions(t)
			if err := os.MkdirAll(filepath.Join(o.Repo, "docs"), 0700); err != nil {
				t.Fatal(err)
			}
			target := filepath.Join(o.Repo, "docs", "friction.md")
			if denial == "checker" {
				if err := os.WriteFile(target, []byte("must-not-read"), 0600); err != nil {
					t.Fatal(err)
				}
				o.InitFile = filepath.Join(o.Repo, ".nodwyn", "init.scm")
				if err := os.MkdirAll(filepath.Dir(o.InitFile), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(o.InitFile, []byte(`(register-check "no-reads" "deny" (lambda (name args) (equal? name "fs-read")))`), 0600); err != nil {
					t.Fatal(err)
				}
			} else {
				outside := filepath.Join(t.TempDir(), "outside.md")
				if err := os.WriteFile(outside, []byte("must-not-read"), 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(outside, target); err != nil {
					t.Fatal(err)
				}
			}
			b, _ := testRemote(t, o)
			defer b.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			snap, err := b.Attach(ctx, o.Repo, "")
			if err != nil {
				t.Fatal(err)
			}
			m := newModel(ctx, b, o.Repo, snap.ID, false)
			m.restore(snap)
			m.Update(m.inspect(":friction")())
			for {
				ev, err := b.Next(ctx)
				if err != nil {
					t.Fatal(err)
				}
				m.consume(ev)
				if ev.Kind == event.Status {
					state, _ := decode[event.StatusInfo](ev.Payload)
					if state.State == "idle" {
						break
					}
				}
			}
			if !strings.Contains(m.inspector.content, "Friction read failed") || strings.Contains(m.inspector.content, "must-not-read") {
				t.Fatal("inspector bypassed read policy")
			}
		})
	}
}
