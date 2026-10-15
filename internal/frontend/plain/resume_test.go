package plain

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fclaude/nodwyn/internal/config"
	"github.com/fclaude/nodwyn/internal/core"
	"github.com/fclaude/nodwyn/internal/llm"
	"github.com/fclaude/nodwyn/internal/log"
)

// `nodwyn --resume ID` replays the whole conversation, read from the log,
// though the resumed session holds only its end (ADR 49 §10).
func TestResumeReplaysTheWholeConversation(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	if err := os.Mkdir(repo, 0o700); err != nil {
		t.Fatal(err)
	}
	o := core.Options{Paths: config.PathsUnder(filepath.Join(root, "config"), filepath.Join(root, "data")), Config: config.Default(), NoConfirm: true, Provider: llm.NewOllama("http://127.0.0.1:1")}
	id := "20260901T070000Z-0000ffff"
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
	write(log.KindSessionStart, log.SessionStart{Repo: repo})
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
	s, err := core.Resume(o, id)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if first := s.EventsSoFar()[0].Seq; first <= 1 {
		t.Fatalf("the session holds its log from seq %d: this shows nothing", first)
	}
	var out bytes.Buffer
	c := &Chat{Session: s, In: strings.NewReader(""), Out: &out, ReplayHistory: true}
	if err := c.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"run 0 of the schedule", "run 299 of the schedule"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("the replay does not show %q", want)
		}
	}
}
