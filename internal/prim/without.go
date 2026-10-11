package prim

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/fclaude/nodwyn/internal/scheme"
)

// check-without asks whether a check tests each part of a change: the
// evidence loop calls it when an unattended turn is about to end on a check
// that passed (ADR 32, amended 2026-10-11). It takes each part of the change
// out in turn, a hunk of a changed file or a whole new or deleted file, runs
// the check again, and puts the part back. A check that still passes without
// a part does not test it: either the part is not needed or the check cannot
// tell. On the long-context SWE-bench runs every losing attempt, in all three
// harnesses, checked its change with something that passed whether or not the
// change was right; the same showed in the person's own sessions, where Go
// tests stayed green while the page the change was for went blank. The test
// needs no notion of what the task is: only the change, as git shows it, and
// the check the model itself ran.
//
// What it touches is what keep-passing would keep (keepable: no credential
// file, nothing under a protected path, no link), in code only: documents
// and tests are left alone, since a check that passes without a test or a
// note says nothing about the check. A hunk that adds or removes only
// comments and blank lines is left out too: no check can see it. Every file
// it changes is put back to the bytes it had, after each run and again at
// the end whatever happens. The runs share one time budget, so a slow check
// (a whole test suite) cannot take a turn's wall time: a part not reached
// before it runs out is not reported.
const (
	withoutMaxHunks = 8
	withoutMaxLines = 6000 // a longer file is left out, as Unified leaves out a longer diff
	withoutMaxFile  = 1 << 20
)

// withoutBudget is the time all of a check-without's runs share.
var withoutBudget = 5 * time.Minute

// withoutPart is one part of the change: the file with that part taken out
// (absent when the part is the whole file), and where it is.
type withoutPart struct {
	path           string
	line           int // the part's first line in the file as it is now; 0 for a whole file
	added, removed int
	kind           string // "hunk", "new file" or "deleted file"
	content        []byte
	absent         bool
}

// withoutOriginal is a file as the change left it, to put back.
type withoutOriginal struct {
	data   []byte
	mode   fs.FileMode
	exists bool
}

func checkWithout(o Options) *scheme.Prim {
	return &scheme.Prim{
		Name: "check-without",
		Meta: scheme.Meta{
			Doc: "Run a check again with each part of the uncommitted change taken out in turn (a hunk of a changed code file, or a whole new or deleted one; documents, tests and comment-only hunks are left alone), putting each part back after its run. Returns a list with one alist per part: path, line (its first line now, 0 for a whole file), added, removed, kind (hunk, new file or deleted file), and status, the check's exit status without that part, or error. At most " + fmt.Sprint(withoutMaxHunks) + " parts, run within " + withoutBudget.String() + " in all: a part not reached in that time is not in the list. The evidence loop calls it before an unattended turn ends on a passing check; a part whose check still exits 0 is one the check does not test.",
			Params: []scheme.Param{
				{Name: "cmd", Type: "string", Doc: "the check's program, as it was run", Role: "argv"},
				{Name: "args", Type: "list", Doc: "its arguments", Role: "argv"},
			},
			Host:   true,
			Hidden: true,
		},
		Handler: func(c *scheme.Call) (scheme.Value, error) {
			name, err := c.String(0)
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
			parts, statuses, err := runWithout(c.Ctx, o, name, args)
			if err != nil {
				return scheme.Value{}, fmt.Errorf("check-without: %w", err)
			}
			out := make([]scheme.Value, len(parts))
			for i, p := range parts {
				fields := []scheme.Value{
					scheme.Cons(scheme.Str("path"), scheme.Str(p.path)),
					scheme.Cons(scheme.Str("line"), scheme.Int(int64(p.line))),
					scheme.Cons(scheme.Str("added"), scheme.Int(int64(p.added))),
					scheme.Cons(scheme.Str("removed"), scheme.Int(int64(p.removed))),
					scheme.Cons(scheme.Str("kind"), scheme.Str(p.kind)),
				}
				if s := statuses[i]; s.err != nil {
					fields = append(fields, scheme.Cons(scheme.Str("error"), scheme.Str(s.err.Error())))
				} else {
					fields = append(fields, scheme.Cons(scheme.Str("status"), scheme.Int(int64(s.status))))
				}
				out[i] = scheme.List(fields...)
			}
			return scheme.List(out...), nil
		},
	}
}

