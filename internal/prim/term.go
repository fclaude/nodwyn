package prim

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/creack/pty"

	"github.com/fclaude/brote/scheme"
	"github.com/fclaude/nodwyn/internal/termemu"
)

// Terminals (ADR 48): a program run on a pseudo-terminal, whose screen the
// model reads as text and whose keyboard it presses, so that tmux, neovim,
// less and the like work. The output goes through a terminal emulator
// (charmbracelet/x/vt, behind internal/termemu, since the core may not
// import it), which keeps the screen, the cursor and the history that
// scrolled off.
//
// EXPERIMENTAL AND TEMPORARY. This was added to measure what a terminal is
// worth to a model before deciding whether to make it safe. What the model
// types here is not checked at all: the guard sees the program that
// term-open starts and nothing it is then told to run, so a terminal is a
// shell with the person's privileges, and the denylist and the file pins do
// not reach what a program run in it opens. It is offered only when
// [experimental] terminal is set in config.toml, and it may change or go away.
const (
	termLimit       = 3                      // open at once per session
	termDefaultCols = 100                    // the screen's size unless the model asks for another
	termDefaultRows = 30                     //
	termMinCols     = 20                     //
	termMaxCols     = 250                    //
	termMinRows     = 5                      //
	termMaxRows     = 100                    //
	termScrollback  = 5000                   // lines of history kept for a program that does not use the alternate screen
	termMaxText     = 16 << 10               // the most one term-type sends
	termMaxKeys     = 200                    // the most one term-keys presses
	termOpenQuiet   = 300 * time.Millisecond // how long a new program must be quiet before term-open returns its screen
	termOpenCap     = 3 * time.Second        // and the longest term-open waits for that
	termTypeQuiet   = 150 * time.Millisecond // the same after typing
	termTypeCap     = 2 * time.Second        //
	termWaitQuiet   = 500 * time.Millisecond // how long term-wait without until waits for the program to go quiet
	termWaitDefault = 10                     // seconds term-wait waits unless told otherwise
	termWaitMax     = 60                     // and at most
	termSendCap     = 5 * time.Second        // a program that does not read its input stops a send after this
	termDrain       = 500 * time.Millisecond // after the program exits, how long its last output may still arrive
	termBlankRun    = 3                      // this many blank rows in a row are shown as one line
)

// Terms holds a session's terminals.
type Terms struct {
	mu     sync.Mutex
	next   int
	terms  map[string]*term
	closed bool
	// tmuxDir is where every terminal's tmux keeps its socket (TMUX_TMPDIR).
	// tmux's default, /tmp/tmux-UID, is the person's own tmux: a model that
	// ran tmux there would attach to their sessions and leave its own behind
	// them. The directory is made when the first terminal opens and removed,
	// its server with it, when the session ends.
	tmuxDir string
}

// NewTerms returns an empty set.
func NewTerms() *Terms { return &Terms{terms: map[string]*term{}} }

type term struct {
	id, line   string
	cmd        *exec.Cmd
	ptmx       *os.File
	emu        termemu.Emulator
	eof        chan struct{} // closed when the program's output has ended
	stopOnce   sync.Once
	mu         sync.Mutex
	lastOut    time.Time
	done       bool
	exit       int
	signalName string
}

func (t *term) state() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return stateText(t.done, t.exit, t.signalName)
}

func (t *term) exited() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.done
}

// environment is the program's: the scrubbed one, as a background process
// has it, with the terminal type this emulator speaks, a UTF-8 locale when
// there is none (tmux draws its borders with line characters only under
// one) and a tmux directory of the session's own.
func (ts *Terms) environment(o Options) ([]string, error) {
	if ts.tmuxDir == "" {
		dir, err := os.MkdirTemp("/tmp", "nodwyn-tmux-")
		if err != nil {
			return nil, fmt.Errorf("a terminal needs a private directory for tmux: %w", err)
		}
		ts.tmuxDir = dir
	}
	var env []string
	locale := false
	for _, kv := range childEnv(o) {
		switch {
		case strings.HasPrefix(kv, "TERM="), strings.HasPrefix(kv, "TMUX_TMPDIR="):
			continue
		case strings.HasPrefix(kv, "LANG="), strings.HasPrefix(kv, "LC_ALL="):
			locale = true
		}
		env = append(env, kv)
	}
	env = append(env, "TERM=xterm-256color", "TMUX_TMPDIR="+ts.tmuxDir)
	if !locale {
		lang := "C.UTF-8"
		if runtime.GOOS == "darwin" {
			lang = "en_US.UTF-8"
		}
		env = append(env, "LANG="+lang)
	}
	return env, nil
}

