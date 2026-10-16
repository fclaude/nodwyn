package memory

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/fclaude/nodwyn/internal/log"
)

// events builds a log's events in order, seq from 1, a second apart.
type events struct {
	t   *testing.T
	evs []log.Event
}

func (e *events) add(kind log.Kind, payload any) {
	e.t.Helper()
	raw, err := json.Marshal(payload)
	if err != nil {
		e.t.Fatal(err)
	}
	seq := uint64(len(e.evs) + 1)
	e.evs = append(e.evs, log.Event{Seq: seq, TS: time.Date(2026, 10, 15, 7, 0, int(seq), 0, time.UTC), Session: "s1", Kind: kind, Payload: raw})
}

func cost(usd float64) *float64 { return &usd }

// A turn's card says what was asked, how it ended and how long it took,
// what it cost, its plan with the marks made, the files it wrote, the
// commands it ran and how each ended, its errors and the start of its
// answer; a compaction's note is an episode covering the conversation since
// the one before, naming the reply that holds it; a turn nothing ended is
// closed by the next message, or by a resume, and says so. A card's head is
// the card in one line, and Open names the turn still open.
func TestFolderMakesTurnCardsAndEpisodes(t *testing.T) {
	e := &events{t: t}
	e.add(log.KindSessionStart, log.SessionStart{Repo: "/r"})
	e.add(log.KindUserMsg, log.UserMsg{Text: "Fix the flaky TestPagerOrder.\nIt fails one run in ten."})
	e.add(log.KindGatePlan, log.GatePlan{Plan: "1. Read the test — find the order\n2. Fix it — sort the keys\n3. Check — run the package"})
	e.add(log.KindLLMResp, log.LLMResp{ToolCalls: []log.ToolCall{{ID: "c1", Name: "shell-exec", Args: json.RawMessage(`{"cmd":"go","args":["test","./pager/"]}`)}}, Usage: log.Usage{Cost: cost(0.02)}})
	e.add(log.KindToolCall, log.ToolCallEvent{ToolCall: log.ToolCall{ID: "c1", Name: "shell-exec", Args: json.RawMessage(`{"cmd":"go","args":["test","./pager/"]}`)}})
	e.add(log.KindToolResult, log.ToolResult{ID: "c1", Name: "shell-exec", OK: true, Value: `(("status" . 1) ("stdout" . "FAIL TestPagerOrder") ("stderr" . ""))`})
	e.add(log.KindPlanStep, log.PlanStep{Step: 1, State: "done", Note: "it ranges over a map"})
	e.add(log.KindGuardDecision, log.GuardDecision{Prim: "fs-edit", Decision: "allow", Path: "pager/pager.go"})
	e.add(log.KindGuardDecision, log.GuardDecision{Prim: "fs-edit", Decision: "deny", Path: "secret.env"})
	e.add(log.KindToolResult, log.ToolResult{ID: "c2", Name: "fs-edit", OK: false, Error: "old text not found\nin pager/pager.go"})
	e.add(log.KindPlanStep, log.PlanStep{Step: 2, State: "done"})
	e.add(log.KindPlanStep, log.PlanStep{Step: 3, State: "skipped"})
	e.add(log.KindLLMResp, log.LLMResp{Content: "Sorted the keys in pager.go; the test passes 50 runs in a row.", Usage: log.Usage{Cost: cost(0.03)}})
	e.add(log.KindTurnEnd, log.TurnEnd{Outcome: "completed"})
	// A second turn, compacted in the middle, which nothing ends.
	e.add(log.KindUserMsg, log.UserMsg{Text: "Now look at the schema drift"})
	e.add(log.KindLLMResp, log.LLMResp{Content: "the note: pager fixed, schema next"})
	e.add(log.KindContextNarrow, log.ContextNarrow{System: "s", Lasting: true})
	e.add(log.KindContextCompact, log.ContextCompact{Trigger: "auto", Compacted: true, Before: 90000, NoteSeq: 16})
	e.add(log.KindUserMsg, log.UserMsg{Text: "and then the docs"})
	e.add(log.KindSessionResume, log.SessionStart{Repo: "/r"})

	f := Folder{Session: "s1"}
	var got []Entry
	for _, ev := range e.evs {
		got = append(got, f.Observe(ev)...)
		if from, asked := f.Open(); ev.Seq == 14 && from != 0 {
			t.Fatal("a turn is open after its turn.end")
		} else if ev.Seq == 15 && (from != 15 || asked != "Now look at the schema drift") {
			t.Fatalf("the open turn: %d %q", from, asked)
		}
	}
	if len(got) != 4 {
		t.Fatalf("%d entries: %+v", len(got), got)
	}
	card := got[0]
	if card.ID != "m:s1:2-14:1" || card.Level != LevelTurn || card.From != 2 || card.Through != 14 {
		t.Fatalf("the first card: %+v", card)
	}
	for _, want := range []string{
		"asked   Fix the flaky TestPagerOrder. It fails one run in ten.",
		"outcome completed · 12s · $0.05",
		"plan    1 ✓ Read the test — find the order  2 ✓ Fix it — sort the keys  3 ✗ Check — run the package",
		"wrote   pager/pager.go\n",
		"ran     go test ./pager/ (exit 1)",
		"errors  fs-edit: old text not found",
		"answer  Sorted the keys in pager.go; the test passes 50 runs in a row.",
	} {
		if !strings.Contains(card.Text, want) {
			t.Fatalf("the card does not say %q:\n%s", want, card.Text)
		}
	}
	if strings.Contains(card.Text, "secret.env") {
		t.Fatal("a write the guard denied is on the card")
	}
	if card.Head != `completed · 12s · $0.05 · asked "Fix the flaky TestPagerOrder. It fails one run in ten."` {
		t.Fatalf("the card's head: %q", card.Head)
	}
	episode := got[1]
	if episode.Level != LevelEpisode || episode.From != 1 || episode.Through != 18 || episode.Note != 16 || !strings.Contains(episode.Text, "the note: pager fixed, schema next") || !strings.Contains(episode.Text, "90000 tokens") {
		t.Fatalf("the compaction's episode: %+v", episode)
	}
	if f.EpisodeFrom != 19 {
		t.Fatalf("the next episode starts at %d, want 19", f.EpisodeFrom)
	}
	if unfinished := got[2]; unfinished.ID != "m:s1:15-18:1" || !strings.Contains(unfinished.Text, "outcome unfinished") {
		t.Fatalf("the turn the next message closed: %+v", unfinished)
	}
	if interrupted := got[3]; interrupted.ID != "m:s1:19-19:1" || !strings.Contains(interrupted.Text, "outcome interrupted") {
		t.Fatalf("the turn a resume closed: %+v", interrupted)
	}
}

// A compaction recorded without the seq of its note, as an older log has
// it, takes the latest reply, which is the note record-compaction would
// have named.
func TestEpisodeOfACompactionThatNamesNoNote(t *testing.T) {
	e := &events{t: t}
	e.add(log.KindUserMsg, log.UserMsg{Text: "go on"})
	e.add(log.KindLLMResp, log.LLMResp{Content: "an earlier reply"})
	e.add(log.KindLLMResp, log.LLMResp{Content: "the handover note"})
	e.add(log.KindContextCompact, log.ContextCompact{Trigger: "command", Compacted: true})
	f := Folder{Session: "s1"}
	var got []Entry
	for _, ev := range e.evs {
		got = append(got, f.Observe(ev)...)
	}
	if len(got) != 1 || !strings.Contains(got[0].Text, "the handover note") || strings.Contains(got[0].Text, "an earlier reply") || got[0].Note != 3 {
		t.Fatalf("the episode: %+v", got)
	}
}