type withoutStatus struct {
	status int
	err    error
}

// runWithout finds the change's parts, then runs the check once without
// each, and puts every file back.
func runWithout(ctx context.Context, o Options, name string, args []string) ([]withoutPart, []withoutStatus, error) {
	k := &Keeper{o: o}
	_, paths, err := k.look(ctx)
	if err != nil {
		return nil, nil, err
	}
	tracked, err := k.headFiles(ctx)
	if err != nil {
		return nil, nil, err
	}
	deny := denylist(o)
	originals := map[string]withoutOriginal{}
	var parts []withoutPart
	for _, rel := range paths {
		if len(parts) >= withoutMaxHunks {
			break
		}
		if !keepable(deny, rel) || !codeFile(rel) {
			continue
		}
		abs := filepath.Join(o.Root, filepath.FromSlash(rel))
		info, err := os.Lstat(abs)
		h, inHead := tracked[rel]
		switch {
		case errors.Is(err, fs.ErrNotExist):
			if !inHead {
				continue
			}
			old, err := k.headBlob(ctx, h.oid)
			if err != nil {
				continue
			}
			originals[rel] = withoutOriginal{}
			parts = append(parts, withoutPart{path: rel, kind: "deleted file", content: old, removed: len(splitLines(string(old)))})
		case err != nil:
			return nil, nil, err
		case !info.Mode().IsRegular() || info.Size() > withoutMaxFile:
			continue
		default:
			cur, err := os.ReadFile(abs)
			if err != nil {
				return nil, nil, err
			}
			originals[rel] = withoutOriginal{data: cur, mode: info.Mode().Perm(), exists: true}
			if !inHead {
				parts = append(parts, withoutPart{path: rel, kind: "new file", absent: true, added: len(splitLines(string(cur)))})
				continue
			}
			old, err := k.headBlob(ctx, h.oid)
			if err != nil {
				continue
			}
			for _, p := range hunksOf(rel, old, cur) {
				if len(parts) < withoutMaxHunks {
					parts = append(parts, p)
				}
			}
		}
	}
	// Every file goes back to the bytes the change left, whatever happens.
	defer func() {
		for rel := range originals {
			putBackWithout(k, o, rel, originals[rel])
		}
	}()
	statuses := make([]withoutStatus, len(parts))
	deadline := time.Now().Add(withoutBudget)
	for i, p := range parts {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		if !time.Now().Before(deadline) {
			parts, statuses = parts[:i], statuses[:i]
			break
		}
		if err := writeWithout(k, o, p); err != nil {
			statuses[i] = withoutStatus{err: err}
			continue
		}
		status, err := runCheck(ctx, o, name, args, deadline)
		statuses[i] = withoutStatus{status: status, err: err}
		if err := putBackWithout(k, o, p.path, originals[p.path]); err != nil {
			return nil, nil, fmt.Errorf("could not put %s back: %w", p.path, err)
		}
		if ctx.Err() != nil {
			return nil, nil, ctx.Err()
		}
	}
	return parts, statuses, nil
}

// codeFile is whether rel is code a check could exercise: not a document,
// and not a test (a check that passes without a test says nothing).
func codeFile(rel string) bool {
	base := filepath.Base(rel)
	switch strings.ToLower(filepath.Ext(base)) {
	case ".md", ".rst", ".txt", ".tex", ".adoc", ".org", "":
		return false
	}
	for _, part := range strings.Split(rel, "/") {
		if part == "test" || part == "tests" || part == "testdata" || part == "testing" {
			return false
		}
	}
	return !(strings.Contains(base, "_test.") || strings.HasPrefix(base, "test_") || strings.Contains(base, ".test.") || strings.Contains(base, ".spec."))
}

