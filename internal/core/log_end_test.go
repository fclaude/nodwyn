package core

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fclaude/nodwyn/internal/log"
)

func asJSON(t *testing.T, v any) string {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// forget drops the in-memory listing of the log at path, so the next Info
// reads the log (through its index) instead of the cache.
func forget(path string) {
	infoCache.mu.Lock()
	delete(infoCache.entries, path)
	infoCache.mu.Unlock()
}

// Once a log is indexed, listing it and reading a page near its end read
// the log only from there: with the first half of a 50,000-event log made
// unreadable, both still answer, and only a page from the start fails, as
// a whole read does. Events the log gains are folded in from the end.
func TestLogIndexReadsALongLogFromItsEnd(t *testing.T) {
	t.Parallel()
	st := Store{Dir: filepath.Join(t.TempDir(), "sessions")}
	if err := os.MkdirAll(st.Dir, 0o700); err != nil {
		t.Fatal(err)
	}
	id := "20261015T120000Z-0000bbbb"
	path := log.SessionPath(st.Dir, id)
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	w := bufio.NewWriter(f)
	ts := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	var seq uint64
	write := func(kind log.Kind, payload any) {
		t.Helper()
		raw, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		seq++
		line, err := json.Marshal(log.Event{Seq: seq, TS: ts.Add(time.Duration(seq) * time.Second), Session: id, Kind: kind, Payload: raw})
		if err != nil {
			t.Fatal(err)
		}
		w.Write(append(line, '\n'))
	}
	write(log.KindSessionStart, log.SessionStart{Repo: t.TempDir()})
	for seq < 50_000 {
		write(log.KindUserMsg, log.UserMsg{Text: fmt.Sprintf("run %d", seq)})
		write(log.KindToolResult, log.ToolResult{ID: "c", Name: "fs-read", OK: true, Value: strings.Repeat("output ", 20)})
		write(log.KindLLMResp, log.LLMResp{Content: "done"})
		write(log.KindTurnEnd, log.TurnEnd{Outcome: "completed"})
	}
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
	before, err := st.Info(id)
	if err != nil {
		t.Fatal(err)
	}

	// The first half of the log becomes unreadable, its size unchanged.
	stat, err := f.Stat()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteAt([]byte(strings.Repeat("x", int(stat.Size()/2))), 0); err != nil {
		t.Fatal(err)
	}
	forget(path)
	after, err := st.Info(id)
	if err != nil {
		t.Fatalf("listing read the start of the log: %v", err)
	}
	if asJSON(t, after) != asJSON(t, before) {
		t.Fatalf("the listing changed:\n%s\n%s", asJSON(t, after), asJSON(t, before))
	}
	page, err := st.ReadLog(id, seq-100, 0, 100)
	if err != nil {
		t.Fatalf("a page near the end read the start of the log: %v", err)
	}
	if len(page.Events) != 100 || page.Events[0].Seq != seq-99 || page.Next != seq || page.More {
		t.Fatalf("the last page: %d events from %d, next %d, more %v", len(page.Events), page.Events[0].Seq, page.Next, page.More)
	}

	ts = ts.Add(time.Hour)
	write(log.KindUserMsg, log.UserMsg{Text: "one more"})
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
	f.Close()
	grown, err := st.Info(id)
	if err != nil {
		t.Fatalf("a log grown since its index: %v", err)
	}
	if !grown.InFlight || !grown.LastEvent.Equal(ts.Add(time.Duration(seq)*time.Second)) {
		t.Fatalf("the event the log gained was not folded in: %+v", grown)
	}
	// Last, since a page that cannot be read where its mark says drops the
	// index: the start of the log is unreadable, or the checks above show
	// nothing.
	if _, err := st.ReadLog(id, 0, 0, 10); err == nil {
		t.Fatal("the start of the log is readable")
	}
}