func (ts *Terms) get(id string) (*term, error) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	if t, ok := ts.terms[id]; ok {
		return t, nil
	}
	if len(ts.terms) == 0 {
		return nil, fmt.Errorf("no terminal %q: none is open in this session", id)
	}
	ids := make([]string, 0, len(ts.terms))
	for k := range ts.terms {
		ids = append(ids, k)
	}
	sort.Strings(ids)
	return nil, fmt.Errorf("no terminal %q; this session's are %s", id, strings.Join(ids, ", "))
}

func (ts *Terms) start(o Options, name string, args []string, cols, rows int) (*term, error) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	if ts.closed {
		return nil, errors.New("the session is closing")
	}
	open := 0
	for _, t := range ts.terms {
		if !t.exited() {
			open++
		}
	}
	if open >= termLimit {
		return nil, fmt.Errorf("%d terminals are already open; close one with term-close first", termLimit)
	}
	emu, err := termemu.New(cols, rows, termScrollback)
	if err != nil {
		return nil, err
	}
	env, err := ts.environment(o)
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(name, pipefail(name, args)...)
	cmd.Dir = o.Root
	cmd.Env = env
	// pty.StartWithSize puts the program in a session of its own with the
	// terminal as its controlling one, so its process group is its pid.
	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: uint16(rows), Cols: uint16(cols)})
	if err != nil {
		return nil, err
	}
	ts.next++
	t := &term{
		id: fmt.Sprintf("t%d", ts.next), line: strings.Join(append([]string{name}, args...), " "),
		cmd: cmd, ptmx: ptmx, emu: emu, eof: make(chan struct{}),
		lastOut: time.Now(),
	}
	ts.terms[t.id] = t
	go t.pump()
	go t.read()
	go t.wait()
	return t, nil
}

// pump carries what the emulator produces for the program to it: the keys it
// encodes, and its answers to the program's questions about the terminal
// (neovim asks for the cursor position and the device attributes, and waits).
func (t *term) pump() { _, _ = io.Copy(t.ptmx, t.emu) }

// read feeds the program's output to the emulator.
func (t *term) read() {
	defer close(t.eof)
	// The emulator parses whatever a program prints. A panic in it must not
	// take the daemon down with every other session: it ends this terminal.
	defer func() {
		if recover() != nil {
			t.stop()
		}
	}()
	buf := make([]byte, 32<<10)
	for {
		n, err := t.ptmx.Read(buf)
		if n > 0 {
			_, _ = t.emu.Write(buf[:n])
			t.mu.Lock()
			t.lastOut = time.Now()
			t.mu.Unlock()
		}
		if err != nil {
			return
		}
	}
}

// wait notes how the program ended, once its last output has been read.
func (t *term) wait() {
	code, signalName := exitStatus(t.cmd.Wait())
	select {
	case <-t.eof:
	case <-time.After(termDrain): // something else still holds the terminal open
	}
	t.mu.Lock()
	t.done, t.exit, t.signalName = true, code, signalName
	t.mu.Unlock()
}

// stop hangs up on the program the way closing a terminal window does:
// SIGHUP to its process group, SIGKILL after a grace period, then the
// terminal itself goes. It is safe to call twice.
func (t *term) stop() {
	t.stopOnce.Do(func() {
		if !t.exited() {
			stopProcessGroup(t.cmd.Process.Pid, syscall.SIGHUP, t.exited)
		}
		_ = t.ptmx.Close()
		t.emu.CloseInput()
	})
}