// hunksOf is each run of changed lines between old and cur, as cur with that
// run alone put back as it was in old. Runs whose changed lines are all
// comments or blank are left out.
func hunksOf(rel string, old, cur []byte) []withoutPart {
	a, b := splitLines(string(old)), splitLines(string(cur))
	if len(a)+len(b) > withoutMaxLines {
		return nil
	}
	ops := myers(a, b)
	trailing := strings.HasSuffix(string(cur), "\n")
	var parts []withoutPart
	for i := 0; i < len(ops); {
		if ops[i].kind == ' ' {
			i++
			continue
		}
		j := i
		for j < len(ops) && ops[j].kind != ' ' {
			j++
		}
		p := withoutPart{path: rel, kind: "hunk"}
		meaningful := false
		for _, x := range ops[i:j] {
			if x.kind == '+' {
				p.added++
			} else {
				p.removed++
			}
			meaningful = meaningful || !commentOrBlank(x.line)
		}
		if !meaningful {
			i = j
			continue
		}
		// The line in cur where the run starts: its first added line, or for a
		// run that only removes, the line after it.
		p.line = len(b) + 1
		for _, x := range ops[i:] {
			if x.kind == '+' || x.kind == ' ' {
				p.line = x.bi + 1
				break
			}
		}
		var lines []string
		for k, x := range ops {
			inRun := k >= i && k < j
			switch {
			case x.kind == ' ':
				lines = append(lines, x.line)
			case inRun && x.kind == '-', !inRun && x.kind == '+':
				lines = append(lines, x.line)
			}
		}
		text := strings.Join(lines, "\n")
		if trailing && len(lines) > 0 {
			text += "\n"
		}
		p.content = []byte(text)
		parts = append(parts, p)
		i = j
	}
	return parts
}

// commentOrBlank is a line no check can see: blank, or a comment in the usual
// line-comment forms.
func commentOrBlank(line string) bool {
	t := strings.TrimSpace(line)
	for _, prefix := range []string{"//", "#", ";", "/*", "*", "--", "<!--"} {
		if strings.HasPrefix(t, prefix) {
			return true
		}
	}
	return t == ""
}

// writeWithout makes the file what it is with p taken out.
func writeWithout(k *Keeper, o Options, p withoutPart) error {
	abs := filepath.Join(o.Root, filepath.FromSlash(p.path))
	if !k.inside(abs) {
		return errors.New("outside the repository")
	}
	if p.absent {
		return os.Remove(abs)
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return err
	}
	return os.WriteFile(abs, p.content, 0o644)
}

// putBackWithout makes the file what the change left: its bytes and mode, or
// gone when the change had deleted it.
func putBackWithout(k *Keeper, o Options, rel string, orig withoutOriginal) error {
	abs := filepath.Join(o.Root, filepath.FromSlash(rel))
	if !k.inside(abs) {
		return errors.New("outside the repository")
	}
	if !orig.exists {
		err := os.Remove(abs)
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(abs, orig.data, orig.mode); err != nil {
		return err
	}
	return os.Chmod(abs, orig.mode)
}

// runCheck runs the check as shell-exec runs a command, stopped at the
// deadline if that comes first, and says its exit status.
func runCheck(parent context.Context, o Options, name string, args []string, deadline time.Time) (int, error) {
	timeout := o.ShellTimeout
	if timeout <= 0 {
		timeout = 10 * time.Minute
	}
	if left := time.Until(deadline); left < timeout {
		timeout = left
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, pipefail(name, args)...)
	cmd.Dir = o.Root
	cmd.Env = scrubEnv(o.EnvAllow)
	if o.Scratch != "" {
		cmd.Env = append(cmd.Env, "TMPDIR="+o.Scratch)
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 5 * time.Second
	err := runStopping(cmd, io.Discard, io.Discard)
	if parent.Err() != nil {
		return 0, parent.Err()
	}
	if ctx.Err() != nil {
		return 0, fmt.Errorf("timed out after %s", timeout)
	}
	var exitErr *exec.ExitError
	switch {
	case err == nil:
		return 0, nil
	case errors.As(err, &exitErr):
		if s := exitErr.ExitCode(); s >= 0 {
			return s, nil
		}
		return 128, nil
	}
	return 0, err
}
