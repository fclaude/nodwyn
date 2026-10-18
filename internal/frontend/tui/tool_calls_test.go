package tui

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/fclaude/nodwyn/internal/event"
	"github.com/fclaude/nodwyn/internal/llm"
	"github.com/fclaude/nodwyn/internal/log"
	fake "github.com/fclaude/nodwyn/test/fakes/ollama"
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
		if !strings.Contains(cards[i].render(true), want) {
			t.Fatalf("card %d missing %q", i, want)
		}
	}
	if cards[2].call != nil || strings.Contains(cards[2].render(true), "awaiting result") {
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
	if !strings.Contains(cards[0].render(true), "no result recorded") || cards[1].call != nil {
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
	if m.activeTool == nil || !strings.Contains(m.activeTool.render(true), "awaiting result") {
		t.Fatal("catch-up lost an open call")
	}
	m.consume(wire(string(log.KindGuardDecision), log.GuardDecision{Prim: "read", Origin: "model", Decision: "confirm", Answer: "yes", Detail: canary + "\x1b[2J"}, 2))
	m.consume(wire(string(log.KindToolResult), log.ToolResult{ID: "live", Name: "read", OK: true, Value: canary + "\x1b]52;c;ATTACK\a\nvisible result"}, 3))
	text := toolCards(m)[0].render(true)
	if strings.Contains(text, canary) || strings.Contains(text, "\x1b") || strings.Contains(text, "ATTACK") || !strings.Contains(text, "visible result") {
		t.Fatal("unsafe or missing tool detail")
	}
	snap.Busy = false
	m.restore(snap)
	if m.activeTool != nil || !strings.Contains(toolCards(m)[0].render(true), "no result recorded") {
		t.Fatal("idle history displayed an interrupted call as running")
	}
}

func TestToolCardsFromDaemonEvents(t *testing.T) {
	o := testOptions(t)
	if err := os.MkdirAll(filepath.Join(o.Repo, ".nodwyn"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(o.Repo, ".nodwyn", "init.scm"), []byte(`(register-tool "read-note" (lambda () (fs-read "note")) '((doc . "Read note") (params . ())))`), 0600); err != nil {
		t.Fatal(err)
	}
	o.InitFile = filepath.Join(o.Repo, ".nodwyn", "init.scm")
	if err := os.WriteFile(filepath.Join(o.Repo, "note"), []byte("visible result\nsk-ant-abcdefgh1234"), 0600); err != nil {
		t.Fatal(err)
	}
	server := fake.New(fake.Turn{ToolCalls: []fake.ToolCall{
		{ID: "read", Name: "read-note", Args: map[string]any{}},
		{ID: "write", Name: "fs-write", Args: map[string]any{"path": "declined", "content": "must not write"}},
	}}, fake.Turn{Content: "done"})
	defer server.Close()
	o.Provider = llm.NewOllama(server.URL)
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
		if restored.render(true) != cards[i].render(true) || strings.Contains(restored.render(true), "sk-ant-abcdefgh1234") {
			t.Fatal("history changed or exposed tool details")
		}
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
	if !strings.Contains(toolCards(m)[0].render(true), "no result recorded") {
		t.Fatal("missing outcome presented as successful")
	}
}

// A conversation is unreadable when every tool call dumps its arguments and
// its result into it. The compact line carries what a reader scans; the
// body is behind :details.
func TestCompactToolLineSummarisesWithoutTheBody(t *testing.T) {
	m := testModel()
	m.consume(wire(string(log.KindToolCall), log.ToolCallEvent{
		ToolCall: log.ToolCall{ID: "c1", Name: "fs-read", Args: json.RawMessage(`{"path":"internal/log/log.go"}`)},
		Origin:   "model",
	}, 1))
	m.consume(wire(string(log.KindGuardDecision), log.GuardDecision{Prim: "fs-read", Origin: "model", Decision: "allow"}, 2))
	m.consume(wire(string(log.KindToolResult), log.ToolResult{
		ID: "c1", Name: "fs-read", OK: true, Value: strings.Repeat("x", 10240), Millis: 5,
	}, 3))
	card := toolCards(m)[0]

	compact := card.render(false)
	if strings.Contains(compact, "\n") {
		t.Fatalf("compact render must be one line: %q", compact)
	}
	for _, want := range []string{"fs-read", "internal/log/log.go", "[allow]", "✓ 10.0 kB", "5 ms"} {
		if !strings.Contains(compact, want) {
			t.Errorf("compact line missing %q: %q", want, compact)
		}
	}
	if strings.Contains(compact, strings.Repeat("x", 64)) {
		t.Error("compact line must not carry the result body")
	}
	if full := card.render(true); !strings.Contains(full, strings.Repeat("x", 64)) {
		t.Error("details render must carry the result body")
	}
}

// Once a call names its target, a long prose argument is noise on the line.
func TestArgSummaryLeadsWithTheTargetAndDropsProse(t *testing.T) {
	long := strings.Repeat("prose ", 40)
	got := argSummary([]byte(`{"new":"` + long + `","path":"README.md","old":"short old"}`))
	if !strings.HasPrefix(got, "README.md") {
		t.Fatalf("target should lead: %q", got)
	}
	if strings.Contains(got, "prose prose") {
		t.Errorf("long prose argument should be dropped: %q", got)
	}
	if !strings.Contains(got, "short old") {
		t.Errorf("short arguments should survive: %q", got)
	}
	if plain := argSummary([]byte(`{"a":"one","b":"two"}`)); plain != "one two" {
		t.Errorf("without a target key every value shows: %q", plain)
	}
}

// A session row must keep the detail that tells two sessions apart, even
// when the first message is long enough to fill the terminal.
func TestSessionRowKeepsTheMetaColumn(t *testing.T) {
	long := strings.Repeat("a long first message ", 10)
	row := twoColumn(long, "nodwyn · 4d", 60, false)
	if !strings.HasSuffix(ansi.Strip(row), "nodwyn · 4d") {
		t.Fatalf("meta must survive clipping: %q", row)
	}
	if width := lipgloss.Width(row); width != 60 {
		t.Fatalf("row width %d, want 60: %q", width, row)
	}
	short := ansi.Strip(twoColumn("untitled", "nodwyn · 4d", 60, false))
	if !strings.HasPrefix(short, "untitled  ") || !strings.HasSuffix(short, "nodwyn · 4d") {
		t.Fatalf("short label should be padded to the meta column: %q", short)
	}
}