// Close stops every terminal the session opened, and the tmux server any of
// them started.
func (ts *Terms) Close() {
	ts.mu.Lock()
	ts.closed = true
	all := make([]*term, 0, len(ts.terms))
	for _, t := range ts.terms {
		all = append(all, t)
	}
	dir := ts.tmuxDir
	ts.mu.Unlock()
	var wg sync.WaitGroup
	for _, t := range all {
		wg.Add(1)
		go func(t *term) { defer wg.Done(); t.stop() }(t)
	}
	wg.Wait()
	if dir == "" {
		return
	}
	// A tmux server leaves the terminal that started it (it detaches into a
	// session of its own), so hanging up does not reach it. The socket is named
	// with -S, and only if it is there: given just TMUX_TMPDIR, tmux falls back
	// to /tmp/tmux-UID when that directory is gone, which is the person's own
	// server. Close can run twice (a session closes, then its cleanup does), and
	// the second time the directory is gone.
	socket := filepath.Join(dir, fmt.Sprintf("tmux-%d", os.Getuid()), "default")
	if _, err := os.Stat(socket); err == nil {
		if path, err := exec.LookPath("tmux"); err == nil {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			cmd := exec.CommandContext(ctx, path, "-S", socket, "kill-server")
			cmd.Env = []string{"TMUX_TMPDIR=" + dir}
			_ = cmd.Run()
			cancel()
		}
	}
	_ = os.RemoveAll(dir)
}

// Keys. A name is looked up in lower case, and several are the same key.
var termKeyNames = map[string]string{
	"enter": termemu.Enter, "return": termemu.Enter, "escape": termemu.Escape, "esc": termemu.Escape,
	"tab": termemu.Tab, "bspace": termemu.Backspace, "backspace": termemu.Backspace,
	"delete": termemu.Delete, "dc": termemu.Delete, "insert": termemu.Insert, "ic": termemu.Insert,
	"up": termemu.Up, "down": termemu.Down, "left": termemu.Left, "right": termemu.Right,
	"home": termemu.Home, "end": termemu.End,
	"pageup": termemu.PageUp, "pgup": termemu.PageUp, "ppage": termemu.PageUp,
	"pagedown": termemu.PageDown, "pgdn": termemu.PageDown, "npage": termemu.PageDown,
	"f1": "f1", "f2": "f2", "f3": "f3", "f4": "f4", "f5": "f5", "f6": "f6",
	"f7": "f7", "f8": "f8", "f9": "f9", "f10": "f10", "f11": "f11", "f12": "f12",
}

const termKeyHelp = `a single character, a name (Enter Escape Tab BTab Space BSpace Delete Insert Up Down Left Right Home End PageUp PageDown F1 to F12) or C-x or M-x for Control or Alt with a letter`

// parseKey turns "Enter", "a", "C-w", "M-x" or "BTab" into the key press the
// emulator encodes for the program (application cursor keys and the like
// depend on the modes the program has set).
func parseKey(spec string) (termemu.Key, error) {
	bad := fmt.Errorf("unknown key %q: use %s", spec, termKeyHelp)
	rest := spec
	var ctrl, alt bool
	for len(rest) > 2 && rest[1] == '-' {
		switch rest[0] {
		case 'C', 'c':
			ctrl = true
		case 'M', 'm':
			alt = true
		default:
			return termemu.Key{}, bad
		}
		rest = rest[2:]
	}
	if strings.EqualFold(rest, "btab") && !ctrl && !alt {
		return termemu.Key{Name: termemu.Tab, Shift: true}, nil
	}
	if strings.EqualFold(rest, "space") {
		return termemu.Key{Rune: ' ', Ctrl: ctrl, Alt: alt}, nil
	}
	if r, size := utf8.DecodeRuneInString(rest); size == len(rest) && r != utf8.RuneError {
		if ctrl {
			// The control characters the emulator knows: a to z and four
			// punctuation marks.
			lower := r | 0x20
			if lower >= 'a' && lower <= 'z' {
				r = lower
			} else if !strings.ContainsRune(`[\]^_`, r) {
				return termemu.Key{}, bad
			}
		}
		return termemu.Key{Rune: r, Ctrl: ctrl, Alt: alt}, nil
	}
	if name, ok := termKeyNames[strings.ToLower(rest)]; ok && !ctrl {
		return termemu.Key{Name: name, Alt: alt}, nil
	}
	return termemu.Key{}, bad
}

