package prim

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/fclaude/brote/scheme"
	"golang.org/x/sys/unix"
)

// Background processes: a server, a watcher, a program to talk to, started
// with shell-start and read, fed and stopped by id. They belong to the
// session: Close stops every one, and none outlives it on purpose.
const (
	procLimit      = 4               // running at once per session
	procBufferSize = 1 << 20         // output kept per process: the latest MiB
	procSettle     = 1 * time.Second // how long shell-start waits for first output
	procMaxWait    = 600             // seconds shell-read may wait: a whole test suite, read once
	procStopGrace  = 2 * time.Second // SIGTERM, then SIGKILL
	procMaxText    = termMaxText     // the most one shell-send types
)

// Procs holds a session's background processes: those shell-start started,
// and the shell-exec commands a lean session left running at the time limit
// (runHolding).
type Procs struct {
	mu     sync.Mutex
	next   int
	procs  map[string]*proc
	held   map[*heldCommand]bool
	closed bool
}

// NewProcs returns an empty set.
func NewProcs() *Procs { return &Procs{procs: map[string]*proc{}, held: map[*heldCommand]bool{}} }

// heldCommand is a shell-exec command left running: its process group, and
// done, closed once it has ended and its files are written and closed.
type heldCommand struct {
	pgid int
	done chan struct{}
}

// hold keeps h until release, unless the session is closing.
func (ps *Procs) hold(h *heldCommand) bool {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	if ps.closed {
		return false
	}
	ps.held[h] = true
	return true
}

func (ps *Procs) release(h *heldCommand) {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	delete(ps.held, h)
}

type proc struct {
	id      string
	line    string
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	started time.Time

	mu         sync.Mutex
	changed    chan struct{} // closed and replaced whenever output or state changes
	buf        []byte        // the latest output: at least the last procBufferSize bytes, trimmed in batches
	total      int64         // bytes ever written
	cursor     int64         // bytes already returned by a read
	done       bool
	exit       int
	signalName string // the signal that ended it, if one did
}

func (p *proc) Write(b []byte) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.buf = append(p.buf, b...)
	// Trimmed in place once there is a buffer's worth too much, not by
	// copying the whole buffer on every write once it is full: a watcher
	// printing a line at a time was throttled to about 2,000 lines a second.
	if len(p.buf) > 2*procBufferSize {
		p.buf = p.buf[:copy(p.buf, p.buf[len(p.buf)-procBufferSize:])]
	}
	p.total += int64(len(b))
	p.signal()
	return len(b), nil
}

// signal wakes readers; p.mu is held.
func (p *proc) signal() {
	close(p.changed)
	p.changed = make(chan struct{})
}

// take returns the output not yet read, at most limit bytes of it, and what
// the read should say about bytes dropped from the buffer or left for later.
func (p *proc) take(limit int) (text string, dropped, left int64) {
	kept := p.buf[max(0, len(p.buf)-procBufferSize):]
	start := p.total - int64(len(kept))
	if p.cursor < start {
		dropped = start - p.cursor
		p.cursor = start
	}
	chunk := kept[p.cursor-start:]
	if limit > 0 && len(chunk) > limit {
		left = int64(len(chunk) - limit)
		chunk = chunk[:limit]
	}
	p.cursor += int64(len(chunk))
	return string(chunk), dropped, left
}

func (p *proc) state() string { return stateText(p.done, p.exit, p.signalName) }

// stateText is how a read says whether a process is still running or how it
// ended; terminals (term.go) say it the same way.
func stateText(done bool, exit int, signalName string) string {
	if done && signalName != "" {
		return fmt.Sprintf("exited %d (%s)", exit, signalName)
	}
	if done {
		return fmt.Sprintf("exited %d", exit)
	}
	return "running"
}

// procReadSettle is how long a waiting read that has new output waits on,
// for more of it or for the exit, before it returns. A check that prints its
// summary and then exits was read as still running: on commit-bench (nodwyn
// dsl6, 556d6d12) make validate's PASS came with "running", the model read
// no further, and the loop never saw the run end (2026-10-13).
var procReadSettle = time.Second

// exitStatus is the code a finished command reports and, when a signal
// ended it, the signal's name: a shell's convention of 128 plus the number.
func exitStatus(err error) (code int, signalName string) {
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		code = exit.ExitCode()
		if ws, ok := exit.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
			return 128 + int(ws.Signal()), "stopped by " + unix.SignalName(ws.Signal())
		}
		return code, ""
	}
	if err != nil {
		return 1, ""
	}
	return 0, ""
}

