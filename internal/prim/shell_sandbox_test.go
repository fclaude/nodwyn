package prim

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// In sandbox mode (ADR 55) what a command starts in the background keeps
// running after the command returns, and after the session ends: on a
// Terminal-Bench task the server the agent starts with `nohup server &` is
// what the task's tests talk to once the agent is done. The call still
// returns once the command's own program has exited, with what the
// background process printed first, and a process that goes on printing
// after that is not ended by a broken pipe. By default, in either profile,
// the same process is stopped when the command returns.
func TestASandboxCommandLeavesWhatItStartsInTheBackgroundRunning(t *testing.T) {
	lines := []string{
		// Holds the output, prints its address first and then a line every
		// 50 ms, which would end it at once on a closed pipe.
		"(echo listening on 8000; while sleep 0.05; do echo request; done) & echo pid=$!",
		// Its own output goes elsewhere, as nohup's often does.
		"nohup sleep 60 > /dev/null 2>&1 & echo pid=$!",
		// A process group of its own, as setsid and a daemon make one.
		"perl -e 'use POSIX; setsid(); sleep 60' & echo pid=$!",
	}
	pidOf := func(t *testing.T, got shellOut) int {
		t.Helper()
		// The background process may print before the shell does.
		m := regexp.MustCompile(`pid=(\d+)`).FindStringSubmatch(got.Stdout)
		if m == nil {
			t.Fatalf("no pid in %+v", got)
		}
		pid, _ := strconv.Atoi(m[1])
		t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })
		return pid
	}
	for _, lean := range []bool{true, false} {
		procs := NewProcs()
		sandbox := newViewRig(t, func(o *Options) { o.Sandbox, o.Lean, o.Procs = true, lean, procs })
		for _, line := range lines {
			started := time.Now()
			got := sandbox.run(t, line)
			if took := time.Since(started); took > 4*time.Second {
				t.Errorf("lean %v, %s: the call took %v", lean, line, took)
			}
			if got.Status != float64(0) {
				t.Fatalf("lean %v, %s: %+v", lean, line, got)
			}
			pid := pidOf(t, got)
			if strings.Contains(line, "listening") && !strings.Contains(got.Stdout, "listening on 8000") {
				t.Errorf("lean %v: what the server printed first is not in the result: %q", lean, got.Stdout)
			}
			time.Sleep(500 * time.Millisecond)
			if !alive(pid) {
				t.Errorf("lean %v, %s: the process it started (%d) was stopped", lean, line, pid)
			}
		}
		// The session's end leaves them running too.
		procs.Close()
		if got := sandbox.run(t, lines[0]); got.Status != float64(0) {
			t.Fatalf("lean %v, after the session closed: %+v", lean, got)
		} else if pid := pidOf(t, got); !waitFor(time.Second, func() bool { return alive(pid) }) {
			t.Errorf("lean %v: started after Close, %d is not running", lean, pid)
		}

		plain := newViewRig(t, func(o *Options) { o.Lean, o.Procs = lean, NewProcs() })
		got := plain.run(t, lines[0])
		pid := pidOf(t, got)
		if !waitFor(5*time.Second, func() bool { return !alive(pid) }) {
			t.Errorf("lean %v: by default the process a command left (%d) still runs", lean, pid)
		}
	}
}

// In sandbox mode a command may write outside the repository and the
// scratch directory without a security question, which by default the argv
// trigger asks a person (and a lean session nobody approves refuses), and
// the full profile's shell-exec description says what is left running is not
// stopped.
func TestASandboxCommandWritesOutsideTheRepositoryUnasked(t *testing.T) {
	outside, _ := filepath.EvalSymlinks(t.TempDir())
	target := filepath.Join(outside, "etc", "app.conf")
	line := "mkdir -p " + filepath.Dir(target) + " && echo port=8000 > " + target
	security := func(r *viewRig) bool {
		asked := false
		for _, q := range r.h.prompts {
			asked = asked || q.Security && strings.Contains(strings.Join(q.Reasons, " "), "outside the repository")
		}
		return asked
	}
	sandbox := newViewRig(t, func(o *Options) { o.Sandbox, o.Lean = true, false })
	if got := sandbox.run(t, line); got.Status != float64(0) {
		t.Fatalf("in sandbox mode: %+v", got)
	}
	if b, err := os.ReadFile(target); err != nil || string(b) != "port=8000\n" {
		t.Fatalf("the file outside: %q %v", b, err)
	}
	if security(sandbox) {
		t.Errorf("in sandbox mode a person was asked: %+v", sandbox.h.prompts)
	}
	// The rig answers every question yes, a security one too, so the
	// question is what shows the difference.
	plain := newViewRig(t, func(o *Options) { o.Lean = false })
	plain.run(t, line)
	if !security(plain) {
		t.Errorf("by default nobody was asked about %s: %+v", target, plain.h.prompts)
	}
	sp, _ := sandbox.h.in.Lookup("shell-exec")
	pp, _ := plain.h.in.Lookup("shell-exec")
	if strings.Contains(sp.Meta.Doc, "is stopped when it ends") || !strings.Contains(sp.Meta.Doc, "keeps running after it ends") || !strings.Contains(pp.Meta.Doc, "is stopped when it ends") {
		t.Errorf("the descriptions: sandbox %q, default %q", sp.Meta.Doc, pp.Meta.Doc)
	}
}
