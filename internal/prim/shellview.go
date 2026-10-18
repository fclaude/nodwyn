package prim

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/fclaude/nodwyn/internal/scheme"
)

// A long output is not poured into the model's context and is not for the
// model to trim with `| tail`; shell-exec shows a bounded view of it and
// gives a handle to the rest, as a pager does: the first lines, the last
// lines, and where the lines between are. 84% of the benchmark's test runs
// were piped into `| tail -N` or `| head -N`, and in 30% of the runs that
// executed (362 of 1,208) the output then showed no verdict: Django's
// parallel runner prints only "Destroying test database" lines at the end,
// and the model ran the suite again with other flags, a call and minutes
// each. The lines were never lost to the tool, only to the pipe (ADR 32,
// amended 2026-10-17).
const (
	viewBytes  = 8 << 10 // a stream over this many bytes, or
	viewLines  = 150     // this many lines, is shown as a view
	viewHead   = 20      // lines of its start
	viewTail   = 50      // lines of its end
	viewBudget = 6 << 10 // the most a view takes, so the window, which cuts a result over max_result_tokens (3,072 tokens: 9 KiB), never cuts it again
	viewLine   = 300     // characters of a line it shows
	spoolLimit = 8 << 20 // the most of a stream the saved file holds
)

// outputFiles numbers the saved outputs of a session and makes their files:
// <scratch>/out/N.stdout and N.stderr, N being 1, 2, 3 ... per session and
// shared by the two streams of a call.
type outputFiles struct {
	mu   sync.Mutex
	last int
}

// callFiles is what the streams of one call share: the number it was given,
// 0 until a stream needs a file.
type callFiles struct {
	n int
}

// create makes the file for one stream of a call, claiming the call's number
// first if it has none. The files are made exclusively: a scratch directory
// that already holds a number (another session's primitives sharing it) is
// not overwritten, the next number is taken.
func (f *outputFiles) create(scratch string, call *callFiles, stream string) (*os.File, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	dir := filepath.Join(scratch, "out")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, "", err
	}
	open := func(n int) (*os.File, string, error) {
		path := filepath.Join(dir, strconv.Itoa(n)+"."+stream)
		file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		return file, path, err
	}
	if call.n != 0 {
		file, path, err := open(call.n)
		return file, path, err
	}
	for {
		n := f.last + 1
		file, path, err := open(n)
		switch {
		case err == nil:
			f.last, call.n = n, n
			return file, path, nil
		case os.IsExist(err):
			f.last = n
		default:
			return nil, "", err
		}
	}
}

// outputTap sits in front of a stream's capped writer. It keeps the stream's
// first and last lines and counts the rest, as it passes, and once the stream
// is over a view's thresholds it writes the whole of it to a file, so that
// what the capped writer drops from the middle is on disk. A stream that
// stays under the thresholds costs nothing but the few kilobytes held until
// it is known to.
type outputTap struct {
	next    io.Writer
	files   *outputFiles
	call    *callFiles
	scratch string
	stream  string // "stdout" or "stderr"

	bytes   int
	head    []string // the first viewHead lines, cut for display
	ring    []string // the last viewTail finished lines, in a ring
	ringAt  int
	done    int    // finished lines
	cur     []byte // the unfinished line, its first bytes
	curLen  int    // its whole length
	kept    []byte // the stream so far, until it has a file
	file    *os.File
	failed  bool // the file could not be made or written: there is no handle
	path    string
	written int  // bytes in the file
	fileEnd int  // line ends in the file
	limited bool // the file stopped at spoolLimit
	partial bool // the stream's last line has no line end

	// mu is held by Write and by what reads the tap while the command may
	// still write: detach, and finish once it is left running.
	mu sync.Mutex
	// onlyHeld is a stream that is never shown as a view (a command that
	// reads a range or limits its own output): it gets a file only when its
	// command is left running at the time limit (runHolding), holding up to
	// spoolLimit until then.
	onlyHeld bool
	// detached is set once the command is left running: what it writes goes
	// to the file alone.
	detached bool
}

