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
	fake "github.com/fclaude/nodwyn/test/fakes/ollama"
)

// shell-start asks like shell-exec, and a session's background processes
// end with it.
func TestBackgroundProcessesAskAndEndWithTheSession(t *testing.T) {
	t.Parallel()
	start := fake.Turn{ToolCalls: []fake.ToolCall{{ID: "s", Name: "shell-start", Args: map[string]any{"cmd": "sh", "args": []any{"-c", "echo $$ > pid; echo up; exec sleep 60"}}}}}
	refused := newRig(t, true, start, fake.Turn{Content: "done"})
	refused.s.Send("serve")
	refused.drain("n")
	if !strings.Contains(refused.logb.String(), `"prim":"shell-start"`) || !strings.Contains(refused.logb.String(), "not confirmed") {
		t.Fatal("shell-start ran without a confirm")
	}

	r := newRig(t, false, start, fake.Turn{Content: "done"})
	r.s.Send("serve")
	r.drain("y")
	if !strings.Contains(r.logb.String(), "p1: running") {
		t.Fatalf("not started:\n%s", r.logb.String())
	}
	var pid int
	for i := 0; i < 100 && pid == 0; i++ {
		raw, _ := os.ReadFile(filepath.Join(r.repo, "pid"))
		pid, _ = strconv.Atoi(strings.TrimSpace(string(raw)))
		time.Sleep(20 * time.Millisecond)
	}
	if pid == 0 || syscall.Kill(pid, 0) != nil {
		t.Fatalf("process %d is not running", pid)
	}
	r.s.Close()
	for i := 0; i < 100 && syscall.Kill(pid, 0) == nil; i++ {
		time.Sleep(20 * time.Millisecond)
	}
	if syscall.Kill(pid, 0) == nil {
		t.Fatalf("process %d outlived its session", pid)
	}
}

// A model waiting for a command that is still running is not stopped for
// polling it: the same shell-read ended two turns at its fourth time, one of
// them a promote that had succeeded (20261003T002506Z-f564a28c, progress
// stop at seq 5907). Here a process that runs the whole test is read six
// times in a row and the turn ends with the model's answer.
func TestPollingARunningCommandIsNotAProgressStop(t *testing.T) {
	t.Parallel()
	poll := func(id string) fake.Turn {
		return fake.Turn{ToolCalls: []fake.ToolCall{{ID: id, Name: "shell-read", Args: map[string]any{"id": "p1", "wait": 0}}}}
	}
	r := newRigWith(t, false, func(c *config.Config) { c.Loop.MaxToolCalls = 20 },
		fake.Turn{ToolCalls: []fake.ToolCall{{ID: "s", Name: "shell-start", Args: map[string]any{"cmd": "sh", "args": []any{"-c", "exec sleep 60"}}}}},
		poll("r1"), poll("r2"), poll("r3"), poll("r4"), poll("r5"), poll("r6"),
		fake.Turn{Content: "still running; I will check again when it is done"},
	)
	r.s.Send("run the long check")
	r.drain("y")
	if strings.Contains(r.logb.String(), "progress stop") {
		t.Fatalf("the turn was stopped for polling:\n%s", r.logb.String())
	}
	if !strings.Contains(r.logb.String(), "still running; I will check again") {
		t.Fatalf("the turn did not reach the model's answer:\n%s", r.logb.String())
	}
	r.s.Close()
}
