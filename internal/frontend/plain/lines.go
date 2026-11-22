package plain

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/fclaude/nodwyn/internal/frontend/plain/lineedit"

	"golang.org/x/term"
)

// lineReader reads one line of user input, showing a prompt. On a terminal
// it switches to raw mode for the duration of the read and uses the VT100
// line editor: the kernel's canonical-mode line limit (1024 bytes on macOS)
// no longer applies, pasted text of any length arrives intact, and
// bracketed paste is handled. Raw mode is restored after every read, so
// while a turn runs the terminal is cooked and Ctrl-C still delivers
// SIGINT to abort it. Anything that is not a terminal (a pipe, a file)
// is read with a plain scanner.
type lineReader struct {
	scanner *bufio.Scanner // non-tty
	out     io.Writer
	tty     *os.File // tty, or nil
	term    *lineedit.Terminal
}

func newLineReader(in io.Reader, out io.Writer) *lineReader {
	lr := &lineReader{out: out}
	if f, ok := in.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		lr.tty = f
		// The editor draws with escape sequences (bracketed paste on and
		// off, cursor moves) and, in raw mode, its own carriage returns:
		// through TerminalText they reached the terminal as "[?2004h" and
		// "[D", bracketed paste never turned on, and a pasted message
		// arrived as several. It writes to the terminal itself; its prompt
		// is cleaned instead (read).
		editor := out
		if t, ok := out.(terminalText); ok {
			editor = t.w
		}
		lr.term = lineedit.NewTerminal(struct {
			io.Reader
			io.Writer
		}{f, editor}, "")
		return lr
	}
	lr.useScanner(in)
	return lr
}

func (lr *lineReader) useScanner(in io.Reader) {
	lr.scanner = bufio.NewScanner(in)
	lr.scanner.Buffer(make([]byte, 0, 64<<10), 16<<20)
	lr.tty = nil
}

// Flush both the terminal driver and the editor, which may already have read
// several lines in one system call.
func (lr *lineReader) discardTypeAhead() {
	_ = discardTypeAhead(int(lr.tty.Fd()))
	lr.term.DiscardInput()
}

// answer reads the answer to a question (a confirm, a choice, a limit). On a
// terminal, what was typed before the question was shown is dropped: the
// terminal queues keys pressed while a turn runs, and the next read took a
// "yes" typed to an earlier question, or to nothing, as the answer to a
// security confirm nobody had read.
func (lr *lineReader) answer(prompt string) (line string, ok bool) {
	if lr.tty != nil {
		lr.discardTypeAhead()
	}
	return lr.read(prompt)
}

// read returns the next line without its newline. ok is false at EOF (or
// Ctrl-C / Ctrl-D on a terminal).
func (lr *lineReader) read(prompt string) (line string, ok bool) {
	if lr.tty == nil {
		fmt.Fprint(lr.out, prompt)
		if !lr.scanner.Scan() {
			return "", false
		}
		return lr.scanner.Text(), true
	}
	old, err := term.MakeRaw(int(lr.tty.Fd()))
	if err != nil {
		// Not really a tty after all: fall back for this and later reads.
		lr.useScanner(lr.tty)
		return lr.read(prompt)
	}
	defer term.Restore(int(lr.tty.Fd()), old)
	lr.term.SetBracketedPasteMode(true)
	defer lr.term.SetBracketedPasteMode(false)
	lr.term.SetPrompt(controlsOut(prompt))
	var pasted strings.Builder
	for {
		line, err = lr.term.ReadLine()
		if err == nil {
			if pasted.Len() == 0 {
				return line, true
			}
			pasted.WriteString(line)
			return pasted.String(), true
		}
		if err != lineedit.ErrPasteIndicator {
			return "", false
		}
		// Enter inside bracketed paste belongs to the same message.
		pasted.WriteString(line)
		pasted.WriteByte('\n')
		lr.term.SetPrompt("")
	}
}

// secret reads one masked line: the user's input is not echoed. It is used
// for credential-register's masked secret input (ADR 5). On a terminal it
// uses term.ReadPassword, which disables echo for the duration of the read.
func (lr *lineReader) secret(prompt string) (line string, ok bool) {
	if lr.tty == nil {
		fmt.Fprint(lr.out, prompt)
		if !lr.scanner.Scan() {
			return "", false
		}
		return lr.scanner.Text(), true
	}
	lr.discardTypeAhead()
	fmt.Fprint(lr.out, prompt)
	buf, err := term.ReadPassword(int(lr.tty.Fd()))
	if err != nil {
		return "", false
	}
	fmt.Fprintln(lr.out) // move to a fresh line after the masked input
	return string(buf), true
}
