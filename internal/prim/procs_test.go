package prim

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/fclaude/brote/scheme"
)

func procInterp(t *testing.T) (*scheme.Interp, *Procs) {
	t.Helper()
	ps := NewProcs()
	t.Cleanup(ps.Close)
	o := DefaultOptions(t.TempDir())
	o.Procs = ps
	o.EnvAllow = []string{"PATH"}
	in := scheme.New()
	if err := Register(in, o); err != nil {
		t.Fatal(err)
	}
	return in, ps
}

func evalStr(t *testing.T, in *scheme.Interp, src string) string {
	t.Helper()
	v, err := in.EvalString(context.Background(), src)
	if err != nil {
		t.Fatalf("%s: %v", src, err)
	}
	return v.S
}

func TestShellStartReadSendStop(t *testing.T) {
	in, _ := procInterp(t)
	out := evalStr(t, in, `(shell-start "sh" (list "-c" "echo ready; exec cat"))`)
	if !strings.HasPrefix(out, "p1: running (sh -c echo ready; exec cat)\n") || !strings.Contains(out, "ready") {
		t.Fatalf("start: %q", out)
	}
	if out := evalStr(t, in, `(shell-read "p1")`); out != "p1: running (sh -c echo ready; exec cat)\n" {
		t.Fatalf("nothing new: %q", out)
	}
	if out := evalStr(t, in, `(shell-send "p1" "basil\n")`); !strings.Contains(out, "basil") {
		t.Fatalf("send: %q", out)
	}
	if out := evalStr(t, in, `(shell-stop "p1")`); !strings.Contains(out, "p1: exited") {
		t.Fatalf("stop: %q", out)
	}
	if _, err := in.EvalString(context.Background(), `(shell-send "p1" "x\n")`); err == nil || !strings.Contains(err.Error(), "has exited") {
		t.Fatalf("send after exit: %v", err)
	}
	if _, err := in.EvalString(context.Background(), `(shell-read "p9")`); err == nil || !strings.Contains(err.Error(), "p1") {
		t.Fatalf("unknown id should list the session's: %v", err)
	}
}

func TestShellReadWaitsForOutputOrExit(t *testing.T) {
	in, _ := procInterp(t)
	evalStr(t, in, `(shell-start "sh" (list "-c" "sleep 1; echo late; exit 3"))`)
	start := time.Now()
	out := evalStr(t, in, `(shell-read "p1" 10)`)
	if !strings.Contains(out, "late") || time.Since(start) > 8*time.Second {
		t.Fatalf("read with wait: %q after %s", out, time.Since(start))
	}
	out = evalStr(t, in, `(shell-read "p1" 5)`)
	if !strings.HasPrefix(out, "p1: exited 3") {
		t.Fatalf("exit: %q", out)
	}
	// A run that prints its last line and exits a moment later is read as
	// exited, with that line: its summary and its status come together.
	evalStr(t, in, `(shell-start "sh" (list "-c" "sleep 1; echo PASS; sleep 0.2; exit 0"))`)
	if out := evalStr(t, in, `(shell-read "p2" 10)`); !strings.HasPrefix(out, "p2: exited 0") || !strings.Contains(out, "PASS") {
		t.Fatalf("a summary and the exit after it: %q", out)
	}
	for _, src := range []string{`(shell-read "p1" -1)`, `(shell-start "sh" (list 1))`} {
		if _, err := in.EvalString(context.Background(), src); err == nil {
			t.Errorf("%s accepted", src)
		}
	}
}

func TestShellProcessLimitBufferAndClose(t *testing.T) {
	in, ps := procInterp(t)
	for i := 0; i < procLimit; i++ {
		evalStr(t, in, `(shell-start "sleep" (list "30"))`)
	}
	if _, err := in.EvalString(context.Background(), `(shell-start "sleep" (list "30"))`); err == nil || !strings.Contains(err.Error(), "already running") {
		t.Fatalf("limit: %v", err)
	}
	evalStr(t, in, `(shell-stop "p1")`)
	// Output past the buffer is dropped from the front, and the read says so.
	evalStr(t, in, `(shell-start "sh" (list "-c" "yes x | head -c 1500000; echo end"))`)
	p5, _ := ps.get("p5")
	for i := 0; i < 500; i++ {
		p5.mu.Lock()
		done := p5.done
		p5.mu.Unlock()
		if done {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	out := evalStr(t, in, `(shell-read "p5")`)
	if !strings.HasPrefix(out, "p5: exited 0") || !strings.Contains(out, "bytes of earlier output were dropped") || !strings.Contains(out, "more bytes: read again") {
		t.Fatalf("large output: %q", out[:min(len(out), 300)])
	}
	ps.Close()
	for id, p := range ps.procs {
		p.mu.Lock()
		done := p.done
		p.mu.Unlock()
		if !done {
			t.Fatalf("%s still running after Close", id)
		}
	}
	if _, err := in.EvalString(context.Background(), `(shell-start "true" '())`); err == nil {
		t.Fatal("started after Close")
	}
}

func TestShellPrimsNeedASession(t *testing.T) {
	in := scheme.New()
	if err := Register(in, DefaultOptions(t.TempDir())); err != nil {
		t.Fatal(err)
	}
	if _, ok := in.Lookup("shell-start"); ok {
		t.Fatal("shell-start without a process set")
	}
}

// A program that ignores SIGTERM is killed, and shell-stop says it ended:
// it answered "running" for the process it had just killed, and the model
// tried again or reported a stuck process.
func TestShellStopReportsAKilledProcessEnded(t *testing.T) {
	in, _ := procInterp(t)
	evalStr(t, in, `(shell-start "sh" (list "-c" "trap '' TERM; echo ready; while :; do sleep 1; done"))`)
	if out := evalStr(t, in, `(shell-stop "p1")`); strings.Contains(out, "running") {
		t.Fatalf("stop: %q", out)
	}
}
