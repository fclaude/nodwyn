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
	"github.com/fclaude/nodwyn/internal/config"
	"github.com/fclaude/nodwyn/internal/log"
)

// A search, a recall and an index update read as words on their lines; an
// index build's own reads neither give the call its verdict nor fill its
// details one line each, and what the build did is a line under the call
// (ADR 50).
func TestRetrievalCallsReadAsWords(t *testing.T) {
	m := testModel()
	m.consume(wire(string(log.KindToolCall), log.ToolCallEvent{
		ToolCall: log.ToolCall{ID: "s", Name: "index-search", Args: json.RawMessage(`{"query":"credential binding","scope":"repo","limit":5}`)},
		Origin:   "model",
	}, 1))
	m.consume(wire(string(log.KindGuardDecision), log.GuardDecision{Prim: "index-search", Origin: "model", Decision: "allow"}, 2))
	for seq := uint64(3); seq < 6; seq++ {
		m.consume(wire(string(log.KindGuardDecision), log.GuardDecision{Prim: "fs-read", Origin: "model", Decision: "allow", Checker: "repo-pin"}, seq))
	}
	m.consume(wire(string(log.KindGuardDecision), log.GuardDecision{Prim: "fs-read", Origin: "model", Decision: "confirm", Answer: "no", Actor: "not asked", Reason: "user checker secrets"}, 6))
	m.consume(wire(string(log.KindIndexUpdate), log.IndexUpdate{Root: "/r", Files: 3, Read: 3, Refused: []string{"key.txt"}, Millis: 1300}, 7))
	m.consume(wire(string(log.KindToolResult), log.ToolResult{ID: "s", Name: "index-search", OK: true, Value: "[the 1 best]", Millis: 9}, 8))
	card := toolCards(m)[0]
	compact := card.render(false)
	if !strings.Contains(compact, "index-search  “credential binding” in the repository  [allow]  ✓") {
		t.Fatalf("the search's line: %q", compact)
	}
	full := card.render(true)
	if !strings.Contains(full, "fs-read · allow ×3") || strings.Count(full, "fs-read · allow") != 1 {
		t.Fatalf("the build's reads in details:\n%s", full)
	}
	if !transcriptHas(m, "  [index updated: read 3 files · left out by the guard: key.txt · holds 3 files · 1.3 s]")() {
		t.Fatalf("the build's line: %+v", m.transcript)
	}
	for _, c := range []struct{ name, args, want string }{
		{"recall", `{"seq":812,"start":40}`, "recall  seq 812 from line 40"},
		{"recall", `{"seq":12,"task":"20261009T034803Z-3981612f"}`, "recall  seq 12 of task 20261009T034803Z-3981612f"},
		{"index-update", `{}`, "index-update  the whole repository"},
		{"index-search", `{"query":"handover","scope":"earlier"}`, "index-search  “handover” in earlier tasks"},
	} {
		card := &toolCallView{call: &log.ToolCallEvent{ToolCall: log.ToolCall{ID: "x", Name: c.name, Args: json.RawMessage(c.args)}}}
		if got := card.render(false); !strings.Contains(got, c.want) {
			t.Errorf("%s %s: %q", c.name, c.args, got)
		}
	}
}

// :index shows the project's repository index and :index forget deletes it;
// :earlier shows whether earlier tasks are searched and :earlier on and off
// change it, kept with the folder's name in workspaces.toml, through either
// backend (ADR 50).
func TestRetrievalCommandsThroughBothBackends(t *testing.T) {
	for _, mode := range []string{"local", "rpc"} {
		t.Run(mode, func(t *testing.T) {
			o := testOptions(t)
			if err := os.WriteFile(filepath.Join(o.Repo, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			b := testBackend(t, mode, o)
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			snap, err := b.Attach(ctx, o.Repo, "")
			if err != nil {
				t.Fatal(err)
			}
			m := newModel(ctx, b, o.Repo, snap.ID, false)
			m.restore(snap)
			view := func(command string) string {
				t.Helper()
				typed(m, command)
				if m.inspector == nil {
					t.Fatalf("%s showed nothing; status %q", command, m.status)
				}
				content := m.inspector.content
				m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
				return content
			}
			if got := view(":index"); !strings.Contains(got, "Repository index: none yet") || !strings.Contains(got, "Earlier tasks: not searched") {
				t.Fatalf(":index before a build:\n%s", got)
			}
			typed(m, ":eval (index-update)")
			pump(t, m, transcriptHas(m, "The index holds 1 files"))
			if got := view(":index"); !strings.Contains(got, "Repository index: 1 files, ") || !strings.Contains(got, ":index forget deletes it") {
				t.Fatalf(":index after a build:\n%s", got)
			}
			typed(m, ":index forget")
			if !strings.HasPrefix(m.status, "Forgot the repository index of repo") {
				t.Fatalf(":index forget: %q", m.status)
			}
			if got := view(":index"); !strings.Contains(got, "Repository index: none yet") {
				t.Fatalf(":index after forgetting:\n%s", got)
			}

			typed(m, ":earlier on")
			if !strings.Contains(m.status, "the model may search them") {
				t.Fatalf(":earlier on: %q", m.status)
			}
			if ws, err := config.LoadWorkspace(config.RegistryFile(o.Paths), o.Repo); err != nil || !ws.EarlierTasks {
				t.Fatalf("workspaces.toml after :earlier on: %+v, %v", ws, err)
			}
			if got := view(":earlier"); !strings.Contains(got, "Earlier tasks: the model may search this folder's other tasks") {
				t.Fatalf(":earlier when on:\n%s", got)
			}
			typed(m, ":earlier off")
			if ws, _ := config.LoadWorkspace(config.RegistryFile(o.Paths), o.Repo); ws.EarlierTasks {
				t.Fatal("still on after :earlier off")
			}
			typed(m, ":earlier maybe")
			if !strings.HasPrefix(m.status, "usage: :index") {
				t.Fatalf("a wrong argument: %q", m.status)
			}
		})
	}
}
