package plain

import (
	"bytes"
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

type promptSignals struct {
	message  chan struct{}
	question chan struct{}
}

func (w promptSignals) Write(p []byte) (int, error) {
	for prompt, ready := range map[string]chan struct{}{"message> ": w.message, "choice> ": w.question} {
		if bytes.Contains(p, []byte(prompt)) {
			select {
			case ready <- struct{}{}:
			default:
			}
		}
	}
	return len(p), nil
}

func TestQuestionAcceptsOnlyInputTypedAfterItsPrompt(t *testing.T) {
	master, slave, err := pty.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer master.Close()
	defer slave.Close()
	ready := promptSignals{make(chan struct{}, 1), make(chan struct{}, 1)}
	reader := newLineReader(slave, ready)
	write := make(chan error, 1)
	go func() {
		<-ready.message
		if _, err := io.WriteString(master, "hello\rfirst\r"); err != nil {
			write <- err
			return
		}
		<-ready.question
		_, err := io.WriteString(master, "second\r")
		write <- err
	}()
	if got, ok := reader.read("message> "); !ok || got != "hello" {
		t.Fatalf("message = %q, read = %v", got, ok)
	}
	if got, ok := reader.answer("choice> "); !ok || got != "second" {
		t.Errorf("choice = %q, read = %v, want second", got, ok)
	}
	select {
	case err := <-write:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("input writer did not finish")
	}
}

// recorder keeps what the editor draws, and says when it first drew.
type recorder struct {
	mu    sync.Mutex
	b     bytes.Buffer
	ready chan struct{}
	once  sync.Once
}

func (r *recorder) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.b.Write(p)
	r.once.Do(func() { close(r.ready) })
	return len(p), nil
}

func (r *recorder) String() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.b.String()
}

// Line mode writes through TerminalText, and gave the editor that writer
// too: the escape sequence that turns bracketed paste on reached the
// terminal as the text "[?2004h", cursor moves as "[D", and a pasted
// message arrived as several. The editor draws to the terminal itself;
// only its prompt, which can hold a repository's text, is cleaned.
func TestTheLineEditorDrawsPastLineModesFilter(t *testing.T) {
	master, slave, err := pty.Open()
	if err != nil {
		t.Skip("no pseudo-terminal:", err)
	}
	defer master.Close()
	defer slave.Close()
	out := &recorder{ready: make(chan struct{})}
	reader := newLineReader(slave, TerminalText(out))
	read := make(chan string, 1)
	go func() {
		line, _ := reader.read("\x1b]0;title\x07you> ")
		read <- line
	}()
	select {
	case <-out.ready:
	case <-time.After(2 * time.Second):
		t.Fatal("prompt not drawn")
	}
	if _, err := io.WriteString(master, "ab\x1b[D\x1b[200~one\rtwo\x1b[201~\r"); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-read:
		if got != "aoneb\ntwo" {
			t.Fatalf("read %q", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("line not received")
	}
	drawn := out.String()
	if !strings.Contains(drawn, "\x1b[?2004h") || !strings.Contains(drawn, "\r") {
		t.Fatalf("the editor's sequences were filtered out: %q", drawn)
	}
	if strings.Contains(drawn, "\x1b]0;") {
		t.Fatalf("the prompt's escape sequence was drawn: %q", drawn)
	}
}
