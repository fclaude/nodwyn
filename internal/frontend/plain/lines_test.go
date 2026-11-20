package plain

import (
	"io"
	"strings"
	"sync"
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

type promptReadyWriter struct {
	ready chan struct{}
	once  sync.Once
}

func (w *promptReadyWriter) Write(p []byte) (int, error) {
	w.once.Do(func() { close(w.ready) })
	return len(p), nil
}

func TestLongTerminalMessageIsComplete(t *testing.T) {
	message := strings.Repeat("é", 4996) + "END!"
	for _, tc := range []struct{ name, keys, want string }{
		{"typed", message + "X\x7f\r", message},
		{"bracketed", "\x1b[200~" + message + "\x1b[201~\r", message},
		{"multiline", "prefix \x1b[200~first\r" + message + "\x1b[201~\r", "prefix first\n" + message},
	} {
		t.Run(tc.name, func(t *testing.T) {
			master, terminal, err := pty.Open()
			if err != nil {
				t.Fatal(err)
			}
			defer master.Close()
			defer terminal.Close()
			output := &promptReadyWriter{ready: make(chan struct{})}
			reader := newLineReader(terminal, output)
			read := make(chan string, 1)
			go func() {
				line, _ := reader.read("you> ")
				read <- line
			}()
			select {
			case <-output.ready:
			case <-time.After(2 * time.Second):
				t.Fatal("prompt not ready")
			}
			if _, err := io.WriteString(master, tc.keys); err != nil {
				t.Fatal(err)
			}
			select {
			case got := <-read:
				if got != tc.want {
					t.Fatalf("entered %d bytes, received %d, final marker present=%v", len(tc.want), len(got), strings.HasSuffix(got, "END!"))
				}
			case <-time.After(2 * time.Second):
				t.Fatal("line not received")
			}
		})
	}
}
