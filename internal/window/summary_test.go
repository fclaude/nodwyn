package window

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/fclaude/nodwyn/internal/llm"
	"github.com/fclaude/nodwyn/internal/log"
)

func summaryFixture(t *testing.T) (*session, uint64, uint64) {
	t.Helper()
	s := newSession()
	s.turn(40) // seq 1..4: first complete conversation
	s.turn(40)
	s.turn(40)
	return s, 1, 4
}
func addTestSummary(t *testing.T, s *session, from, through uint64, text string) log.Event {
	t.Helper()
	hash, err := SummarySourceHash(s.events(t), from, through)
	if err != nil {
		t.Fatal(err)
	}
	ev, err := s.w.Append(log.KindSummary, log.Summary{From: from, Through: through, SourceHash: hash, Text: text})
	if err != nil {
		t.Fatal(err)
	}
	return ev
}
func messagesJSON(t *testing.T, events []log.Event) string {
	t.Helper()
	raw, err := json.Marshal(Build(events, "system", 0, Policy{HighWater: 100000, LowWater: 50000, MaxResultTokens: 10000}).Messages)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}
func TestSummaryReplacementRevocationAndRecentTurns(t *testing.T) {
	s, from, through := summaryFixture(t)
	before := messagesJSON(t, s.events(t))
	ev := addTestSummary(t, s, from, through, "Earlier work established the file format.")
	after := messagesJSON(t, s.events(t))
	if strings.Contains(after, "question 0") || !strings.Contains(after, "Earlier work") || !strings.Contains(after, "question 1") || !strings.Contains(after, "question 2") {
		t.Fatal(after)
	}
	infos := Summaries(s.events(t))
	if len(infos) != 1 || infos[0].Status != "active" {
		t.Fatal(infos)
	}
	recent := addTestSummary(t, s, 5, 8, "Should not replace a recent turn.")
	if got := messagesJSON(t, s.events(t)); strings.Contains(got, "Should not replace") || !strings.Contains(got, "question 1") {
		t.Fatal(got)
	}
	s.w.Append(log.KindSummaryDrop, log.SummaryDrop{SummarySeq: ev.Seq})
	if got := messagesJSON(t, s.events(t)); got != before {
		t.Fatal("revocation did not rebuild the original window", got)
	}
	infos = Summaries(s.events(t))
	if infos[0].Status != "dropped" || infos[1].Seq != recent.Seq || infos[1].Status != "recent" {
		t.Fatal(infos)
	}
}
func TestSummaryStalenessAndSupersessionNeverLoseSource(t *testing.T) {
	s, from, through := summaryFixture(t)
	addTestSummary(t, s, from, through, "old summary")
	latest := addTestSummary(t, s, from, through, "new summary")
	current := messagesJSON(t, s.events(t))
	if strings.Contains(current, "old summary") || !strings.Contains(current, "new summary") {
		t.Fatal(current)
	}
	events := s.events(t)
	// Even a stale latest cache must not revive its superseded predecessor.
	bad := latest
	bad.Payload = json.RawMessage(`{"from":1,"through":4,"source_hash":"wrong","text":"stale latest"}`)
	events[len(events)-1] = bad
	if got := messagesJSON(t, events); strings.Contains(got, "old summary") || strings.Contains(got, "stale latest") {
		t.Fatal("stale cache revived older memory", got)
	}
	events = s.events(t)
	events[0].Payload = json.RawMessage(`{"text":"changed source"}`)
	got := messagesJSON(t, events)
	if strings.Contains(got, "summary") || !strings.Contains(got, "changed source") {
		t.Fatal("stale cache hid source", got)
	}
	s.w.Append(log.KindSummaryDrop, log.SummaryDrop{SummarySeq: latest.Seq})
	got = messagesJSON(t, s.events(t))
	if strings.Contains(got, "old summary") || strings.Contains(got, "new summary") || !strings.Contains(got, "question 0") {
		t.Fatal("drop revived superseded memory", got)
	}
}
func TestSummaryRejectsPartialSpansAndResetsElision(t *testing.T) {
	s, from, through := summaryFixture(t)
	for _, span := range [][2]uint64{{0, 4}, {2, 4}, {1, 2}, {1, 3}, {1, 999}, {5, 4}} {
		if _, err := SummarySourceHash(s.events(t), span[0], span[1]); err == nil {
			t.Fatal("partial span accepted", span)
		}
	}
	s.w.Append(log.KindLLMReq, log.LLMReq{Elided: []uint64{2, 3, 4}})
	ev := addTestSummary(t, s, from, through, "summary")
	s.w.Append(log.KindSummaryDrop, log.SummaryDrop{SummarySeq: ev.Seq})
	got := messagesJSON(t, s.events(t))
	if strings.Contains(got, "elided seq=") || !strings.Contains(got, "answer 0") {
		t.Fatal("revocation retained stale derived elision", got)
	}
}
func TestSummaryAwareRequestReplayAndV1Isolation(t *testing.T) {
	s, from, through := summaryFixture(t)
	recipe := log.WindowContext{Version: RecipeVersion, System: "sys", Tools: json.RawMessage(`[]`), HighWater: 100000, LowWater: 50000, MaxResultTokens: 10000}
	s.w.Append(log.KindSessionStart, log.SessionStart{Context: &recipe})
	ev := addTestSummary(t, s, from, through, "replay summary")
	built := Build(s.events(t), "sys", Estimate("[]"), current(Policy{HighWater: 100000, LowWater: 50000, MaxResultTokens: 10000}))
	req := llm.Request{Messages: built.Messages, Tools: []llm.Tool{}}
	hash, _ := RequestHash(req)
	request, _ := s.w.Append(log.KindLLMReq, log.LLMReq{RequestHash: hash})
	s.w.Append(log.KindSummaryDrop, log.SummaryDrop{SummarySeq: ev.Seq})
	replayed, err := ReplayRequest(s.events(t), request.Seq)
	if err != nil {
		t.Fatal(err)
	}
	want, _ := json.Marshal(req)
	got, _ := json.Marshal(replayed)
	if !bytes.Equal(got, want) {
		t.Fatal("later revocation changed historical request")
	}
	recipe.Version = "window-v1"
	s.w.Append(log.KindSessionResume, log.SessionStart{Context: &recipe})
	addTestSummary(t, s, from, through, "v1 must ignore this")
	old := buildV1(s.events(t), "sys", Estimate("[]"), Policy{HighWater: 100000, LowWater: 50000, MaxResultTokens: 10000})
	req.Messages = old.Messages
	hash, _ = RequestHash(req)
	request, _ = s.w.Append(log.KindLLMReq, log.LLMReq{RequestHash: hash})
	replayed, err = ReplayRequest(s.events(t), request.Seq)
	if err != nil {
		t.Fatal(err)
	}
	got, _ = json.Marshal(replayed)
	if bytes.Contains(got, []byte("v1 must ignore")) {
		t.Fatal("v1 changed")
	}
}
func TestRealV1SelfcheckStillReplays(t *testing.T) {
	raw, err := os.ReadFile("../../test/sessions/p10-window-v1-selfcheck.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	events, err := log.Read(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, ev := range events {
		if ev.Kind == log.KindLLMReq {
			if _, err := ReplayRequest(events, ev.Seq); err != nil {
				t.Fatal(err)
			}
			found = true
		}
	}
	if !found {
		t.Fatal("fixture has no recorded request")
	}
}
