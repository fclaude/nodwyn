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
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/fclaude/nodwyn/internal/scheme"
)

func shellExec(o Options) *scheme.Prim {
	files := &outputFiles{}
	return &scheme.Prim{
		Name: "shell-exec",
		Meta: scheme.Meta{
			Doc: "Run a program with arguments in the repository root (no shell: pass arguments as a list). Returns an alist with status, stdout and stderr. A stream of more than 8 KiB or 150 lines is shown as a view: its first 20 lines, a marker that says which lines are not shown, and its last 50 lines (at most 6 KiB, a line cut at 300 characters); the whole stream is saved in a file under $TMPDIR/out/, whose path the marker gives, to read a range of with sed -n 'A,Bp' or search with grep -n. There is no need to pipe a command through tail or head to keep its output short; a pipe through them still works. A command that reads a file (cat, head, tail, nl, sed -n) is not shown as a view: it prints what it was asked for, and past 256 KiB keeps its first and last halves. A command that runs past the time limit is stopped and returns what it printed, with a status that says it timed out. A bash -c command line runs with pipefail, so a pipeline's status is that of its last command that failed. Whatever the program leaves running in the background is stopped when it ends; a program that keeps running goes to shell-start. A python -c program takes newlines, not ;, before a compound statement (class, def, if, for): write several lines, or python - with a heredoc in bash -c. A result carries a note when the command exited 141 (output cut by a downstream command such as head) or read a file and printed nothing (a range past the end of the file).",
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
			args, err := stringList(c, 1)
			if err != nil {
				return scheme.Value{}, err
			}
			ctx, cancel := context.WithTimeout(c.Ctx, o.ShellTimeout)
			defer cancel()
			cmd := exec.CommandContext(ctx, cmdName, pipefail(cmdName, args)...)
			cmd.Dir = o.Root
			cmd.Env = childEnv(o)
			cmd.Stdin = nil
			// Children get their own process group so cancellation kills the
			// whole tree, not just the parent.
			cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
			cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
			cmd.WaitDelay = 5 * time.Second
			var stdout, stderr bytes.Buffer
			out, errOut := &capped{buf: &stdout, n: o.MaxOutput, tail: o.MaxOutput / 2}, &capped{buf: &stderr, n: o.MaxOutput, tail: o.MaxOutput / 2}
			// A long stream is shown as a view with a file behind it, unless
			// there is nowhere to keep the file, the command asks for a range of
			// a file, or a program is computing with the output.
			var outTap, errTap *outputTap
			var outW, errW io.Writer = out, errOut
			if o.Scratch != "" && directCall(c.Ctx) && !readLike(cmdName, args) {
				call := &callFiles{}
				outTap = newOutputTap(out, files, call, o.Scratch, "stdout")
				errTap = newOutputTap(errOut, files, call, o.Scratch, "stderr")
				outW, errW = outTap, errTap
			}
			runErr := runStopping(cmd, outW, errW)
			out.finish()
			errOut.finish()
			if outTap != nil {
				outTap.finish()
				errTap.finish()
			}
			status := scheme.Int(0)
			if c.Ctx.Err() != nil {
				return scheme.Value{}, c.Ctx.Err() // aborted by the user or the loop
			}
			if ctx.Err() != nil {
				// What it printed before the cut is what says where it
				// hung, so it is returned with the reason.
				status = scheme.Str("timed out after " + o.ShellTimeout.String())
				runErr = nil
			}
			var exitErr *exec.ExitError
			switch {
			case runErr == nil:
			case errors.As(runErr, &exitErr):
				code := exitErr.ExitCode()
				if code < 0 {
					code = 128 // killed by signal
				}
				status = scheme.Int(int64(code))
			case errors.Is(runErr, exec.ErrNotFound) && strings.ContainsAny(cmdName, " \t"):
				// Models used to a shell often send a whole command line as
				// cmd; say how to split it instead of only "not found".
				fields := strings.Fields(cmdName)
				suggested, _ := json.Marshal(append(fields[1:], args...))
				return scheme.Value{}, fmt.Errorf("%q is a command line, not a program: cmd names one program and args holds its arguments, e.g. cmd %q args %s (there is no shell; use cmd \"bash\" args [\"-c\", ...] for pipes)", cmdName, fields[0], suggested)
			default:
				return scheme.Value{}, runErr
			}
			printed := stdout.Len() + stderr.Len()
			outText, errText := stdout.String(), stderr.String()
			if outTap != nil {
				// One view may take viewBudget; two share it, so the result stays
				// under the window's cut.
				budget := viewBudget
				if outTap.viewable() && errTap.viewable() {
					budget /= 2
				}
				if v, ok := outTap.view(budget); ok {
					outText = v
				}
				if v, ok := errTap.view(budget); ok {
					errText = v
				}
			}
			fields := []scheme.Value{
				scheme.Cons(scheme.Str("status"), status),
				scheme.Cons(scheme.Str("stdout"), scheme.Str(outText)),
				scheme.Cons(scheme.Str("stderr"), scheme.Str(errText)),
			}
			if note := shellNote(cmdName, args, status, printed); note != "" {
				fields = append(fields, scheme.Cons(scheme.Str("note"), scheme.Str(note)))
			}
			return scheme.List(fields...), nil
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
	// SIGTERM first, so a program can clean up after itself (the demo
	// removes its folders).
	stopProcessGroup(cmd.Process.Pid, syscall.SIGTERM, func() bool { return syscall.Kill(-cmd.Process.Pid, 0) != nil })
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

// stopProcessGroup ends a process group: sig first, then SIGKILL when gone
// still says no after procStopGrace, and a moment more for the kill to be
// seen. gone says whether what the caller waits for has ended, which is the
// leader for a process started with shell-start and every member for what
// shell-exec leaves running. It reports whether it had.
func stopProcessGroup(pgid int, sig syscall.Signal, gone func() bool) bool {
	if syscall.Kill(-pgid, sig) != nil {
		return true // no such group: nothing is left
	}
	if waitUntil(procStopGrace, gone) {
		return true
	}
	_ = syscall.Kill(-pgid, syscall.SIGKILL)
	return waitUntil(time.Second, gone)
}

// waitUntil polls cond until it holds or d has passed, and reports whether
// it held.
func waitUntil(d time.Duration, cond func() bool) bool {
	for deadline := time.Now().Add(d); ; time.Sleep(20 * time.Millisecond) {
		if cond() {
			return true
		}
		if !time.Now().Before(deadline) {
			return false
		}
	}
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
// in the parent's environment never reaches a subprocess.
func scrubEnv(allow []string) []string {
	out := make([]string, 0, len(allow))
	for _, k := range allow {
		if v, ok := os.LookupEnv(k); ok {
			out = append(out, k+"="+v)
		}
	}
	return out
}

// ScrubEnv is the environment the host gives a program it runs itself
// outside a primitive (git making a session's worktree, ADR 34), built as
// a primitive's is.
func ScrubEnv(allow []string) []string { return scrubEnv(allow) }

// childEnv is the environment of a program a primitive starts for the
// model: the scrubbed one, with temporary files going to the session's
// scratch directory.
func childEnv(o Options) []string {
	env := scrubEnv(o.EnvAllow)
	if o.Scratch != "" {
		env = append(env, "TMPDIR="+o.Scratch)
	}
	return env
}

// stringList is argument i, a list of strings, as the args of a command.
func stringList(c *scheme.Call, i int) ([]string, error) {
	items, err := c.List(i)
	if err != nil {
		return nil, err
	}
	out := make([]string, len(items))
	for j, it := range items {
		if it.Kind != scheme.KString {
			return nil, errors.New("args must be a list of strings")
		}
		out[j] = it.S
	}
	return out, nil
}

// capped keeps at most n bytes of what is written to it, and says when it
// dropped some. With tail zero it keeps the first n bytes and appends a
// truncation marker once, at the first byte it drops: when the earlier writes
// filled it exactly, output past the cap still says it was cut. With a tail
// it keeps the whole output up to n bytes and, past that, the first n-tail
// bytes and the last tail bytes, and finish puts the marker between them: the end of a long build or test run is where its
// failure is.
type capped struct {
	buf  *bytes.Buffer
	n    int
	tail int
	cut  bool
	rest []byte // the latest of what came after the head, trimmed in batches
	seen int    // bytes written after the head
}

func (c *capped) Write(p []byte) (int, error) {
	size := len(p)
	if !c.cut {
		room := c.n - c.buf.Len()
		if len(p) <= room {
			return c.buf.Write(p)
		}
		c.cut = true
		if c.tail == 0 {
			c.buf.Write(p[:room])
			c.buf.WriteString("\n[output truncated at " + byteCount(c.n) + "]\n")
			return size, nil
		}
		// Past n the head shrinks to n-tail, and what it held beyond that
		// belongs to the tail's window.
		all := append(append([]byte(nil), c.buf.Bytes()...), p...)
		c.buf.Reset()
		c.buf.Write(all[:c.n-c.tail])
		p = all[c.n-c.tail:]
	}
	if c.tail > 0 {
		c.rest = append(c.rest, p...)
		c.seen += len(p)
		if len(c.rest) > 2*c.tail {
			c.rest = c.rest[:copy(c.rest, c.rest[len(c.rest)-c.tail:])]
		}
	}
	return size, nil
}

// finish appends what a writer with a tail kept of the end, after a marker
// that counts the bytes between; it does nothing for one that was not cut.
func (c *capped) finish() {
	if c.tail == 0 || !c.cut {
		return
	}
	kept := c.rest[max(0, len(c.rest)-c.tail):]
	fmt.Fprintf(c.buf, "\n[%s omitted: only the first %s and the last %s of the output are kept]\n", byteCount(c.seen-len(kept)), byteCount(c.n-c.tail), byteCount(len(kept)))
	c.buf.Write(kept)
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

var readLikeRE = regexp.MustCompile(`(^|[\s;&|(])(cat|head|tail|nl|sed\s+-n)\b`)

// readLike is whether a command line reads part of a file, or limits its own
// output: cat, head, tail, nl, sed -n. Whoever asks for a range of a file
// wants that range, and a pipe into head or tail has made the output short.
func readLike(cmd string, args []string) bool {
	return readLikeRE.MatchString(cmd + " " + strings.Join(args, " "))
}

// shellNote says what a result's bare numbers do not. Exit 141 is SIGPIPE: a
// downstream command (head) stopped reading, so there may be more output; on
// the benchmark's matplotlib-25960 `grep ... | head -n 100` ended 141 and the
// model never noticed. A command that reads a file, exits 0 and prints nothing
// has read past its end: django-14534's `sed -n '600,700p'` of a 284-line
// file was taken for a fault of the tool, and four calls of 20 went on
// finding out (ADR 32, amended 2026-10-17).
func shellNote(cmd string, args []string, status scheme.Value, printed int) string {
	if status.Kind == scheme.KInt && status.I == 141 {
		return "exit 141: the output was cut because a command later in the pipe (such as head) stopped reading; there may be more"
	}
	if printed == 0 && status.Kind == scheme.KInt && status.I == 0 {
		line := cmd + " " + strings.Join(args, " ")
		if readLike(cmd, args) && !strings.Contains(line, ">") {
			return "exit 0 with no output: a command that reads a file printed nothing, so its range may lie past the end of the file (wc -l FILE counts the lines) or the file is empty"
		}
	}
	return ""
}
