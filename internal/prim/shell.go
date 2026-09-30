package prim

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"github.com/fclaude/nodwyn/internal/scheme"
)

func shellExec(o Options) *scheme.Prim {
	return &scheme.Prim{
		Name: "shell-exec",
		Meta: scheme.Meta{
			Doc: "Run a program with arguments in the repository root (no shell: pass arguments as a list). Returns an alist with status, stdout and stderr.",
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
			cmd := exec.CommandContext(ctx, cmdName, args...)
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
			cmd.Stdout = &capped{&stdout, o.MaxOutput}
			cmd.Stderr = &capped{&stderr, o.MaxOutput}
			status := 0
			runErr := cmd.Run()
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
				return scheme.Value{}, fmt.Errorf("%q is a command line, not a program: cmd names one program and args holds its arguments, e.g. cmd %q args %s (there is no shell; use cmd \"sh\" args [\"-c\", ...] for pipes)", cmdName, fields[0], suggested)
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