// sendBounded runs a write to a program's input, which waits for the program
// to read it: one that does not (it is busy, or has stopped reading) fails
// the call instead of hanging the session, and an abort ends the wait. The
// write itself ends when the program reads, or exits. look says how to see
// what the program is doing.
func sendBounded(ctx context.Context, id, look string, write func()) error {
	done := make(chan struct{})
	go func() { write(); close(done) }()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(termSendCap):
		return fmt.Errorf("%s is not reading its input; %s", id, look)
	}
}

// await waits for the screen to be ready to read and says why it stopped:
// "matched" when re matches a line of the screen, "quiet" when the program
// has printed nothing for quiet (never with a re), "exited", or "timed out"
// after limit. from is the time the wait began, so output printed before it
// does not count as quiet already.
func (t *term) await(ctx context.Context, limit time.Duration, from time.Time, quiet time.Duration, re *regexp.Regexp) (string, error) {
	end := time.NewTimer(limit)
	defer end.Stop()
	tick := time.NewTicker(25 * time.Millisecond)
	defer tick.Stop()
	for {
		t.mu.Lock()
		last := t.lastOut
		done := t.done
		t.mu.Unlock()
		if last.Before(from) {
			last = from
		}
		switch {
		case re != nil && re.MatchString(strings.Join(t.screenRows(), "\n")):
			return "matched", nil
		case done:
			return "exited", nil
		case re == nil && time.Since(last) >= quiet:
			return "quiet", nil
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-end.C:
			return "timed out", nil
		case <-tick.C:
		}
	}
}

// rowText reads one row of cells, left to right, up to cols or the first
// missing cell. A wide character's placeholder cells hold no text.
func rowText(cell func(x int) (string, bool), cols int) string {
	var b strings.Builder
	for x := range cols {
		s, ok := cell(x)
		if !ok {
			break
		}
		b.WriteString(s)
	}
	return strings.TrimRight(b.String(), " ")
}

// screenRows are the lines of the screen as the program drew them now.
func (t *term) screenRows() []string {
	cols, rows := t.emu.Width(), t.emu.Height()
	out := make([]string, rows)
	for y := range out {
		out[y] = rowText(func(x int) (string, bool) { return t.emu.Cell(x, y) }, cols)
	}
	return out
}

