package tui

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/fclaude/nodwyn/internal/event"
	"github.com/fclaude/nodwyn/internal/guard"
	"github.com/fclaude/nodwyn/internal/llm"
	"github.com/fclaude/nodwyn/internal/log"
	fake "github.com/fclaude/nodwyn/test/fakes/ollama"
)

func TestComposerNewlinesPasteResizeAndBusyDraft(t *testing.T) {
	m := testModel()
	m.Update(tea.PasteMsg{Content: "first\r\nsecond\rthird"})
	m.Update(tea.KeyPressMsg{Code: tea.KeyEnter, Mod: tea.ModAlt})
	m.Update(tea.PasteMsg{Content: "fourth"})
	m.Update(tea.KeyPressMsg{Code: 'j', Mod: tea.ModCtrl})
	want := "first\nsecond\nthird\nfourth\n"
	if m.composer.Value() != want || m.busy {
		t.Fatalf("newline/paste changed text or sent it: %q", m.composer.Value())
	}
	// More than the textarea's default 99 lines must survive viewport changes.
	m.Update(tea.PasteMsg{Content: strings.Repeat("long line with unicode λ界\n", 120)})
	want = m.composer.Value()
	for _, size := range []tea.WindowSizeMsg{{Width: 30, Height: 8}, {Width: 80, Height: 24}} {
		m.Update(size)
		if m.composer.Value() != want || m.composer.LineCount() != 125 {
			t.Fatal("resize truncated draft")
		}
		if lines := strings.Count(m.View().Content, "\n") + 1; lines > size.Height {
			t.Fatalf("view needs %d lines in height %d", lines, size.Height)
		}
	}
	m.busy = true
	_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd != nil || m.composer.Value() != want {
		t.Fatal("busy submit lost or sent draft")
	}
}

func TestPromptKeepsComposerCursorAndDisconnectKeepsDraft(t *testing.T) {
	m := testModel()
	m.Update(tea.PasteMsg{Content: "first\nsecond"})
	m.Update(tea.KeyPressMsg{Code: tea.KeyLeft})
	line, col := m.composer.Line(), m.composer.Column()
	m.consume(wire(event.PromptConfirm, event.Confirm{ID: "p", Prompt: guard.Prompt{Prim: "fs-write", Path: "note"}}, 0))
	m.Update(tea.PasteMsg{Content: "y"})
	if m.promptInput.Value() != "y" || m.composer.Value() != "first\nsecond" {
		t.Fatal("prompt input mixed with draft")
	}
	m.clearPrompt("p")
	if m.composer.Line() != line || m.composer.Column() != col || !m.composer.Focused() {
		t.Fatal("prompt changed draft cursor or focus")
	}
	m.Update(nextResult{err: errors.New("EOF"), generation: m.generation})
	m.restore(snapshot{ID: "session", Repo: "/repo"})
	if m.composer.Value() != "first\nsecond" || m.composer.Column() != col {
		t.Fatal("reconnect changed unsent draft")
	}
}

func TestMultilineSubmissionThroughBothBackends(t *testing.T) {
	for _, mode := range []string{"local", "rpc"} {
		t.Run(mode, func(t *testing.T) {
			o := testOptions(t)
			server := fake.New(fake.Turn{Content: "received"})
			defer server.Close()
			o.Provider = llm.NewOllama(server.URL)
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
			for _, text := range []string{"first line\n\nlast line λ", ":eval (begin\n; keep the next expression outside the comment\n(+ 20 22))"} {
				m.Update(tea.PasteMsg{Content: text})
				if m.busy {
					t.Fatal("paste submitted without Enter")
				}
				_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
				if cmd == nil {
					t.Fatal("Enter did not submit")
				}
				result := cmd().(actionResult)
				if result.err != nil {
					t.Fatal(result.err)
				}
				m.Update(result)
				completed := false
				for {
					ev, err := b.Next(ctx)
					if err != nil {
						t.Fatal(err)
					}
					m.consume(ev)
					if ev.Kind == string(log.KindLLMResp) || ev.Kind == string(log.KindEvalResult) {
						completed = true
					}
					if completed && ev.Kind == event.Status && !m.busy {
						break
					}
				}
				if m.composer.Value() != "" {
					t.Fatal("submitted draft retained")
				}
			}
			history, err := b.History(ctx, snap.ID)
			if err != nil {
				t.Fatal(err)
			}
			var message, source, value string
			for _, e := range history {
				switch e.Kind {
				case log.KindUserMsg:
					p, _ := decode[log.UserMsg](e.Payload)
					message = p.Text
				case log.KindEvalReq:
					p, _ := decode[log.EvalRequest](e.Payload)
					source = p.Source
				case log.KindEvalResult:
					p, _ := decode[log.EvalResult](e.Payload)
					value = p.Value
				}
			}
			if message != "first line\n\nlast line λ" || source != "(begin\n; keep the next expression outside the comment\n(+ 20 22))" || value != "42" {
				t.Fatalf("multiline submission changed: %q, %q, %q", message, source, value)
			}
		})
	}
}
