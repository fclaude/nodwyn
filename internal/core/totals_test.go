package core

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fclaude/nodwyn/internal/log"
)

// Fixed times make the accounting independent of how fast the fake runs.
func TestIndexedTotalsSurviveGrowthAndRebuild(t *testing.T) {
	t.Parallel()
	st := Store{Dir: t.TempDir()}
	id := "totals"
	f, err := os.Create(log.SessionPath(st.Dir, id))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var seq uint64
	at := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	add := func(second int, kind log.Kind, p any) {
		t.Helper()
		seq++
		raw, err := json.Marshal(p)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.NewEncoder(f).Encode(log.Event{Seq: seq, TS: at.Add(time.Duration(second) * time.Second), Session: id, Kind: kind, Payload: raw}); err != nil {
			t.Fatal(err)
		}
	}
	read := func() SessionTotals {
		t.Helper()
		r, err := st.Totals(id)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	add(0, log.KindSessionStart, log.SessionStart{Repo: t.TempDir()})
	if read().Started {
		t.Fatal("an empty session has started")
	}
	add(1, log.KindRunState, log.RunState{Status: "running"})
	add(2, log.KindRunTurn, log.RunTurn{Number: 1})
	add(3, log.KindUserMsg, log.UserMsg{Text: "work"})
	add(4, log.KindLLMReq, log.LLMReq{})
	price, cached := 0.25, 80
	add(6, log.KindLLMResp, log.LLMResp{Usage: log.Usage{PromptTokens: 100, OutputTokens: 20, CachedTokens: &cached, Cost: &price, CostEstimated: true}})
	add(7, log.KindToolCall, log.ToolCallEvent{ToolCall: log.ToolCall{ID: "outer"}})
	add(8, log.KindToolCall, log.ToolCallEvent{ToolCall: log.ToolCall{ID: "inner"}})
	add(9, log.KindNote, log.Note{Text: "before the wait"})
	add(19, log.KindGuardDecision, log.GuardDecision{Path: "a.txt", Decision: "confirm", Answer: "yes", Detail: "+ sk-ant-abcdefgh1234"})
	add(20, log.KindToolResult, log.ToolResult{ID: "inner"})
	half := read()
	if half.Clock.Tools != 3000 || half.Clock.OpenTool != at.Add(20*time.Second).UnixMilli() {
		t.Fatalf("open nested tools counted twice or kept a wait: %+v", half.Clock)
	}
	add(21, log.KindToolResult, log.ToolResult{ID: "outer"})
	add(21, log.KindAttach, log.Attach{Name: "notes.txt", Text: "the content is not part of totals"})
	add(22, log.KindJevReq, log.JevReq{Usage: &log.JevUsage{InputTokens: 5, OutputTokens: 2, Cost: &price}})
	add(23, log.KindLLMResp, log.LLMResp{})
	add(24, log.KindTurnEnd, log.TurnEnd{Outcome: "completed"})
	got := read()
	if got.Through != seq || got.Cost != 0.5 || !got.Estimated || got.Unpriced != 1 || got.Cache.Prompt != 100 || got.Cache.Cached != 80 || got.Speed.MS != 2000 || got.Speed.Out != 20 || got.Judge.Tokens != 7 {
		t.Fatalf("accounting: %+v", got)
	}
	if got.Clock.Total != 22000 || got.Clock.Work != 12000 || got.Clock.Model != 2000 || got.Clock.Tools != 4000 || got.Clock.Open != 0 || got.Clock.OpenModel != 0 || got.Clock.OpenTool != 0 {
		t.Fatalf("clocks: %+v", got.Clock)
	}
	if got.Turns != 1 || got.Runs != 1 || got.LastTurn.Seq != 3 || got.LastRun.Seq != 2 {
		t.Fatalf("boundaries: %+v", got)
	}
	if len(got.Attachments) != 1 || got.Attachments[0].Text != "" {
		t.Fatalf("attachments copied content: %+v", got.Attachments)
	}
	if len(got.Changes) != 1 || strings.Contains(got.Changes[0].Detail, "sk-ant-") {
		t.Fatalf("legacy approval not redacted: %+v", got.Changes)
	}
	before := asJSON(t, got)
	for _, invalid := range []string{"deleted", "old version", "invalid JSON"} {
		switch invalid {
		case "deleted":
			err = os.Remove(indexPath(st.Dir, id))
		case "old version":
			raw, e := os.ReadFile(indexPath(st.Dir, id))
			if e != nil {
				t.Fatal(e)
			}
			var idx logIndex
			if e = json.Unmarshal(raw, &idx); e != nil {
				t.Fatal(e)
			}
			idx.Version = 2
			raw, e = json.Marshal(idx)
			if e != nil {
				t.Fatal(e)
			}
			err = os.WriteFile(indexPath(st.Dir, id), raw, 0600)
		default:
			err = os.WriteFile(indexPath(st.Dir, id), []byte("broken"), 0600)
		}
		if err != nil {
			t.Fatal(err)
		}
		if got := asJSON(t, read()); got != before {
			t.Fatalf("%s changed totals:\n%s\n%s", invalid, before, got)
		}
	}
	add(30, log.KindSessionResume, log.SessionStart{})
	add(100, log.KindUserMsg, log.UserMsg{Text: "again"})
	add(101, log.KindLLMReq, log.LLMReq{})
	// A resume after a crash must not count the downtime or an abandoned pair.
	add(200, log.KindSessionResume, log.SessionStart{})
	if c := read().Clock; c.Total != 23000 || c.Model != 2000 || c.Open != 0 || c.OpenModel != 0 {
		t.Fatalf("crash: %+v", c)
	}
	if _, err = f.WriteString(`{"seq":`); err != nil {
		t.Fatal(err)
	}
	if !read().Truncated {
		t.Fatal("a torn tail was hidden")
	}
}

func TestTotalsAndBoundaryPagesReadOnlyTheSuffix(t *testing.T) {
	t.Parallel()
	st := Store{Dir: filepath.Join(t.TempDir(), "sessions")}
	id := "totals-suffix"
	l := newSyntheticLog(t, st.Dir, id)
	l.turns(300)
	before, err := st.Totals(id)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(indexPath(st.Dir, id))
	if err != nil {
		t.Fatal(err)
	}
	var idx logIndex
	if err = json.Unmarshal(raw, &idx); err != nil {
		t.Fatal(err)
	}
	// Leave the last turn and the index's end intact, but break the sparse
	// mark before it: the exact turn boundary must be what reads this page.
	boundary := idx.Turns[len(idx.Turns)-1]
	f, err := os.OpenFile(log.SessionPath(st.Dir, id), os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err = f.WriteAt([]byte(strings.Repeat("x", int(boundary.Offset))), 0); err != nil {
		t.Fatal(err)
	}
	after, err := st.Totals(id)
	if err != nil {
		t.Fatal(err)
	}
	if asJSON(t, after) != asJSON(t, before) {
		t.Fatal("totals reread the damaged prefix")
	}
	page, err := st.ReadLog(id, boundary.Seq-1, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if page.Events[0].Seq != boundary.Seq {
		t.Fatalf("page: %+v", page)
	}
	l.add(log.KindUserMsg, log.UserMsg{Text: "next"})
	grown, err := st.Totals(id)
	if err != nil {
		t.Fatal(err)
	}
	if grown.Turns != before.Turns+1 || grown.Through != before.Through+1 {
		t.Fatalf("growth: %+v", grown)
	}
}

// Every prefix is serialised as an index would be: pending requests and the
// gap between driver turns must retain their meaning after the next read.
func TestIndexedTotalsKeepUsageAndRunClocks(t *testing.T) {
	t.Parallel()
	var idx logIndex
	at := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	var seq uint64
	add := func(second int, k log.Kind, p any) {
		t.Helper()
		seq++
		raw, err := json.Marshal(p)
		if err != nil {
			t.Fatal(err)
		}
		idx.observeTotals(log.Event{Seq: seq, TS: at.Add(time.Duration(second) * time.Second), Kind: k, Payload: raw}, 0)
		raw, err = json.Marshal(idx)
		if err != nil {
			t.Fatal(err)
		}
		if err = json.Unmarshal(raw, &idx); err != nil {
			t.Fatal(err)
		}
	}
	zero, full, cost := 0, 50, 0.1
	free := 0.0
	add(1, log.KindUserMsg, log.UserMsg{Text: "one"})
	add(2, log.KindLLMReq, log.LLMReq{})
	add(3, log.KindLLMResp, log.LLMResp{Usage: log.Usage{PromptTokens: 100, OutputTokens: 40, CachedTokens: &full, Cost: &free, Unreported: true, WallMS: 500}})
	add(4, log.KindLLMReq, log.LLMReq{})
	add(6, log.KindLLMResp, log.LLMResp{Usage: log.Usage{PromptTokens: 100, OutputTokens: 20, CachedTokens: &zero, WallMS: 1000}})
	add(7, log.KindLLMResp, log.LLMResp{Usage: log.Usage{PromptTokens: 100, OutputTokens: 20, WallMS: 1000}})
	add(8, log.KindLLMReq, log.LLMReq{})
	add(10, log.KindLLMResp, log.LLMResp{Usage: log.Usage{PromptTokens: 50, OutputTokens: 10, CachedTokens: &full, Cost: &cost}})
	add(11, log.KindJevReq, log.JevReq{})
	add(12, log.KindJevReq, log.JevReq{Usage: &log.JevUsage{InputTokens: 5}})
	add(13, log.KindGuardDecision, log.GuardDecision{Decision: "confirm", Actor: "judge"})
	add(14, log.KindGuardDecision, log.GuardDecision{Decision: "confirm", Actor: "unattended"})
	add(15, log.KindTurnEnd, log.TurnEnd{})
	add(17, log.KindRunTurn, log.RunTurn{Number: 2})
	add(18, log.KindUserMsg, log.UserMsg{Text: "two"})
	add(19, log.KindTurnEnd, log.TurnEnd{})
	add(21, log.KindGuardDecision, log.GuardDecision{Decision: "confirm", Actor: "person"})
	add(25, log.KindRunTurn, log.RunTurn{Number: 3})
	add(26, log.KindUserMsg, log.UserMsg{Text: "three"})
	add(27, log.KindTurnEnd, log.TurnEnd{})
	got := idx.Totals
	if got.Clock.Total != 20000 || got.Clock.Work != 20000 {
		t.Fatalf("driver gaps or automatic answers: %+v", got.Clock)
	}
	if got.Cost != cost || got.Unpriced != 3 || got.Estimated || got.Cache.Prompt != 150 || got.Cache.Cached != 50 || got.Speed.MS != 4000 || got.Speed.Out != 50 || got.Speed.Replies != 3 || got.Judge.Calls != 2 || got.Judge.Unpriced != 1 {
		t.Fatalf("usage: %+v", got)
	}
	if len(idx.Turns) != 3 {
		t.Fatalf("driver turns counted their user message twice: %+v", idx.Turns)
	}
}

func TestBoundaryMarksRebuildWhenTheSeekIsWrong(t *testing.T) {
	t.Parallel()
	st := Store{Dir: t.TempDir()}
	id := "totals-stale-mark"
	l := newSyntheticLog(t, st.Dir, id)
	l.turns(5)
	for _, bad := range []int64{-1, 1, 1 << 30} {
		if _, err := st.Totals(id); err != nil {
			t.Fatal(err)
		}
		raw, err := os.ReadFile(indexPath(st.Dir, id))
		if err != nil {
			t.Fatal(err)
		}
		var idx logIndex
		if err = json.Unmarshal(raw, &idx); err != nil {
			t.Fatal(err)
		}
		k := len(idx.Turns) - 1
		seq := idx.Turns[k].Seq
		idx.Turns[k].Offset = bad
		raw, err = json.Marshal(idx)
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(indexPath(st.Dir, id), raw, 0600); err != nil {
			t.Fatal(err)
		}
		page, err := st.ReadLog(id, seq-1, 0, 100)
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Events) == 0 || page.Events[0].Seq != seq || page.More {
			t.Fatalf("offset %d: %+v", bad, page)
		}
	}
}
