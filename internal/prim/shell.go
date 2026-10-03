package prim

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/fclaude/nodwyn/internal/scheme"
)

func shellExec(o Options) *scheme.Prim {
	return &scheme.Prim{
		Name: "shell-exec",
		Meta: scheme.Meta{
			Doc: "Run a program with arguments in the repository root (no shell: pass arguments as a list). Returns an alist with status, stdout and stderr. A bash -c command line runs with pipefail, so a pipeline's status is that of its last command that failed. Whatever the program leaves running in the background is stopped when it ends; a program that keeps running goes to shell-start.",
			Params: []scheme.Param{
				{Name: "cmd", Type: "string", Doc: "one program name or path, without its arguments", Role: "argv"},
				{Name: "args", Type: "list", Doc: "list of argument strings", Role: "argv"},
			},
			Host:         true,
			Irreversible: true,
			TaintSource:  scheme.TagShell,
		},
		Handler: func(c *scheme.Call) (scheme.Value, error) {
			cmdName, err := c.String(0)
			if err != nil {
				return scheme.Value{}, err
			}
			items, err := c.List(1)
			if err != nil {
				return scheme.Value{}, err
			}
			args := make([]string, len(items))
			for i, it := range items {
				if it.Kind != scheme.KString {
					return scheme.Value{}, errors.New("args must be a list of strings")
				}
				args[i] = it.S
			}
			ctx, cancel := context.WithTimeout(c.Ctx, o.ShellTimeout)
			defer cancel()
			cmd := exec.CommandContext(ctx, cmdName, pipefail(cmdName, args)...)
			cmd.Dir = o.Root
			cmd.Env = scrubEnv(o.EnvAllow)
			if o.Scratch != "" {
				// Temporary files go to the session's scratch directory.
				cmd.Env = append(cmd.Env, "TMPDIR="+o.Scratch)
			}
			cmd.Stdin = nil
			// Children get their own process group so cancellation kills the
			// whole tree, not just the parent.
			cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
			cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
			cmd.WaitDelay = 5 * time.Second
			var stdout, stderr bytes.Buffer
			runErr := runStopping(cmd, &capped{&stdout, o.MaxOutput}, &capped{&stderr, o.MaxOutput})
			status := 0
			if c.Ctx.Err() != nil {
				return scheme.Value{}, c.Ctx.Err() // aborted by the user or the loop
			}
			if ctx.Err() != nil {
				return scheme.Value{}, fmt.Errorf("timed out after %s", o.ShellTimeout)
			}
			var exitErr *exec.ExitError
			switch {
			case runErr == nil:
			case errors.As(runErr, &exitErr):
				status = exitErr.ExitCode()
				if status < 0 {
					status = 128 // killed by signal
				}
			case errors.Is(runErr, exec.ErrNotFound) && strings.ContainsAny(cmdName, " \t"):
				// Models used to a shell often send a whole command line as
				// cmd; say how to split it instead of only "not found".
				fields := strings.Fields(cmdName)
				suggested, _ := json.Marshal(append(fields[1:], args...))
				return scheme.Value{}, fmt.Errorf("%q is a command line, not a program: cmd names one program and args holds its arguments, e.g. cmd %q args %s (there is no shell; use cmd \"bash\" args [\"-c\", ...] for pipes)", cmdName, fields[0], suggested)
			default:
				return scheme.Value{}, runErr
			}
			return scheme.List(
				scheme.Cons(scheme.Str("status"), scheme.Int(int64(status))),
				scheme.Cons(scheme.Str("stdout"), scheme.Str(stdout.String())),
				scheme.Cons(scheme.Str("stderr"), scheme.Str(stderr.String())),
			), nil
		},
	}
}

