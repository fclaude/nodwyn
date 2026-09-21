package plain

import (
	"bufio"
	"fmt"
	"io"
	"os"

	"golang.org/x/term"
)

// lineReader reads one line of user input, showing a prompt. On a terminal
// it switches to raw mode for the duration of the read and uses x/term's
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
	term    *term.Terminal
}

func newLineReader(in io.Reader, out io.Writer) *lineReader {
	lr := &lineReader{out: out}
	if f, ok := in.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		lr.tty = f
		lr.term = term.NewTerminal(struct {
			io.Reader
			io.Writer
		}{f, out}, "")
		return lr
	}
	lr.scanner = bufio.NewScanner(in)
	lr.scanner.Buffer(make([]byte, 0, 64<<10), 16<<20)
	return lr
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
		lr.tty = nil
		lr.scanner = bufio.NewScanner(lr.tty)
		return lr.read(prompt)
	}
	defer term.Restore(int(lr.tty.Fd()), old)
	lr.term.SetPrompt(prompt)
	line, err = lr.term.ReadLine()
	if err != nil {
		return "", false
	}
	return line, true
}
