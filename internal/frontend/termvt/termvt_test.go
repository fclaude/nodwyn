package termvt

import (
	"sync"
	"testing"

	"github.com/fclaude/nodwyn/internal/termemu"
)

// Cell text must be copied while output is excluded: the upstream emulator
// returns pointers into its mutable screen and scrollback buffers.
func TestCellTextWhileOutputChanges(t *testing.T) {
	e, err := termemu.New(8, 2, 4)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.Write([]byte("seed\r\nseed\r\n")); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	start := make(chan struct{})
	wg.Add(1)
	go func() {
		defer wg.Done()
		<-start
		for i := 0; i < 2000; i++ {
			if _, err := e.Write([]byte("abcdefgh\r\n")); err != nil {
				t.Error(err)
				return
			}
		}
	}()
	close(start)
	for i := 0; i < 2000; i++ {
		e.Cell(0, 0)
		e.HistoryCell(0, 0)
	}
	wg.Wait()
	if text, ok := e.Cell(0, 0); !ok || text != "a" {
		t.Fatalf("screen cell = %q, %v; want a, true", text, ok)
	}
	if text, ok := e.HistoryCell(0, 0); !ok || text != "a" {
		t.Fatalf("history cell = %q, %v; want a, true", text, ok)
	}
}
