package prim

import (
	"os"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/fclaude/nodwyn/internal/scheme"
)

// A bash -c line gets pipefail before its first short option; anything
// else runs as it was given.
func TestPipefailOnlyForBashCommandLines(t *testing.T) {
	for _, c := range []struct {
		name string
		args []string
		want []string
	}{
		{"bash", []string{"-c", "x | y"}, []string{"-o", "pipefail", "-c", "x | y"}},
		{"/usr/bin/bash", []string{"-lc", "x"}, []string{"-o", "pipefail", "-lc", "x"}},
		{"bash", []string{"--norc", "-c", "x"}, []string{"--norc", "-o", "pipefail", "-c", "x"}},
		{"bash", []string{"-e", "-c", "x"}, []string{"-o", "pipefail", "-e", "-c", "x"}},
		{"bash", []string{"-o", "errexit", "-c", "x"}, []string{"-o", "pipefail", "-o", "errexit", "-c", "x"}},
		{"bash", []string{"script.sh", "-c"}, []string{"script.sh", "-c"}},
		{"bash", []string{"-e", "script.sh"}, []string{"-e", "script.sh"}},
		{"sh", []string{"-c", "x | y"}, []string{"-c", "x | y"}},
		{"go", []string{"-c", "x"}, []string{"-c", "x"}},
	} {
		if got := pipefail(c.name, c.args); !slices.Equal(got, c.want) {
			t.Errorf("pipefail(%s %v) = %v, want %v", c.name, c.args, got, c.want)
		}
	}
}

// A pipeline that hides a failure behind tail reports it: the working rules
// suggest `| tail -n 40`, and a failing check piped that way reported 0.
func TestShellExecPipelineReportsItsFailure(t *testing.T) {
	h := newHarness(t)
	h.answer = true
	for src, want := range map[string]string{
		`(shell-exec "bash" '("-c" "false | tail -n 1"))`:             `(("status" . 1) ("stdout" . "") ("stderr" . ""))`,
		`(shell-exec "bash" '("-ec" "false | cat; echo after"))`:      `(("status" . 1) ("stdout" . "") ("stderr" . ""))`,
		`(shell-exec "bash" '("--norc" "-c" "exit 3 | cat"))`:         `(("status" . 3) ("stdout" . "") ("stderr" . ""))`,
		`(shell-exec "bash" '("-c" "printf 'a\\nb\\n' | tail -n 1"))`: `(("status" . 0) ("stdout" . "b\n") ("stderr" . ""))`,
	} {
		if got := scheme.Write(h.mustEval(t, src)); got != want {
			t.Errorf("%s = %s, want %s", src, got, want)
		}
	}
}

// What a command leaves running in the background is stopped when it ends,
// and does not hold up its result: the browser checks' demo, started with &,
// outlived its session. Before, a child that kept the output open made the
// call wait five seconds and then fail.
func TestShellExecStopsWhatItLeavesRunning(t *testing.T) {
	h := newHarness(t)
	h.answer = true
	for _, line := range []string{
		"sleep 60 & echo $!",                  // holds the output
		"sleep 60 > /dev/null 2>&1 & echo $!", // does not
		"(trap '' TERM; sleep 60) & echo $!",  // ignores SIGTERM, so it takes SIGKILL
	} {
		start := time.Now()
		v, err := h.eval(t, `(shell-exec "bash" '("-c" `+strconv.Quote(line)+`))`)
		if err != nil {
			t.Fatalf("%s: %v", line, err)
		}
		if took := time.Since(start); took > 4*time.Second {
			t.Errorf("%s: took %s", line, took)
		}
		out := scheme.Write(v)
		if !strings.Contains(out, `("status" . 0)`) {
			t.Fatalf("%s: %s", line, out)
		}
		stdout := ""
		for p := v; p.Kind == scheme.KPair; p = p.P.Cdr {
			if kv := p.P.Car; kv.Kind == scheme.KPair && kv.P.Car.S == "stdout" {
				stdout = kv.P.Cdr.S
			}
		}
		pid, err := strconv.Atoi(strings.TrimSpace(strings.Split(stdout, "\n")[0]))
		if err != nil {
			t.Fatalf("%s: no pid in %q", line, stdout)
		}
		for deadline := time.Now().Add(5 * time.Second); alive(pid); time.Sleep(20 * time.Millisecond) {
			if time.Now().After(deadline) {
				_ = syscall.Kill(pid, syscall.SIGKILL)
				t.Fatalf("%s: the process it left (%d) still runs", line, pid)
			}
		}
	}
}

// alive reports a running process; one killed but not yet reaped is a
// zombie, and counts as gone.
func alive(pid int) bool {
	if syscall.Kill(pid, 0) != nil {
		return false
	}
	stat, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return true
	}
	f := strings.Fields(string(stat))
	return len(f) < 3 || f[2] != "Z"
}
