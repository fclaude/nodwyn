package core

import (
	"fmt"
	"runtime"
	"testing"

	"github.com/fclaude/nodwyn/internal/log"
)

// A search shows up to 20 hits, often of one task, and each was read through
// a fresh load of the task's index (a read and a parse of its whole .idx).
// Reading them through one reader loads it once, and the events are the same.
func TestEventReaderLoadsATasksIndexOnce(t *testing.T) {
	o := diskOptions(t)
	last := taskLog(t, o, "20261001T090000Z-aaaa", "/repo", "needle", func(write func(log.Kind, any)) {
		for i := 0; i < 600; i++ {
			write(log.KindUserMsg, log.UserMsg{Text: fmt.Sprintf("message %d about nothing in particular", i)})
		}
	})
	store := Store{Dir: o.Paths.SessionsDir}
	id := "20261001T090000Z-aaaa"
	seqs := []uint64{last, last + 1, last + 100, last + 300, last + 599}

	measure := func(read func(seq uint64) (log.Event, error)) (events []log.Event, bytes uint64) {
		var before, after runtime.MemStats
		runtime.GC()
		runtime.ReadMemStats(&before)
		for round := 0; round < 4; round++ {
			for _, seq := range seqs {
				ev, err := read(seq)
				if err != nil {
					t.Fatal(err)
				}
				if round == 0 {
					events = append(events, ev)
				}
			}
		}
		runtime.ReadMemStats(&after)
		return events, after.TotalAlloc - before.TotalAlloc
	}
	// Each read on its own reader: an index load apiece, as eventAt did.
	alone, aloneBytes := measure(func(seq uint64) (log.Event, error) { return store.eventAt(id, seq) })
	reader := store.eventReader()
	shared, sharedBytes := measure(func(seq uint64) (log.Event, error) { return reader.at(id, seq) })
	for i := range alone {
		if alone[i].Seq != shared[i].Seq || string(alone[i].Payload) != string(shared[i].Payload) {
			t.Fatalf("event %d differs: %+v and %+v", i, alone[i], shared[i])
		}
	}
	if sharedBytes*3 > aloneBytes {
		t.Fatalf("20 reads on one reader allocated %d bytes, on a reader apiece %d: the index is loaded each time", sharedBytes, aloneBytes)
	}
}
