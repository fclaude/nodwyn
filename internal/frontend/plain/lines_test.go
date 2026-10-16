package plain

import (
	"io"
	"testing"
	"time"

	"github.com/creack/pty"
)

// On a terminal, keys typed before a question is shown do not answer it:
// the terminal queues what is typed while a turn runs, and a "yes" typed
// earlier answered the next confirm, a security one included, unread. The
// next message, typed ahead, is kept.
func TestTypeAheadDoesNotAnswerAQuestion(t *testing.T) {
	master, slave, err := pty.Open()
	if err != nil {
		t.Skip("no pseudo-terminal:", err)
	}
	defer master.Close()
	defer slave.Close()
	go io.Copy(io.Discard, master) // what the reader echoes
	lr := newLineReader(slave, slave)

	if _, err := master.Write([]byte("hello\n")); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	if line, ok := lr.read("you> "); !ok || line != "hello" {
		t.Fatalf("a message typed ahead: %q %v", line, ok)
	}

	if _, err := master.Write([]byte("yes\n")); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	go func() {
		time.Sleep(300 * time.Millisecond)
		master.Write([]byte("n\r"))
	}()
	if line, ok := lr.answer("[y/n] "); !ok || line != "n" {
		t.Fatalf("the answer read %q (%v): what was typed before the question answered it", line, ok)
	}
}
