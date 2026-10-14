// Package termvt is the terminal emulator the terminal primitives use
// (ADR 48): charmbracelet/x/vt behind the termemu interface. It lives under
// internal/frontend because the boundary lint (ADR 10) allows Charm
// libraries only there and in cmd; importing it registers the emulator, and
// cmd/nodwyn does so.
package termvt

import (
	"io"

	"github.com/charmbracelet/x/vt"

	"github.com/fclaude/nodwyn/internal/termemu"
)

func init() {
	termemu.Register(func(cols, rows, history int) termemu.Emulator {
		emu := vt.NewSafeEmulator(cols, rows)
		emu.SetScrollbackSize(history)
		return &emulator{SafeEmulator: emu}
	})
}

type emulator struct{ *vt.SafeEmulator }

// keyCodes are the emulator's codes for the canonical names of termemu.
var keyCodes = map[string]rune{
	termemu.Enter: vt.KeyEnter, termemu.Escape: vt.KeyEscape, termemu.Tab: vt.KeyTab,
	termemu.Backspace: vt.KeyBackspace, termemu.Delete: vt.KeyDelete, termemu.Insert: vt.KeyInsert,
	termemu.Up: vt.KeyUp, termemu.Down: vt.KeyDown, termemu.Left: vt.KeyLeft, termemu.Right: vt.KeyRight,
	termemu.Home: vt.KeyHome, termemu.End: vt.KeyEnd, termemu.PageUp: vt.KeyPgUp, termemu.PageDown: vt.KeyPgDown,
	"f1": vt.KeyF1, "f2": vt.KeyF2, "f3": vt.KeyF3, "f4": vt.KeyF4, "f5": vt.KeyF5, "f6": vt.KeyF6,
	"f7": vt.KeyF7, "f8": vt.KeyF8, "f9": vt.KeyF9, "f10": vt.KeyF10, "f11": vt.KeyF11, "f12": vt.KeyF12,
}

// SendKey encodes k the way the program's terminal modes ask for it
// (application cursor keys, for one). A key the emulator cannot encode is
// dropped; the primitives only offer ones it can.
func (e *emulator) SendKey(k termemu.Key) {
	var mod vt.KeyMod
	if k.Ctrl {
		mod |= vt.ModCtrl
	}
	if k.Alt {
		mod |= vt.ModAlt
	}
	if k.Shift {
		mod |= vt.ModShift
	}
	if k.Name != "" {
		if code, ok := keyCodes[k.Name]; ok {
			e.SafeEmulator.SendKey(vt.KeyPressEvent{Code: code, Mod: mod})
		}
		return
	}
	press := vt.KeyPressEvent{Code: k.Rune, Mod: mod}
	// A control character carries no text, which is how the emulator
	// recognises it; any other key is the character it types.
	if !k.Ctrl {
		press.Text = string(k.Rune)
	}
	e.SafeEmulator.SendKey(press)
}

func (e *emulator) CloseInput() {
	// The emulator's own Close would end the pipe too, but writes a flag its
	// readers read unlocked; closing the pipe alone ends the reader and
	// releases a writer without that.
	if pw, ok := e.InputPipe().(*io.PipeWriter); ok {
		_ = pw.CloseWithError(io.EOF)
	}
}

func (e *emulator) Cell(x, y int) (string, bool) {
	c := e.CellAt(x, y)
	if c == nil {
		return "", false
	}
	return c.Content, true
}

func (e *emulator) HistoryLen() int { return e.ScrollbackLen() }

func (e *emulator) HistoryCell(x, line int) (string, bool) {
	c := e.ScrollbackCellAt(x, line)
	if c == nil {
		return "", false
	}
	return c.Content, true
}

func (e *emulator) CursorPosition() (int, int) {
	p := e.SafeEmulator.CursorPosition()
	return p.X, p.Y
}

func (e *emulator) AltScreen() bool { return e.IsAltScreen() }
