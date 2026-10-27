package core

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/fclaude/nodwyn/internal/config"
	_ "github.com/fclaude/nodwyn/internal/frontend/termvt" // the emulator a binary links in
	"github.com/fclaude/nodwyn/internal/log"
	fake "github.com/fclaude/nodwyn/test/fakes/ollama"
)

func withTerminal(c *config.Config) { c.Experimental.Terminal = true }

// The terminal is experimental (ADR 48): a session offers it only when
// config.toml says so.
func TestTerminalToolsAreOffUnlessEnabled(t *testing.T) {
	t.Parallel()
	r := newRig(t, true)
	for _, name := range []string{"term-open", "term-type", "term-keys", "term-screen", "term-wait", "term-close"} {
		if _, ok := r.s.interp.Lookup(name); ok {
			t.Errorf("%s is offered by default", name)
		}
	}
	on := newRigWith(t, true, withTerminal)
	for _, name := range []string{"term-open", "term-type", "term-keys", "term-screen", "term-wait", "term-close"} {
		p, ok := on.s.interp.Lookup(name)
		if !ok || !p.Tool() {
			t.Errorf("%s is not a tool with [experimental] terminal set", name)
		}
	}
	if !strings.Contains(on.logb.String(), "Experimental: the terminal tools") {
		t.Error("a session with the terminal on does not say it is experimental")
	}
	if strings.Contains(r.logb.String(), "Experimental") {
		t.Error("a session without it mentions it")
	}
}

// Opening a terminal asks once, and says it is experimental; after that the
// model types and presses keys without being asked, and ending the session
// hangs up on a terminal the model left open.
func TestTerminalAsksOnceAndEndsWithTheSession(t *testing.T) {
	t.Parallel()
	r := newRigCalls(t, false, 20, withTerminal,
		toolTurn("1", "term-open", map[string]any{"cmd": "sh", "args": []any{"-c", "echo $$ > term.pid; exec cat"}, "cols": 60, "rows": 12}),
		toolTurn("2", "term-type", map[string]any{"id": "t1", "text": "basil\n"}),
		toolTurn("3", "term-keys", map[string]any{"id": "t1", "keys": []any{"x", "Enter"}}),
		toolTurn("4", "term-screen", map[string]any{"id": "t1"}),
		fake.Turn{Content: "done"})
	asked := r.asked("open a terminal", "y")
	if len(asked) != 1 || asked[0].Prompt.Prim != "term-open" || !strings.Contains(strings.Join(asked[0].Prompt.Reasons, " "), "experimental") {
		t.Fatalf("want one prompt, for term-open, saying it is experimental: %+v", asked)
	}
	results := map[string]string{}
	for _, ev := range r.logged() {
		var tr log.ToolResult
		if ev.Kind == log.KindToolResult && ev.Decode(&tr) == nil {
			if !tr.OK {
				t.Errorf("%s failed: %s", tr.Name, tr.Error)
			}
			results[tr.Name] = tr.Value
		}
	}
	if !strings.Contains(results["term-open"], " 60x12") || strings.Count(results["term-type"], "basil") != 2 || strings.Count(results["term-screen"], "| x") != 2 {
		t.Fatalf("results: %q", results)
	}
	raw, err := os.ReadFile(filepath.Join(r.repo, "term.pid"))
	if err != nil {
		t.Fatalf("the program did not run in the repository: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		t.Fatal(err)
	}
	if err := syscall.Kill(pid, 0); err != nil {
		t.Fatalf("the terminal's program is not running: %v", err)
	}
	r.s.Close()
	for deadline := time.Now().Add(5 * time.Second); syscall.Kill(pid, 0) == nil; time.Sleep(20 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("the terminal's program still runs after the session closed")
		}
	}
}