// read waits up to wait for new output or an exit, then returns what the
// model reads: the id and state on the first line, then the new output.
// Once there is new output it waits settleFor more for the rest, or for the
// exit, within wait (shell-read gives procReadSettle).
func (p *proc) read(ctx context.Context, wait time.Duration, limit int, settleFor time.Duration) (string, error) {
	deadline := time.NewTimer(wait)
	defer deadline.Stop()
	var settle *time.Timer
	settled := false
	defer func() {
		if settle != nil {
			settle.Stop()
		}
	}()
	for {
		p.mu.Lock()
		if p.cursor < p.total && settle == nil && settleFor > 0 && wait > 0 && !p.done {
			settle = time.NewTimer(settleFor)
		}
		if settleFor <= 0 {
			settled = true
		}
		if wait <= 0 || p.done || p.cursor < p.total && settled {
			text, dropped, left := p.take(limit)
			var b strings.Builder
			fmt.Fprintf(&b, "%s: %s (%s)\n", p.id, p.state(), p.line)
			if dropped > 0 {
				fmt.Fprintf(&b, "[%d bytes of earlier output were dropped: only the latest %d are kept]\n", dropped, procBufferSize)
			}
			b.WriteString(text)
			if left > 0 {
				fmt.Fprintf(&b, "\n[%d more bytes: read again]", left)
			}
			p.mu.Unlock()
			return b.String(), nil
		}
		changed := p.changed
		p.mu.Unlock()
		var settleC <-chan time.Time
		if settle != nil {
			settleC = settle.C
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-deadline.C:
			wait = 0
		case <-settleC:
			settled = true
		case <-changed:
		}
	}
}

// stop asks the process group to end, kills it after procStopGrace, and
// waits a moment more for the kill to be seen, so what it reports next is
// that the process ended, not that it still runs.
func (p *proc) stop() {
	if p.cmd.Process == nil || p.exited() {
		return
	}
	stopProcessGroup(p.cmd.Process.Pid, syscall.SIGTERM, p.exited)
}

func (p *proc) exited() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.done
}

func (ps *Procs) get(id string) (*proc, error) {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	if p, ok := ps.procs[id]; ok {
		return p, nil
	}
	ids := make([]string, 0, len(ps.procs))
	for k := range ps.procs {
		ids = append(ids, k)
	}
	sort.Strings(ids)
	if len(ids) == 0 {
		return nil, fmt.Errorf("no process %q: none was started in this session", id)
	}
	return nil, fmt.Errorf("no process %q; this session's are %s", id, strings.Join(ids, ", "))
}

func (ps *Procs) start(o Options, name string, args []string) (*proc, error) {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	if ps.closed {
		return nil, errors.New("the session is closing")
	}
	running := 0
	for _, p := range ps.procs {
		p.mu.Lock()
		if !p.done {
			running++
		}
		p.mu.Unlock()
	}
	if running >= procLimit {
		return nil, fmt.Errorf("%d processes are already running; stop one with shell-stop first", procLimit)
	}
	ps.next++
	p := &proc{id: fmt.Sprintf("p%d", ps.next), line: strings.Join(append([]string{name}, args...), " "), started: time.Now(), changed: make(chan struct{})}
	// A bash -c line reports its pipeline's failure, as shell-exec's does.
	cmd := exec.Command(name, pipefail(name, args)...)
	cmd.Dir = o.Root
	cmd.Env = childEnv(o)
	cmd.Stdout, cmd.Stderr = p, p
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	p.cmd, p.stdin = cmd, stdin
	ps.procs[p.id] = p
	go func() {
		code, signal := exitStatus(cmd.Wait())
		p.mu.Lock()
		p.done, p.exit, p.signalName = true, code, signal
		p.signal()
		p.mu.Unlock()
	}()
	return p, nil
}

// Close stops every process the session started, and returns once each
// shell-exec command left running has ended and its files are closed, so the
// scratch directory can go.
func (ps *Procs) Close() {
	ps.mu.Lock()
	ps.closed = true
	all := make([]*proc, 0, len(ps.procs))
	for _, p := range ps.procs {
		all = append(all, p)
	}
	held := make([]*heldCommand, 0, len(ps.held))
	for h := range ps.held {
		held = append(held, h)
	}
	ps.mu.Unlock()
	var wg sync.WaitGroup
	for _, p := range all {
		wg.Add(1)
		go func(p *proc) { defer wg.Done(); p.stop() }(p)
	}
	for _, h := range held {
		wg.Add(1)
		go func(h *heldCommand) {
			defer wg.Done()
			stopProcessGroup(h.pgid, syscall.SIGTERM, func() bool { return syscall.Kill(-h.pgid, 0) != nil })
			<-h.done
		}(h)
	}
	wg.Wait()
}

