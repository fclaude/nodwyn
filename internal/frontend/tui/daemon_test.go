package tui

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/fclaude/nodwyn/internal/core"
	"github.com/fclaude/nodwyn/internal/frontend/rpc"
	"github.com/fclaude/nodwyn/internal/llm"
	"github.com/fclaude/nodwyn/internal/schedule"
	fake "github.com/fclaude/nodwyn/test/fakes/ollama"
)

// A task open in the TUI is open in the daemon, so its schedule runs there
// as it would for a closed task. When the TUI hosted the task in its own
// process, that process held the task's log, and the daemon skipped every
// run while the TUI was open. schedule.run is the scheduler's own path
// (executeSchedule), taken now instead of at the schedule's time.
func TestATaskOpenInTheTUIStillRunsOnSchedule(t *testing.T) {
	model := fake.NewT(t, fake.Turn{Content: "the scheduled check is done"})
	o := testOptions(t)
	o.Provider = llm.NewOllama(model.URL)
	b, socket := testRemote(t, o)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	m := newModel(ctx, b, o.Repo, "", false)
	m.Update(m.Init()())
	press(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if !m.active {
		t.Fatalf("not opened: %s", m.status)
	}
	console, err := rpc.Dial(ctx, socket)
	if err != nil {
		t.Fatal(err)
	}
	defer console.Close()
	at := time.Now().Add(48 * time.Hour).Format(schedule.Layout)
	if err := console.Call(ctx, "schedule.set", map[string]any{"id": m.id, "message": "check the build", "repeat": "daily", "at": at}, nil); err != nil {
		t.Fatal(err)
	}
	if err := console.Call(ctx, "schedule.run", map[string]any{"id": m.id}, nil); err != nil {
		t.Fatalf("the scheduler could not run the task the TUI has open: %v", err)
	}
	pump(t, m, transcriptHas(m, "the scheduled check is done"))
	if !transcriptHas(m, "check the build")() {
		t.Fatalf("the TUI does not show the scheduled message:\n%s", m.viewport.View())
	}
	var state core.ScheduleInfo
	if err := console.Call(ctx, "schedule.read", map[string]any{"id": m.id}, &state); err != nil {
		t.Fatal(err)
	}
	if state.LastRun == nil || state.LastSkip != nil {
		t.Fatalf("the run is not recorded as run: %+v, skipped %+v", state.LastRun, state.LastSkip)
	}
}

// Ctrl+Q detaches, leaving the task open in the daemon; :close closes it
// there and quits, as the console's Close does, and a worktree with
// nothing unlanded goes with its branch.
func TestCloseEndsTheTaskAndItsCleanWorktree(t *testing.T) {
	o := testOptions(t)
	gitInit(t, o.Repo)
	b, socket := testRemote(t, o)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	m := newModel(ctx, b, o.Repo, "", false)
	m.worktree = true
	m.Update(m.Init()())
	if !m.active || m.branch == "" {
		t.Fatalf("not opened in a worktree: %q %s", m.branch, m.status)
	}
	worktree := m.repo
	console, err := rpc.Dial(ctx, socket)
	if err != nil {
		t.Fatal(err)
	}
	defer console.Close()
	open := func() error { return console.Call(ctx, "session.info", map[string]any{"id": m.id}, nil) }

	_, cmd := m.Update(tea.KeyPressMsg{Code: 'q', Mod: tea.ModCtrl})
	if _, quit := cmd().(tea.QuitMsg); !quit {
		t.Fatal("Ctrl+Q did not quit")
	}
	if err := open(); err != nil {
		t.Fatalf("Ctrl+Q closed the task: %v", err)
	}

	m.composer.SetValue(":close")
	_, cmd = m.Update(m.submit()())
	if cmd == nil {
		t.Fatalf(":close did not quit: %s", m.status)
	}
	if _, quit := cmd().(tea.QuitMsg); !quit {
		t.Fatalf(":close did not quit: %s", m.status)
	}
	if err := open(); err == nil {
		t.Fatal("the task is still open in the daemon")
	}
	if _, err := os.Stat(worktree); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the clean worktree is still there: %v", err)
	}
	out, err := exec.Command("git", "-C", o.Repo, "branch", "--list", m.branch).Output()
	if err != nil || strings.TrimSpace(string(out)) != "" {
		t.Fatalf("the worktree's branch is still there: %q %v", out, err)
	}
}