// render is what the model reads: a header with the state, the size and the
// cursor, then each row of the view with its number. offset scrolls the view
// that many lines back into the history of a program that does not use the
// alternate screen.
func (t *term) render(offset int) (string, error) {
	cols, rows := t.emu.Width(), t.emu.Height()
	alt := t.emu.AltScreen()
	history := t.emu.HistoryLen()
	if offset > 0 && alt {
		return "", fmt.Errorf("%s runs a full-screen program, which keeps no history: scroll with its own keys (PageDown, C-d, Down)", t.id)
	}
	offset = min(offset, history)
	first := history - offset // the first line of the view, counting the history then the screen
	live := t.screenRows()
	lines := make([]string, rows)
	for i := range lines {
		idx := first + i
		if idx >= history {
			lines[i] = live[idx-history]
			continue
		}
		lines[i] = rowText(func(x int) (string, bool) { return t.emu.HistoryCell(x, idx) }, cols)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s: %s (%s) %dx%d", t.id, t.state(), t.line, cols, rows)
	last := -1
	for i, l := range lines {
		if l != "" {
			last = i
		}
	}
	if offset == 0 {
		x, y := t.emu.CursorPosition()
		if !t.emu.CursorHidden() {
			fmt.Fprintf(&b, ", cursor row %d col %d", y+1, x+1)
			last = max(last, y)
		}
		if alt {
			b.WriteString(", full-screen")
		}
	} else {
		fmt.Fprintf(&b, ", scrolled back %d lines", offset)
		if above := history - offset; above > 0 {
			fmt.Fprintf(&b, " (%d more above)", above)
		}
	}
	width := len(fmt.Sprint(rows))
	for i := 0; i <= last; i++ {
		// A run of blank rows is one line: most of a shell's screen is
		// blank, and the model reads the screen after every key.
		end := i
		for end <= last && lines[end] == "" {
			end++
		}
		if end-i >= termBlankRun {
			fmt.Fprintf(&b, "\n%*d-%d| (blank)", width, i+1, end)
			i = end - 1
			continue
		}
		fmt.Fprintf(&b, "\n%*d|", width, i+1)
		if lines[i] != "" {
			b.WriteByte(' ')
			b.WriteString(lines[i])
		}
	}
	if last < 0 {
		b.WriteString("\n(the screen is blank)")
	}
	return b.String(), nil
}

const termExperimental = "EXPERIMENTAL and temporary: it may change or go away. "

func termPrims(o Options, ts *Terms) []*scheme.Prim {
	// open fetches the terminal a call names, and refuses to type into one
	// whose program has exited.
	open := func(c *scheme.Call, needRunning bool) (*term, error) {
		id, err := c.String(0)
		if err != nil {
			return nil, err
		}
		t, err := ts.get(id)
		if err != nil {
			return nil, err
		}
		if needRunning && t.exited() {
			return nil, fmt.Errorf("%s has exited; its last screen is still readable with term-screen, and term-close frees it", id)
		}
		return t, nil
	}
	return []*scheme.Prim{
		{
			Name: "term-open",
			Meta: scheme.Meta{
				Doc: termExperimental + "Open a real terminal (a pseudo-terminal, 100 columns by 30 rows unless cols and rows say otherwise) running a program in the repository root, and return its screen. Unlike shell-start it is a terminal, so full-screen programs work: tmux, neovim, less, htop; run cmd \"bash\" for a shell, or the program itself. " +
					"Nothing typed into it is checked: it runs with the person's privileges, and a refusal from another tool is the answer, not something to get around through a terminal. " +
					"Press keys with term-keys and type text with term-type, look with term-screen (scrolling back through history if you need to) and term-wait, and end it with term-close. At most 3 are open, and all are closed when the session ends. For a command that finishes, use shell-exec.",
				Params: []scheme.Param{
					{Name: "cmd", Type: "string", Doc: "one program name or path, without its arguments", Role: "argv"},
					{Name: "args", Type: "list", Doc: "list of argument strings", Role: "argv"},
					{Name: "cols", Type: "integer", Doc: "screen width, 20 to 250; 100 when left out", Optional: true},
					{Name: "rows", Type: "integer", Doc: "screen height, 5 to 100; 30 when left out", Optional: true},
				},
				Host: true, Irreversible: true, TaintSource: scheme.TagShell,
				ConfirmReason: "term-open gives the model a terminal (experimental): what it types there is not checked and runs with your privileges",
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
				size := [2]int64{termDefaultCols, termDefaultRows}
				for i := range size {
					if len(c.Args) > 2+i {
						n, err := c.Int(2 + i)
						if err != nil {
							return scheme.Value{}, err
						}
						if n != 0 {
							size[i] = n
						}
					}
				}
				if size[0] < termMinCols || size[0] > termMaxCols {
					return scheme.Value{}, fmt.Errorf("cols is %d to %d, got %d", termMinCols, termMaxCols, size[0])
				}
				if size[1] < termMinRows || size[1] > termMaxRows {
					return scheme.Value{}, fmt.Errorf("rows is %d to %d, got %d", termMinRows, termMaxRows, size[1])
				}
				began := time.Now()
				t, err := ts.start(o, name, args, int(size[0]), int(size[1]))
				if err != nil {
					return scheme.Value{}, err
				}
				if _, err := t.await(c.Ctx, termOpenCap, began, termOpenQuiet, nil); err != nil {
					return scheme.Value{}, err
				}
				text, err := t.render(0)
				return scheme.Str(text), err
			},
		},
		{
			Name: "term-type",
			Meta: scheme.Meta{
				Doc: termExperimental + "Type text into a terminal, as if at its keyboard; a newline presses Enter. It is typed literally: a word like Escape is typed as letters, so use term-keys for named keys. At most 16 KiB. Returns the screen once the program has stopped printing (waiting at most 2 seconds).",
				Params: []scheme.Param{
					{Name: "id", Type: "string", Doc: "the id term-open returned, such as t1"},
					{Name: "text", Type: "string", Doc: "the text to type"},
				},
				Host: true, TaintSource: scheme.TagShell,
			},
			Handler: func(c *scheme.Call) (scheme.Value, error) {
				t, err := open(c, true)
				if err != nil {
					return scheme.Value{}, err
				}
				text, err := c.String(1)
				if err != nil {
					return scheme.Value{}, err
				}
				if len(text) > termMaxText {
					return scheme.Value{}, fmt.Errorf("text is %d bytes; term-type sends at most %d", len(text), termMaxText)
				}
				// A terminal's Enter key sends a carriage return, which its
				// line discipline turns into the newline a program reads.
				text = strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\r"), "\n", "\r")
				began := time.Now()
				if err := sendBounded(c.Ctx, t.id, "look at it with term-screen", func() { t.emu.SendText(text) }); err != nil {
					return scheme.Value{}, err
				}
				return t.answer(c.Ctx, began, termTypeCap, termTypeQuiet)
			},
		},
		{
			Name: "term-keys",
			Meta: scheme.Meta{
				Doc: termExperimental + "Press keys in a terminal, in order, one list element each: " + termKeyHelp + ". For example [\"Escape\", \":\", \"w\", \"q\", \"Enter\"] or [\"C-w\", \"l\"]. Names are not case-sensitive. At most 200 keys. Returns the screen once the program has stopped printing (waiting at most 2 seconds). To type words, use term-type.",
				Params: []scheme.Param{
					{Name: "id", Type: "string", Doc: "the id term-open returned"},
					{Name: "keys", Type: "list", Doc: "list of key strings"},
				},
				Host: true, TaintSource: scheme.TagShell,
			},
			Handler: func(c *scheme.Call) (scheme.Value, error) {
				t, err := open(c, true)
				if err != nil {
					return scheme.Value{}, err
				}
				items, err := c.List(1)
				if err != nil {
					return scheme.Value{}, err
				}
				if len(items) > termMaxKeys {
					return scheme.Value{}, fmt.Errorf("%d keys; term-keys presses at most %d", len(items), termMaxKeys)
				}
				keys := make([]termemu.Key, len(items))
				for i, it := range items {
					if it.Kind != scheme.KString {
						return scheme.Value{}, errors.New("keys must be a list of strings")
					}
					if keys[i], err = parseKey(it.S); err != nil {
						return scheme.Value{}, err
					}
				}
				began := time.Now()
				if err := sendBounded(c.Ctx, t.id, "look at it with term-screen", func() {
					for _, k := range keys {
						t.emu.SendKey(k)
					}
				}); err != nil {
					return scheme.Value{}, err
				}
				return t.answer(c.Ctx, began, termTypeCap, termTypeQuiet)
			},
		},
		{
			Name: "term-screen",
			Meta: scheme.Meta{
				Doc: termExperimental + "A terminal's screen as it is now, as text: a header (state, size, cursor row and column) and then each row with its number and a | (the number is not on the screen); three or more blank rows in a row are one line, such as 2-19| (blank). With offset, the view scrolled back that many lines through the history of a program that is not full-screen, as a scroll wheel would, to read what has scrolled off; a full-screen program (neovim, less) keeps no history and scrolls with its own keys. Needs no approval.",
				Params: []scheme.Param{
					{Name: "id", Type: "string", Doc: "the id term-open returned"},
					{Name: "offset", Type: "integer", Doc: "lines to scroll back; 0 or left out is the live screen", Optional: true},
				},
				Host: true, TaintSource: scheme.TagShell,
			},
			Handler: func(c *scheme.Call) (scheme.Value, error) {
				t, err := open(c, false)
				if err != nil {
					return scheme.Value{}, err
				}
				offset := int64(0)
				if len(c.Args) > 1 {
					if offset, err = c.Int(1); err != nil {
						return scheme.Value{}, err
					}
					if offset < 0 {
						return scheme.Value{}, fmt.Errorf("offset is 0 or more, got %d", offset)
					}
				}
				text, err := t.render(int(offset))
				return scheme.Str(text), err
			},
		},
		{
			Name: "term-wait",
			Meta: scheme.Meta{
				Doc: termExperimental + "Wait for a terminal, then return its screen. With until, wait until a regular expression matches a line of the screen (the rows as printed, without their numbers; it returns at once if one already does). Without it, wait until the program has printed nothing for half a second. Either way it also ends when the program exits or after seconds (1 to 60; 10 when left out), and the last line says which. Use it after starting something slow, such as a build or a test run, instead of polling term-screen. Needs no approval.",
				Params: []scheme.Param{
					{Name: "id", Type: "string", Doc: "the id term-open returned"},
					{Name: "seconds", Type: "integer", Doc: "the longest to wait, 1 to 60; 10 when left out", Optional: true},
					{Name: "until", Type: "string", Doc: "a regular expression (Go syntax) a line of the screen must match; empty or left out waits for quiet", Optional: true},
				},
				Host: true, TaintSource: scheme.TagShell,
			},
			Handler: func(c *scheme.Call) (scheme.Value, error) {
				t, err := open(c, false)
				if err != nil {
					return scheme.Value{}, err
				}
				seconds := int64(termWaitDefault)
				if len(c.Args) > 1 {
					n, err := c.Int(1)
					if err != nil {
						return scheme.Value{}, err
					}
					if n < 0 || n > termWaitMax {
						return scheme.Value{}, fmt.Errorf("seconds is 1 to %d, got %d", termWaitMax, n)
					}
					if n != 0 {
						seconds = n
					}
				}
				var re *regexp.Regexp
				if len(c.Args) > 2 {
					pattern, err := c.String(2)
					if err != nil {
						return scheme.Value{}, err
					}
					if pattern != "" {
						if re, err = regexp.Compile("(?m)" + pattern); err != nil {
							return scheme.Value{}, fmt.Errorf("until: %w", err)
						}
					}
				}
				began := time.Now()
				why, err := t.await(c.Ctx, time.Duration(seconds)*time.Second, began, termWaitQuiet, re)
				if err != nil {
					return scheme.Value{}, err
				}
				text, err := t.render(0)
				if err != nil {
					return scheme.Value{}, err
				}
				return scheme.Str(fmt.Sprintf("%s\n[%s after %.1fs]", text, why, time.Since(began).Seconds())), nil
			},
		},
		{
			Name: "term-close",
			Meta: scheme.Meta{
				Doc:    termExperimental + "Close a terminal: hang up on its program (SIGHUP to its process group, SIGKILL after 2 seconds) and return its last screen. A tmux server it started keeps running until the session ends, and a later term-open can attach to it. Needs no approval: it ends only what this session opened.",
				Params: []scheme.Param{{Name: "id", Type: "string", Doc: "the id term-open returned"}},
				Host:   true, TaintSource: scheme.TagShell,
			},
			Handler: func(c *scheme.Call) (scheme.Value, error) {
				t, err := open(c, false)
				if err != nil {
					return scheme.Value{}, err
				}
				t.stop()
				text, err := t.render(0)
				ts.mu.Lock()
				delete(ts.terms, t.id)
				ts.mu.Unlock()
				return scheme.Str(text), err
			},
		},
	}
}

// answer waits for the program to settle after input and returns its screen.
func (t *term) answer(ctx context.Context, began time.Time, limit, quiet time.Duration) (scheme.Value, error) {
	if _, err := t.await(ctx, limit, began, quiet, nil); err != nil {
		return scheme.Value{}, err
	}
	text, err := t.render(0)
	return scheme.Str(text), err
}
