package plain

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/fclaude/nodwyn/internal/frontend/rpc"
	"github.com/fclaude/nodwyn/internal/log"
	"github.com/fclaude/nodwyn/test/fakes/must"
)

// resumedSession is a long stored conversation, resumed in a daemon: the
// session holds only its end (ADR 49 §10). It returns line mode over it.
func resumedSession(t *testing.T) *Chat {
	t.Helper()
	o := testOptions(t, "http://127.0.0.1:1")
	id := "20260901T070000Z-0000ffff"
	must.MkdirAll(t, o.Paths.SessionsDir, 0o700)
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
		write(log.KindUserMsg, log.UserMsg{Text: fmt.Sprintf("run %d of the schedule", i)})
		write(log.KindLLMResp, log.LLMResp{Content: fmt.Sprintf("run %d done", i)})
		write(log.KindTurnEnd, log.TurnEnd{Outcome: "completed"})
	}
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
	f.Close()
	client, _ := testDaemon(t, o)
	var info rpc.SessionDetails
	if err := client.Call(context.Background(), "session.resume", map[string]any{"id": id}, &info); err != nil {
		t.Fatal(err)
	}
	return &Chat{Client: client, ID: id, ctx: context.Background()}
}

// `nodwyn --resume ID` replays the whole conversation, read from the log,
// though the resumed session holds only its end (ADR 49 §10).
func TestResumeReplaysTheWholeConversation(t *testing.T) {
	c := resumedSession(t)
	c.In, c.ReplayHistory = strings.NewReader(""), true
	got := runChat(t, c)
	for _, want := range []string{"run 0 of the schedule", "run 299 of the schedule"} {
		if !strings.Contains(got, want) {
			t.Fatalf("the replay does not show %q", want)
		}
	}
}

// :log reads the durable log, so a resumed session shows the runs before
// this one, as the TUI's :log does; it showed only what this process had
// seen.
func TestLogCommandReadsTheWholeLogOfAResumedSession(t *testing.T) {
	var out bytes.Buffer
	c := resumedSession(t)
	c.Out = &out
	c.command(":log 1000")
	for _, want := range []string{"run 0 of the schedule", "run 299 done"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf(":log does not show %q:\n%s", want, out.String())
		}
	}
	out.Reset()
	c.command(":log 1001")
	if !strings.Contains(out.String(), "usage: :log") {
		t.Fatalf("a count past the most: %q", out.String())
	}
}