func newOutputTap(next io.Writer, files *outputFiles, call *callFiles, scratch, stream string) *outputTap {
	return &outputTap{next: next, files: files, call: call, scratch: scratch, stream: stream}
}

// lineKeep is how much of a line the tap holds: more than viewLine characters
// can take, so a line cut there is a line cut at viewLine characters.
const lineKeep = 4*viewLine + 4

func (t *outputTap) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.detached {
		t.spoolBytes(p)
		return len(p), nil
	}
	t.bytes += len(p)
	for rest := p; len(rest) > 0; {
		i := bytes.IndexByte(rest, '\n')
		if i < 0 {
			t.hold(rest)
			break
		}
		t.hold(rest[:i])
		t.finishLine()
		rest = rest[i+1:]
	}
	switch {
	case t.file != nil:
		t.spoolBytes(p)
	case t.onlyHeld:
		t.kept = append(t.kept, p[:min(len(p), max(spoolLimit-len(t.kept), 0))]...)
	case !t.failed:
		t.kept = append(t.kept, p...)
		if t.bytes > viewBytes || t.lines() > viewLines {
			t.open()
		}
	}
	return t.next.Write(p)
}

// detach turns the stream to its file alone, made now if the stream had none,
// for a command left running, and returns the view of what it printed so
// far, its unfinished line included, within budget bytes; false when the file
// cannot be made.
func (t *outputTap) detach(budget int) (string, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.file == nil && !t.failed {
		t.open()
	}
	if t.file == nil || t.failed {
		return "", false
	}
	t.detached = true
	if t.done == 0 {
		return t.display(), true
	}
	v, _ := t.view(budget)
	if t.curLen > 0 {
		v += t.display()
	}
	return v, true
}

// hold adds to the unfinished line.
func (t *outputTap) hold(p []byte) {
	if room := lineKeep - len(t.cur); room > 0 {
		t.cur = append(t.cur, p[:min(room, len(p))]...)
	}
	t.curLen += len(p)
}

// finishLine ends the unfinished line.
func (t *outputTap) finishLine() {
	line := t.display()
	t.done++
	if len(t.head) < viewHead {
		t.head = append(t.head, line)
	}
	if len(t.ring) < viewTail {
		t.ring = append(t.ring, line)
	} else {
		t.ring[t.ringAt] = line
		t.ringAt = (t.ringAt + 1) % viewTail
	}
	t.cur, t.curLen = t.cur[:0], 0
}

// display is the unfinished line as a view shows it: without its carriage
// return, and cut at viewLine characters with an ellipsis.
func (t *outputTap) display() string {
	s := strings.TrimSuffix(string(t.cur), "\r")
	if t.curLen > len(t.cur) || utf8.RuneCountInString(s) > viewLine {
		runes := 0
		for i := range s {
			if runes == viewLine {
				return s[:i] + "…"
			}
			runes++
		}
		return s + "…" // the line goes on past what is held
	}
	return s
}

// lines is how many lines the stream has so far: those finished, and one for
// what follows the last line end.
func (t *outputTap) lines() int {
	if t.curLen > 0 {
		return t.done + 1
	}
	return t.done
}

// open makes the file, with the stream so far in it. A stream whose file
// cannot be made has no handle, and is shown as it always was.
func (t *outputTap) open() {
	file, path, err := t.files.create(t.scratch, t.call, t.stream)
	if err != nil {
		t.failed, t.kept = true, nil
		return
	}
	t.file, t.path = file, path
	first := t.kept
	t.kept = nil
	t.spoolBytes(first)
}

// spoolBytes writes more of the stream to the file, up to spoolLimit, and
// counts the lines it holds.
func (t *outputTap) spoolBytes(p []byte) {
	if t.file == nil || len(p) == 0 {
		return
	}
	if room := spoolLimit - t.written; len(p) > room {
		p = p[:max(room, 0)]
		t.limited = true
	}
	if len(p) == 0 {
		return
	}
	if _, err := t.file.Write(p); err != nil {
		t.failed = true
		return
	}
	t.written += len(p)
	t.fileEnd += bytes.Count(p, []byte{'\n'})
}

