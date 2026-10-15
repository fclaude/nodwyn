package window

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/fclaude/nodwyn/internal/log"
)

// A window built from the events from StartTracker's Start on is the window
// built from every event (ADR 49 §10), at every point of a log, for every
// recipe: a session with compactions (a lasting narrowing and its handover),
// sub-contexts folded away, a widen, plans, attachments, folded results and
// elisions recorded by requests, and the recorded sessions in test/sessions.
// A resumed session holds only the events from there.
func TestWindowFromItsStart(t *testing.T) {
	s := newSession()
	app := func(kind log.Kind, payload any) {
		t.Helper()
		if _, err := s.w.Append(kind, payload); err != nil {
			t.Fatal(err)
		}
	}
	small := current(Policy{HighWater: 400, LowWater: 200, MaxResultTokens: 300})
	small.NamedCuts, small.Blocks = true, true
	for round := range 8 {
		s.turn(300)
		s.build(t, small)
		// A sub-context of its own: an explorer, folded when it answers.
		app(log.KindContextNarrow, log.ContextNarrow{System: "explorer", Agent: "explorer"})
		app(log.KindHarnessMsg, log.HarnessMsg{Text: "look around"})
		app(log.KindLLMResp, log.LLMResp{ToolCalls: []log.ToolCall{{ID: "e", Name: "fs-list", Args: json.RawMessage(`{}`)}}})
		app(log.KindToolResult, log.ToolResult{ID: "e", Name: "fs-list", OK: true, Value: "a\nb"})
		app(log.KindLLMResp, log.LLMResp{Content: "found a and b", Blocks: []log.ThinkingBlock{{Type: "thinking", Thinking: "listed", Signature: "sig"}}})
		app(log.KindContextFold, log.ContextFold{})
		app(log.KindGatePlan, log.GatePlan{Plan: fmt.Sprintf("1. step of round %d", round)})
		app(log.KindAttach, log.Attach{ID: "a", Name: "notes.txt", Type: "text", Bytes: 13, SHA256: "00"})
		s.turn(500)
		s.build(t, small)
		app(log.KindResultFold, log.ResultFold{Seq: uint64(s.buf.Len() % 7), Text: "folded"})
		evs := s.events(t)
		for i := len(evs) - 1; i >= 0; i-- {
			if evs[i].Kind == log.KindToolResult {
				app(log.KindResultFold, log.ResultFold{Seq: evs[i].Seq, Text: "the listing, folded"})
				break
			}
		}
		switch round % 4 {
		case 0:
			// A compaction: the note, then a lasting narrowing and the
			// handover, which the next message cuts the conversation at.
			app(log.KindHarnessMsg, log.HarnessMsg{Text: "write the handover"})
			app(log.KindLLMResp, log.LLMResp{Content: "the note"})
			app(log.KindContextNarrow, log.ContextNarrow{System: "sys", Agent: "compaction", Lasting: true})
			app(log.KindHarnessMsg, log.HarnessMsg{Text: "the conversation was cleared: the note"})
			app(log.KindContextCompact, log.ContextCompact{Compacted: true})
		case 1:
			app(log.KindContextNarrow, log.ContextNarrow{System: "sys", Agent: "compaction", Lasting: true})
			app(log.KindContextWiden, log.ContextWiden{})
		case 2:
			// A lasting narrowing, and inside it one that is not, both
			// still open: the cut is at the lasting one.
			app(log.KindContextNarrow, log.ContextNarrow{System: "outer", Lasting: true})
			app(log.KindHarnessMsg, log.HarnessMsg{Text: "kept: after the cut"})
			app(log.KindContextNarrow, log.ContextNarrow{System: "inner"})
			app(log.KindHarnessMsg, log.HarnessMsg{Text: "inside the inner one"})
		case 3:
			// A narrowing that does not last, still open: no cut.
			app(log.KindContextNarrow, log.ContextNarrow{System: "passing"})
			app(log.KindHarnessMsg, log.HarnessMsg{Text: "inside the passing one"})
		}
		s.turn(200)
		app(log.KindLLMResp, log.LLMResp{Content: "and a signed afterthought", Blocks: []log.ThinkingBlock{{Type: "thinking", Thinking: "thought", Signature: "sig"}}})
		s.build(t, small)
		// A fold, recorded now, of a result from before the cut.
		app(log.KindResultFold, log.ResultFold{Seq: 3, Text: "an old result, folded"})
	}
	logs := map[string][]log.Event{"synthetic": s.events(t)}
	fixtures, err := filepath.Glob("../../test/sessions/*.jsonl")
	if err != nil || len(fixtures) == 0 {
		t.Fatalf("no recorded sessions: %v", err)
	}
	for _, f := range fixtures {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		evs, err := log.Read(strings.NewReader(string(raw)))
		if err != nil {
			t.Fatal(err)
		}
		logs[filepath.Base(f)] = evs
	}
	cut := false
	for name, evs := range logs {
		var tr StartTracker
		for i := range evs {
			tr.Observe(evs[i])
			start := tr.Start()
			cut = cut || start > 1
			for _, p := range []Policy{pol, current(pol), small} {
				whole := Build(evs[:i+1], "sys", 10, p)
				from := Build(evs[start-1:i+1], "sys", 10, p)
				if !reflect.DeepEqual(whole, from) {
					t.Fatalf("%s, through seq %d, start %d, %+v: the window from its start differs\n%+v\n%+v", name, evs[i].Seq, start, p, from, whole)
				}
			}
		}
	}
	if !cut {
		t.Fatal("no window started after the first event: the comparison shows nothing")
	}
}