func procPrims(o Options, ps *Procs) []*scheme.Prim {
	return []*scheme.Prim{
		{
			Name: "shell-start",
			Meta: scheme.Meta{
				Doc: "Start a program that keeps running (a server, a watcher, a program to talk to) in the repository root, in the background, with no shell: pass arguments as a list. Returns its id, whether it is running, and its first output. Read more with shell-read, type into it with shell-send, end it with shell-stop; it is stopped when the session ends. For a command that finishes, use shell-exec.",
				Params: []scheme.Param{
					{Name: "cmd", Type: "string", Doc: "one program name or path, without its arguments", Role: "argv"},
					{Name: "args", Type: "list", Doc: "list of argument strings", Role: "argv"},
				},
				Host: true, Irreversible: true, TaintSource: scheme.TagShell,
			},
			Handler: func(c *scheme.Call) (scheme.Value, error) {
				name, err := c.String(0)
				if err != nil {
					return scheme.Value{}, err
				}
				args, err := stringList(c, 1)
				if err != nil {
					return scheme.Value{}, err
				}
				p, err := ps.start(o, name, args)
				if err != nil {
					return scheme.Value{}, err
				}
				text, err := p.read(c.Ctx, procSettle, o.MaxOutput, 0)
				if err != nil {
					return scheme.Value{}, err
				}
				return scheme.Str(text), nil
			},
		},
		{
			Name: "shell-read",
			Meta: scheme.Meta{
				Doc: "Read what a background process printed since the last read, and whether it is still running or how it exited. With wait, first wait up to that many seconds (at most 600) for new output or an exit. Needs no approval.",
				Params: []scheme.Param{
					{Name: "id", Type: "string", Doc: "the id shell-start returned, such as p1"},
					{Name: "wait", Type: "integer", Doc: "seconds to wait for new output, 0 to 600; 0 returns at once", Optional: true},
				},
				Host: true, TaintSource: scheme.TagShell,
			},
			Handler: func(c *scheme.Call) (scheme.Value, error) {
				id, err := c.String(0)
				if err != nil {
					return scheme.Value{}, err
				}
				wait := int64(0)
				if len(c.Args) > 1 {
					if wait, err = c.Int(1); err != nil {
						return scheme.Value{}, err
					}
					if wait < 0 {
						return scheme.Value{}, fmt.Errorf("wait is 0 to %d seconds, got %d", procMaxWait, wait)
					}
					wait = min(wait, procMaxWait)
				}
				p, err := ps.get(id)
				if err != nil {
					return scheme.Value{}, err
				}
				text, err := p.read(c.Ctx, time.Duration(wait)*time.Second, o.MaxOutput, procReadSettle)
				if err != nil {
					return scheme.Value{}, err
				}
				return scheme.Str(text), nil
			},
		},
		{
			Name: "shell-send",
			Meta: scheme.Meta{
				Doc: "Type text into a background process's input, as if at its keyboard: end a line with \\n. At most 16 KiB. Returns what it printed in the next second.",
				Params: []scheme.Param{
					{Name: "id", Type: "string", Doc: "the id shell-start returned"},
					{Name: "text", Type: "string", Doc: "the text to send, usually ending in \\n", Role: "payload"},
				},
				Host: true, Irreversible: true, TaintSource: scheme.TagShell,
				ConfirmReason: "shell-send types into a running program; what that does depends on the program",
			},
			Handler: func(c *scheme.Call) (scheme.Value, error) {
				id, err := c.String(0)
				if err != nil {
					return scheme.Value{}, err
				}
				text, err := c.String(1)
				if err != nil {
					return scheme.Value{}, err
				}
				p, err := ps.get(id)
				if err != nil {
					return scheme.Value{}, err
				}
				p.mu.Lock()
				done := p.done
				p.mu.Unlock()
				if done {
					return scheme.Value{}, fmt.Errorf("%s has exited; its output is still readable with shell-read", id)
				}
				if len(text) > procMaxText {
					return scheme.Value{}, fmt.Errorf("text is %d bytes; shell-send sends at most %d", len(text), procMaxText)
				}
				var werr error
				if err := sendBounded(c.Ctx, id, "see what it printed with shell-read", func() { _, werr = io.WriteString(p.stdin, text) }); err != nil {
					return scheme.Value{}, err
				}
				if werr != nil {
					return scheme.Value{}, fmt.Errorf("%s: %w", id, werr)
				}
				out, err := p.read(c.Ctx, procSettle, o.MaxOutput, 0)
				if err != nil {
					return scheme.Value{}, err
				}
				return scheme.Str(out), nil
			},
		},
		{
			Name: "shell-stop",
			Meta: scheme.Meta{
				Doc:    "Stop a background process (SIGTERM, then SIGKILL after 2 seconds) and return its last output and how it exited.",
				Params: []scheme.Param{{Name: "id", Type: "string", Doc: "the id shell-start returned"}},
				Host:   true, TaintSource: scheme.TagShell,
			},
			Handler: func(c *scheme.Call) (scheme.Value, error) {
				id, err := c.String(0)
				if err != nil {
					return scheme.Value{}, err
				}
				p, err := ps.get(id)
				if err != nil {
					return scheme.Value{}, err
				}
				p.stop()
				text, err := p.read(c.Ctx, 0, o.MaxOutput, 0)
				if err != nil {
					return scheme.Value{}, err
				}
				return scheme.Str(text), nil
			},
		},
	}
}
