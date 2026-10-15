package tui

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/fclaude/nodwyn/internal/log"
)

// A task resumed in the TUI's own process shows its whole conversation,
// read from its log, though the session holds only the end of it (ADR 49
// §10): with the snapshot taken from the session's events, only the runs
// since its last compaction were shown.
func TestLocalResumeShowsTheWholeConversation(t *testing.T) {
	o := testOptions(t)
	id := "20260901T070000Z-0000eeee"
	if err := os.MkdirAll(o.Paths.SessionsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(log.SessionPath(o.Paths.SessionsDir, id))
	if err != nil {
		t.Fatal(err)
	}
	w := bufio.NewWriter(f)
	var seq uint64
	write := func(kind log.Kind, payload any) {
		raw, _ := json.Marshal(payload)
		seq++
		line, _ := json.Marshal(log.Event{Seq: seq, TS: time.Date(2026, 9, 1, 7, 0, int(seq), 0, time.UTC), Session: id, Kind: kind, Payload: raw})
		w.Write(append(line, '\n'))
	}
	write(log.KindSessionStart, log.SessionStart{Repo: o.Repo})
	for i := range 300 {
		if i == 290 {
			write(log.KindContextNarrow, log.ContextNarrow{System: "sys", Agent: "compaction", Lasting: true})
			write(log.KindHarnessMsg, log.HarnessMsg{Text: "the handover"})
		}
		write(log.KindUserMsg, log.UserMsg{Text: fmt.Sprintf("run %d", i)})
		write(log.KindLLMResp, log.LLMResp{Content: fmt.Sprintf("run %d done", i)})
		write(log.KindTurnEnd, log.TurnEnd{Outcome: "completed"})
	}
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
	f.Close()

	b := newLocal(o)
	defer b.Close()
	snap, err := b.Attach(context.Background(), o.Repo, id)
	if err != nil {
		t.Fatal(err)
	}
	if first := b.session.EventsSoFar()[0].Seq; first <= 1 {
		t.Fatalf("the session holds its log from seq %d: this shows nothing", first)
	}
	if len(snap.History) <= int(seq) || snap.History[0].Seq != 1 {
		t.Fatalf("the conversation shown has %d events from seq %d; the log had %d before the resume", len(snap.History), snap.History[0].Seq, seq)
	}
}