// runStopping runs cmd to its end, then stops whatever it left running in
// its process group: a server started with &, a watcher. Left alone, such a
// process outlives the call and the session, and holds the call's output
// open: a session that started the browser checks' demo in the background
// left it running for good (session 20261003T190332Z-547f0bd6). Output goes
// through pipes this function owns, so Wait returns as soon as the program
// itself exits rather than when the last holder of its output does.
func runStopping(cmd *exec.Cmd, stdout, stderr io.Writer) error {
	outR, outW, err := os.Pipe()
	if err != nil {
		return err
	}
	errR, errW, err := os.Pipe()
	if err != nil {
		outR.Close()
		outW.Close()
		return err
	}
	cmd.Stdout, cmd.Stderr = outW, errW
	err = cmd.Start()
	outW.Close()
	errW.Close()
	if err != nil {
		outR.Close()
		errR.Close()
		return err
	}
	copied := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(stdout, outR); copied <- struct{}{} }()
	go func() { _, _ = io.Copy(stderr, errR); copied <- struct{}{} }()
	err = cmd.Wait()
	stopGroup(cmd.Process.Pid)
	// The output ends when its last writer is gone. A process that left the
	// group (setsid) may still hold it: reading stops after a moment.
	timeout := time.After(procStopGrace)
	for left := 2; left > 0; left-- {
		select {
		case <-copied:
		case <-timeout:
			outR.Close()
			errR.Close()
			<-copied
		}
	}
	outR.Close()
	errR.Close()
	return err
}

// stopGroup stops what is left of a process group: SIGTERM, so a program can
// clean up after itself (the demo removes its folders), then SIGKILL to
// whatever still runs after procStopGrace.
func stopGroup(pgid int) {
	if syscall.Kill(-pgid, syscall.SIGTERM) != nil {
		return
	}
	for deadline := time.Now().Add(procStopGrace); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if syscall.Kill(-pgid, 0) != nil {
			return
		}
	}
	_ = syscall.Kill(-pgid, syscall.SIGKILL)
}

// pipefail runs a bash -c command line with pipefail, so a pipeline's
// status is that of its last command that failed, not of its last command.
// The working rules suggest `| tail -n 40` for long output, and with it a
// failing check reported status 0: a session read a failing browser check
// as a success by its status (20261003T190332Z-547f0bd6), and the default
// loop counts a piped test run by it (ran-ok?). Only bash's -c form
// changes: a script keeps its own options, and sh may be a shell without
// pipefail. The option goes before the first short option, since bash
// takes its long options only before those.
func pipefail(name string, args []string) []string {
	if filepath.Base(name) != "bash" {
		return args
	}
	first := -1
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" || !strings.HasPrefix(a, "-") && !strings.HasPrefix(a, "+") {
			return args
		}
		if strings.HasPrefix(a, "--") {
			continue
		}
		if first < 0 {
			first = i
		}
		if a == "-o" || a == "+o" || a == "-O" || a == "+O" {
			i++ // the option's name
			continue
		}
		if strings.HasPrefix(a, "-") && strings.Contains(a[1:], "c") {
			out := append([]string{}, args[:first]...)
			out = append(out, "-o", "pipefail")
			return append(out, args[first:]...)
		}
	}
	return args
}

// scrubEnv builds a child environment from the allowlist only, so a secret
// in the parent's environment never reaches a subprocess (plan §3.3).
func scrubEnv(allow []string) []string {
	out := make([]string, 0, len(allow))
	for _, k := range allow {
		if v, ok := os.LookupEnv(k); ok {
			out = append(out, k+"="+v)
		}
	}
	return out
}

// capped writes at most n bytes and then appends a truncation marker once.
type capped struct {
	buf *bytes.Buffer
	n   int
}

func (c *capped) Write(p []byte) (int, error) {
	if c.buf.Len() >= c.n {
		return len(p), nil
	}
	room := c.n - c.buf.Len()
	if len(p) > room {
		c.buf.Write(p[:room])
		c.buf.WriteString("\n[output truncated at " + byteCount(c.n) + "]\n")
		return len(p), nil
	}
	return c.buf.Write(p)
}

func byteCount(n int) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%d MiB", n>>20)
	case n >= 1<<10:
		return fmt.Sprintf("%d KiB", n>>10)
	}
	return fmt.Sprintf("%d B", n)
}
