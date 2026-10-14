// Package termemu is the seam between the terminal primitives
// (internal/prim, ADR 48) and the terminal emulator that turns a program's
// output into a screen. The emulator is a Charm library (charmbracelet/x/vt),
// and nothing outside internal/frontend and cmd may depend on one (ADR 10,
// scripts/lint-boundaries.sh), so the primitives use only this interface, the
// adapter in internal/frontend/termvt registers itself here, and cmd/nodwyn
// links the adapter in. A binary that does not link one has no terminal.
//
// The package has no dependencies, so the lint passes for whoever imports it.
package termemu

import (
	"errors"
	"sync"
)

// Key is a key press. Name is a canonical key name (below), or empty for the
// character Rune.
type Key struct {
	Name  string
	Rune  rune
	Ctrl  bool
	Alt   bool
	Shift bool
}

// The canonical names of the keys that are not characters.
const (
	Enter     = "enter"
	Escape    = "escape"
	Tab       = "tab"
	Backspace = "backspace"
	Delete    = "delete"
	Insert    = "insert"
	Up        = "up"
	Down      = "down"
	Left      = "left"
	Right     = "right"
	Home      = "home"
	End       = "end"
	PageUp    = "pageup"
	PageDown  = "pagedown"
	// F1 to F12 are "f1" to "f12".
)

// Emulator is a screen that a program's output is written to. It is safe to
// use from several goroutines.
type Emulator interface {
	// Write parses output the program printed.
	Write(p []byte) (int, error)
	// Read returns what the emulator has to send the program: the keys
	// SendKey encodes, and its answers to questions programs ask of a
	// terminal (where the cursor is, what the terminal is). It blocks until
	// there is something, and ends after CloseInput.
	Read(p []byte) (int, error)
	// SendKey and SendText queue input for Read to return. They wait for
	// whoever is reading.
	SendKey(Key)
	SendText(string)
	// CloseInput ends Read, and releases a SendKey or SendText that waits.
	CloseInput()

	Width() int
	Height() int
	// Cell is the text of the cell at column x of row y of the screen as it
	// is now (the alternate screen's, while a program uses it); ok is false
	// outside it. An empty string with ok true is a placeholder, the second
	// half of a wide character.
	Cell(x, y int) (text string, ok bool)
	// HistoryLen is the number of lines that scrolled off the main screen,
	// and HistoryCell reads one, line 0 being the oldest; ok is false past
	// the end of the line, which is shorter than the screen is wide.
	HistoryLen() int
	HistoryCell(x, line int) (text string, ok bool)
	// CursorPosition is the zero-based column and row.
	CursorPosition() (x, y int)
	CursorHidden() bool
	// AltScreen reports whether the program is using the alternate screen,
	// which keeps no history.
	AltScreen() bool
}

// ErrUnavailable is what New returns when no emulator is linked in.
var ErrUnavailable = errors.New("this build of nodwyn has no terminal emulator linked in (internal/frontend/termvt)")

var (
	mu      sync.Mutex
	factory func(cols, rows, history int) Emulator
)

// Register sets the emulator New makes. The adapter calls it from init.
func Register(f func(cols, rows, history int) Emulator) {
	mu.Lock()
	defer mu.Unlock()
	factory = f
}

// New makes an emulator of the given size that keeps up to history lines of
// what scrolls off.
func New(cols, rows, history int) (Emulator, error) {
	mu.Lock()
	f := factory
	mu.Unlock()
	if f == nil {
		return nil, ErrUnavailable
	}
	return f(cols, rows, history), nil
}
