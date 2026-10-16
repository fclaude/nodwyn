package prim

import (
	"bytes"
	"context"
	"errors"
	"runtime"
	"strings"
	"testing"
	"time"
)

func newBufferedProc() *proc { return &proc{changed: make(chan struct{})} }

// The buffer keeps the latest procBufferSize bytes and a read reports what it
// dropped, however the writes are sized.
func TestProcBufferKeepsTheLatestMiB(t *testing.T) {
	p := newBufferedProc()
	chunk := bytes.Repeat([]byte("abcdefghij"), 1000) // 10,000 bytes, not a divisor of the buffer
	var all []byte
	for i := 0; i < 300; i++ {
		all = append(all, chunk...)
		p.Write(chunk)
	}
	p.Write([]byte("end"))
	all = append(all, "end"...)
	p.mu.Lock()
	text, dropped, left := p.take(0)
	p.mu.Unlock()
	if want := string(all[len(all)-procBufferSize:]); text != want {
		t.Fatalf("the buffer is not the latest %d bytes (got %d)", procBufferSize, len(text))
	}
	if dropped != int64(len(all)-procBufferSize) || left != 0 {
		t.Fatalf("dropped %d, left %d; want %d, 0", dropped, left, len(all)-procBufferSize)
	}
}

// A watcher printing a line at a time into a full buffer used to copy the
// whole MiB on every write (4.6 s for 10,000 16-byte writes): 1,000 writes
// allocated a GiB, and now allocate a few KiB.
func TestProcWriteIntoAFullBufferDoesNotCopyIt(t *testing.T) {
	p := newBufferedProc()
	p.Write(make([]byte, procBufferSize))
	line := []byte("a log line 16 B\n")
	p.Write(line) // the first write past the size may grow the slice
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	for i := 0; i < 1000; i++ {
		p.Write(line)
	}
	runtime.ReadMemStats(&after)
	if got := after.TotalAlloc - before.TotalAlloc; got > 1<<20 {
		t.Fatalf("1,000 small writes into a full buffer allocated %d bytes", got)
	}
}

func BenchmarkProcWriteIntoAFullBuffer(b *testing.B) {
	p := newBufferedProc()
	p.Write(make([]byte, procBufferSize))
	line := []byte("a log line 16 B\n")
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		p.Write(line)
	}
}

// shell-send to a program that does not read its input used to block in the
// pipe write for as long as the program lived, whatever its context did, so
// a turn could not be aborted. It now gives up when the context ends.
func TestShellSendStopsWaitingWhenAborted(t *testing.T) {
	in, _ := procInterp(t)
	evalStr(t, in, `(shell-start "sleep" (list "30"))`)
	text := strings.Repeat("x", procMaxText)
	var err error
	finished := make(chan struct{})
	start := time.Now()
	go func() {
		defer close(finished)
		// More than the pipe holds (64 KiB on Linux), in the sizes one call takes.
		for i := 0; i < 10 && err == nil; i++ {
			ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
			_, err = in.EvalString(ctx, `(shell-send "p1" "`+text+`")`)
			cancel()
		}
	}()
	select {
	case <-finished:
	case <-time.After(20 * time.Second):
		t.Fatal("shell-send is still blocked in the write 20 s on")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("got %v after %s, want the context's error", err, time.Since(start))
	}
}

func TestShellSendRefusesMoreThanOneCallTakes(t *testing.T) {
	in, _ := procInterp(t)
	evalStr(t, in, `(shell-start "sleep" (list "30"))`)
	_, err := in.EvalString(context.Background(), `(shell-send "p1" "`+strings.Repeat("x", procMaxText+1)+`")`)
	if err == nil || !strings.Contains(err.Error(), "shell-send sends at most") {
		t.Fatalf("got %v", err)
	}
}