// finish ends the stream: its last line, which has no line end, is a line of
// its own, and the file is closed.
func (t *outputTap) finish() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.curLen > 0 {
		t.finishLine()
		t.partial = true
	}
	if t.file != nil {
		if err := t.file.Close(); err != nil {
			t.failed = true
		}
	}
}

// view is the stream as the model reads it: its first lines, a marker that
// says which lines are not shown and where the whole is, and its last lines,
// within budget bytes; false when the stream has no file to point to.
func (t *outputTap) view(budget int) (string, bool) {
	if t.file == nil || t.failed {
		return "", false
	}
	total := t.done
	at := func(n int) string { // line n, 1-based
		if n <= len(t.head) {
			return t.head[n-1]
		}
		// The ring holds the last lines; once full its oldest is at ringAt.
		idx := len(t.ring) - 1 - (total - n)
		if len(t.ring) == viewTail {
			idx = (t.ringAt + idx) % viewTail
		}
		return t.ring[idx]
	}
	h := min(viewHead, total)
	n := min(viewTail, total-h)
	build := func(h, n int) string {
		var parts []string
		for i := 1; i <= h; i++ {
			parts = append(parts, at(i))
		}
		cut := false
		if total-n >= h+1 {
			parts = append(parts, t.marker(h+1, total-n, total))
		} else {
			// Nothing is missing but what long lines were cut of.
			for i := 1; i <= total; i++ {
				cut = cut || strings.HasSuffix(at(i), "…")
			}
		}
		for i := max(total-n+1, h+1); i <= total; i++ {
			parts = append(parts, at(i))
		}
		if cut {
			parts = append(parts, t.footer())
		}
		out := strings.Join(parts, "\n")
		if !t.partial || cut {
			out += "\n"
		}
		return out
	}
	out := build(h, n)
	for len(out) > budget && (h > 1 || n > 1) {
		h, n = max(1, h*4/5), max(1, n*4/5)
		out = build(h, n)
	}
	return out, true
}

// viewable is whether the stream will be shown as a view: it is long and its
// file was made.
func (t *outputTap) viewable() bool { return t.file != nil && !t.failed }

// handle is how the model reaches the file: the path as the shell it runs
// writes it, with TMPDIR the scratch directory.
func (t *outputTap) handle() string {
	return "$TMPDIR/out/" + filepath.Base(t.path)
}

// marker stands in a view where lines from..to of total are not shown, like
// a pager's --More--, and says how to see them.
func (t *outputTap) marker(from, to, total int) string {
	h := t.handle()
	var b strings.Builder
	fmt.Fprintf(&b, "[... lines %d-%d of %d not shown; full output is saved at %s", from, to, total, h)
	if t.limited {
		fmt.Fprintf(&b, " (the file holds the first %s of the output, up to line %d)", byteCount(spoolLimit), t.fileEnd)
	}
	if held := t.fileLines(total); from <= held {
		fmt.Fprintf(&b, ": sed -n '%d,%dp' %s shows a range, grep -n PATTERN %s searches it", from, min(to, from+99, held), h, h)
	}
	b.WriteString(" ...]")
	return b.String()
}

// fileLines is how many of the stream's lines the file holds.
func (t *outputTap) fileLines(total int) int {
	if t.limited {
		return t.fileEnd
	}
	return total
}

// footer ends a view whose lines are all there but cut short.
func (t *outputTap) footer() string {
	h := t.handle()
	return fmt.Sprintf("[... lines are cut at %d characters; full output is saved at %s: grep -n PATTERN %s searches it ...]", viewLine, h, h)
}

// directCall is whether the call is the model's own, as the loop makes it,
// and not one a program of the scheme tool or a strategy makes while it
// computes with the output: a program that splits and filters a result needs
// the whole of it.
func directCall(ctx context.Context) bool {
	return scheme.OriginFrom(ctx) == scheme.OriginModel && ctx.Value(nestedKey{}) == nil
}

// nestedKey marks the context of a program of the scheme tool.
type nestedKey struct{}
