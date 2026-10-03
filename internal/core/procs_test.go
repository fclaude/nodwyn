package core

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

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
