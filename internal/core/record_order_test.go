package core

import (
	"sync"
	"testing"

	"github.com/fclaude/nodwyn/internal/log"
)

// Records made at the same time (a turn's, and the changes of a setting made
// during it) reach the log, the events the window is built from and the
// stream a front-end reads in one order (P2-037): a subscriber takes its
// watermark from the last event it is given, and with two orders it lost the
// event before it. The delivered Seq must never go back.
func TestConcurrentRecordsKeepOneOrder(t *testing.T) {
	t.Parallel()
	r := newRig(t, true)
	var delivered []uint64
	done := make(chan struct{})
	go func() {
		defer close(done)
		for ev := range r.s.Events() {
			if ev.Kind == string(log.KindNote) {
				delivered = append(delivered, ev.Seq)
			}
		}
	}()
	const writers, each = 6, 300
	var wg sync.WaitGroup
	for range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range each {
				if _, err := r.s.Record(log.KindNote, log.Note{Text: "n"}); err != nil {
					t.Error(err)
					return
				}
			}
		}()
	}
	wg.Wait()
	// The stream ends when the session closes.
	if err := r.s.Close(); err != nil {
		t.Fatal(err)
	}
	<-done
	if len(delivered) != writers*each {
		t.Fatalf("%d notes delivered, want %d", len(delivered), writers*each)
	}
	for i := 1; i < len(delivered); i++ {
		if delivered[i] <= delivered[i-1] {
			t.Fatalf("delivery went from seq %d to %d", delivered[i-1], delivered[i])
		}
	}
	events := r.s.EventsSoFar()
	for i := 1; i < len(events); i++ {
		if events[i].Seq <= events[i-1].Seq {
			t.Fatalf("the window's events went from seq %d to %d", events[i-1].Seq, events[i].Seq)
		}
	}
}
