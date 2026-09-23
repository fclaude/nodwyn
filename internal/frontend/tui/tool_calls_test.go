package tui

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fclaude/nibb/internal/event"
	"github.com/fclaude/nibb/internal/llm"
	"github.com/fclaude/nibb/internal/log"
	fake "github.com/fclaude/nibb/test/fakes/ollama"
)

func toolCards(m *model) []*toolCallView {
	var cards []*toolCallView
	for _, row := range m.transcript {
		if row.tool != nil {
			cards = append(cards, row.tool)
		}
	}
	return cards
}

func callRecord(id, name string) log.ToolCallEvent {
	return log.ToolCallEvent{ToolCall: log.ToolCall{ID: id, Name: name, Args: json.RawMessage(`{"path":"note"}`)}, Origin: "model"}
}

func TestToolCardsKeepReusedIDsAndSkippedCallsSeparate(t *testing.T) {
	m := testModel()
	m.consume(wire(string(log.KindToolCall), callRecord("same", "read"), 1))
	m.consume(wire(string(log.KindGuardDecision), log.GuardDecision{Prim: "read", Origin: "model", Decision: "allow"}, 2))
	result := wire(string(log.KindToolResult), log.ToolResult{ID: "same", Name: "read", OK: true, Value: "first value", Millis: 7}, 3)
	m.consume(result)
	m.consume(result) // A duplicate live/catch-up record must not add a card.
	m.consume(wire(string(log.KindToolCall), callRecord("same", "read"), 4))
	m.consume(wire(string(log.KindToolResult), log.ToolResult{ID: "same", Name: "read", Error: "second failed"}, 5))
	m.consume(wire(string(log.KindToolResult), log.ToolResult{ID: "skipped", Name: "write", Error: "Not executed: budget"}, 6))
	cards := toolCards(m)
	if len(cards) != 3 || len(cards[0].decisions) != 1 || len(cards[1].decisions) != 0 {
		t.Fatal("calls or decisions merged by reused ID")
	}
	for i, want := range []string{"first value", "second failed", "No matching call record"} {
		if !strings.Contains(cards[i].render(), want) {
			t.Fatalf("card %d missing %q", i, want)
		}
	}
	if cards[2].call != nil || strings.Contains(cards[2].render(), "awaiting result") {
		t.Fatal("skipped result appeared to execute")
	}
}

func TestToolCardIntervalsDoNotGuessMissingResults(t *testing.T) {
	m := testModel()
	m.consume(wire(string(log.KindToolCall), callRecord("one", "read"), 1))
	m.consume(wire(string(log.KindGuardDecision), log.GuardDecision{Prim: "nested", Origin: "model", Decision: "deny", Reason: "blocked"}, 2))
	m.consume(wire(string(log.KindGuardDecision), log.GuardDecision{Prim: "user-read", Origin: "user", Decision: "deny", Reason: "separate"}, 3))
	m.consume(wire(string(log.KindToolResult), log.ToolResult{ID: "one", Name: "different", OK: true, Value: "unmatched"}, 4))
	cards := toolCards(m)
	if len(cards) != 2 || cards[0].result != nil || !cards[0].ended || len(cards[0].decisions) != 1 {
		t.Fatal("mismatched result or origin was assigned to a call")
	}
	if !strings.Contains(cards[0].render(), "no result recorded") || cards[1].call != nil {
		t.Fatal("unmatched result presented as a completed call")
	}
	m.consume(wire(string(log.KindToolCall), callRecord("two", "read"), 5))
	m.consume(wire(string(log.KindTurnEnd), map[string]any{}, 6))
	if m.activeTool != nil || !toolCards(m)[2].ended {
		t.Fatal("turn end retained an active call")
	}
}

func TestToolCardsRestoreOpenIntervalsAndRenderSafely(t *testing.T) {
	const canary = "sk-ant-abcdefgh1234"
	call := callRecord("live", "read")
	call.Args = json.RawMessage(`{"path":"sk-ant-abcdefgh1234"}`)
	e := wire(string(log.KindToolCall), call, 1)
	snap := snapshot{ID: "session", Repo: "/repo", Busy: true, History: []log.Event{{Seq: 1, Kind: log.KindToolCall, Payload: e.Payload}}}
	m := testModel()
	m.restore(snap)
	if m.activeTool == nil || !strings.Contains(m.activeTool.render(), "awaiting result") {
		t.Fatal("catch-up lost an open call")
	}
	m.consume(wire(string(log.KindGuardDecision), log.GuardDecision{Prim: "read", Origin: "model", Decision: "confirm", Answer: "yes", Detail: canary + "\x1b[2J"}, 2))
	m.consume(wire(string(log.KindToolResult), log.ToolResult{ID: "live", Name: "read", OK: true, Value: canary + "\x1b]52;c;ATTACK\a\nvisible result"}, 3))
	text := toolCards(m)[0].render()
	if strings.Contains(text, canary) || strings.Contains(text, "\x1b") || strings.Contains(text, "ATTACK") || !strings.Contains(text, "visible result") {
		t.Fatal("unsafe or missing tool detail")
	}
	snap.Busy = false
	m.restore(snap)
	if m.activeTool != nil || !strings.Contains(toolCards(m)[0].render(), "no result recorded") {
		t.Fatal("idle history displayed an interrupted call as running")
	}
}

func TestToolCardsFromLocalAndRPCEvents(t *testing.T) {
	for _, mode := range []string{"local", "rpc"} {
		t.Run(mode, func(t *testing.T) {
			o := testOptions(t)
			if err := os.MkdirAll(filepath.Join(o.Repo, ".nibb"), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(o.Repo, ".nibb", "init.scm"), []byte(`(register-tool "read-note" (lambda () (fs-read "note")) '((doc . "Read note") (params . ())))`), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(o.Repo, "note"), []byte("visible result\nsk-ant-abcdefgh1234"), 0600); err != nil {
				t.Fatal(err)
			}
			server := fake.New(fake.Turn{ToolCalls: []fake.ToolCall{
				{ID: "read", Name: "read-note", Args: map[string]any{}},
				{ID: "write", Name: "fs-write", Args: map[string]any{"path": "declined", "content": "must not write"}},
			}}, fake.Turn{Content: "done"})
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
			if err := b.Send(ctx, snap.ID, "read and propose a write", false); err != nil {
				t.Fatal(err)
			}
			completed := false
			for {
				ev, err := b.Next(ctx)
				if err != nil {
					t.Fatal(err)
				}
				m.consume(ev)
				if ev.Kind == event.PromptConfirm {
					p, _ := decode[event.Confirm](ev.Payload)
					if m.prompt == nil {
						t.Fatal("tool card hid confirmation")
					}
					if err := b.Answer(ctx, snap.ID, p.ID, "n"); err != nil {
						t.Fatal(err)
					}
				}
				if ev.Kind == string(log.KindTurnEnd) {
					completed = true
				}
				if completed && ev.Kind == event.Status && !m.busy {
					break
				}
			}
			cards := toolCards(m)
			if len(cards) != 2 || !cards[0].result.OK || cards[1].result.OK || len(cards[0].decisions) != 2 {
				t.Fatal("tool results or nested guard decisions missing")
			}
			if _, err := os.Stat(filepath.Join(o.Repo, "declined")); !os.IsNotExist(err) {
				t.Fatal("declined write took effect")
			}
			history, err := b.History(ctx, snap.ID)
			if err != nil {
				t.Fatal(err)
			}
			m.restore(snapshot{ID: snap.ID, Repo: o.Repo, History: history})
			restoredCards := toolCards(m)
			if len(restoredCards) != len(cards) {
				t.Fatal("history lost tool cards")
			}
			for i, restored := range restoredCards {
				if restored.render() != cards[i].render() || strings.Contains(restored.render(), "sk-ant-abcdefgh1234") {
					t.Fatal("history changed or exposed tool details")
				}
			}
		})
	}
}

func TestMalformedToolResultDoesNotInventSuccess(t *testing.T) {
	m := testModel()
	m.consume(wire(string(log.KindToolCall), callRecord("one", "read"), 1))
	m.consume(wire(string(log.KindToolResult), "invalid result", 2))
	if toolCards(m)[0].result != nil || !strings.Contains(m.status, "Invalid tool.result") {
		t.Fatal("malformed result was presented as a tool outcome")
	}
	m.consume(wire(event.Status, event.StatusInfo{State: "idle"}, 0))
	if !strings.Contains(toolCards(m)[0].render(), "no result recorded") {
		t.Fatal("missing outcome presented as successful")
	}
}
