package core

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/fclaude/nodwyn/internal/llm"
	"github.com/fclaude/nodwyn/internal/log"
	"github.com/fclaude/nodwyn/internal/window"
	fake "github.com/fclaude/nodwyn/test/fakes/ollama"
)

func TestSummaryStoreIsExclusiveAppendOnlyAndRedacted(t *testing.T) {
	srv := fake.NewT(t, fake.Turn{Content: "first answer"}, fake.Turn{Content: "second answer"}, fake.Turn{Content: "third answer"})
	defer srv.Close()
	o := diskOptions(t)
	o.Provider = llm.NewOllama(srv.URL)
	s, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"first question", "second question", "third question"} {
		if err := s.Send(text); err != nil {
			t.Fatal(err)
		}
		(&rig{t: t, s: s}).drain("n")
	}
	store := Store{Dir: o.Paths.SessionsDir}
	id := s.ID()
	path := log.SessionPath(o.Paths.SessionsDir, id)
	events, err := log.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	from, through := uint64(0), uint64(0)
	for _, ev := range events {
		if from == 0 && ev.Kind == log.KindUserMsg {
			from = ev.Seq
		}
		if through == 0 && ev.Kind == log.KindLLMResp {
			through = ev.Seq
		}
	}
	if _, err := store.AddSummary(id, from, through, "cannot race active owner"); err == nil {
		t.Fatal("edited an owned session")
	}
	s.Close()
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	const canary = "sk-ant-abcdefgh1234"
	ev, err := store.AddSummary(id, from, through, "Reviewed project. "+canary)
	if err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(path)
	if !bytes.HasPrefix(after, before) || bytes.Contains(after, []byte(canary)) {
		t.Fatal("rewritten source or secret persisted")
	}
	views, err := store.Summaries(id)
	if err != nil || len(views) != 1 || views[0].Status != "active" || strings.Contains(views[0].Summary.Text, canary) {
		t.Fatal(views, err)
	}
	if _, err := store.DropSummary(id, from); err == nil {
		t.Fatal("revoked a non-summary")
	}
	if _, err := store.DropSummary(id, ev.Seq); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DropSummary(id, ev.Seq); err == nil {
		t.Fatal("duplicate revocation accepted")
	}
	events, err = log.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := json.Marshal(window.Build(events, "sys", 0, window.Policy{HighWater: 100000, LowWater: 50000, MaxResultTokens: 1000}).Messages)
	if !bytes.Contains(got, []byte("first question")) || bytes.Contains(got, []byte("Reviewed project")) {
		t.Fatal("revocation did not recompute")
	}
	// Truncated files require the existing recovery path, never silent repair by edits.
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString(`{"seq":`)
	f.Close()
	raw, _ := os.ReadFile(path)
	if _, err := store.AddSummary(id, from, through, "not written"); err == nil {
		t.Fatal("edited truncated log")
	}
	current, _ := os.ReadFile(path)
	if !bytes.Equal(raw, current) {
		t.Fatal("summary operation repaired source")
	}
}

func TestSummaryTextCannotBeSilentlyTruncated(t *testing.T) {
	if _, err := (Store{}).AddSummary("unused", 1, 2, string([]byte{0xff})); err == nil || !strings.Contains(err.Error(), "UTF-8") {
		t.Fatal("invalid UTF-8 accepted", err)
	}
	_, err := (Store{}).AddSummary("unused", 1, 2, strings.Repeat("x", window.MaxSummaryBytes)+" ")
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatal("oversized input not rejected before IO", err)
	}
}

func TestSummaryAndRevocationReachProviderAndRemainReplayable(t *testing.T) {
	srv := fake.NewT(t, fake.Turn{Content: "alpha answer"}, fake.Turn{Content: "beta answer"}, fake.Turn{Content: "gamma answer"}, fake.Turn{Content: "delta answer"}, fake.Turn{Content: "epsilon answer"})
	defer srv.Close()
	capture := &captureRequests{Provider: llm.NewOllama(srv.URL)}
	o := diskOptions(t)
	o.Provider = capture
	s, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"alpha-original", "beta-original", "gamma-original"} {
		if err := s.Send(text); err != nil {
			t.Fatal(err)
		}
		(&rig{t: t, s: s}).drain("n")
	}
	id := s.ID()
	s.Close()
	path := log.SessionPath(o.Paths.SessionsDir, id)
	events, err := log.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var from, through uint64
	for _, ev := range events {
		if from == 0 && ev.Kind == log.KindUserMsg {
			from = ev.Seq
		}
		if through == 0 && ev.Kind == log.KindLLMResp {
			through = ev.Seq
		}
	}
	store := Store{Dir: o.Paths.SessionsDir}
	summary, err := store.AddSummary(id, from, through, "A concise reviewed summary.")
	if err != nil {
		t.Fatal(err)
	}
	resumed, err := Resume(o, id)
	if err != nil {
		t.Fatal(err)
	}
	if err := resumed.Send("delta-original"); err != nil {
		t.Fatal(err)
	}
	(&rig{t: t, s: resumed}).drain("n")
	resumed.Close()
	capture.mu.Lock()
	withSummary := string(capture.requests[len(capture.requests)-1])
	capture.mu.Unlock()
	if strings.Contains(withSummary, "alpha-original") || !strings.Contains(withSummary, "A concise reviewed summary") || !strings.Contains(withSummary, "gamma-original") {
		t.Fatal("summary not used by actual provider")
	}
	if _, err := store.DropSummary(id, summary.Seq); err != nil {
		t.Fatal(err)
	}
	resumed, err = Resume(o, id)
	if err != nil {
		t.Fatal(err)
	}
	if err := resumed.Send("epsilon-original"); err != nil {
		t.Fatal(err)
	}
	(&rig{t: t, s: resumed}).drain("n")
	resumed.Close()
	capture.mu.Lock()
	defer capture.mu.Unlock()
	withoutSummary := string(capture.requests[len(capture.requests)-1])
	if !strings.Contains(withoutSummary, "alpha-original") || strings.Contains(withoutSummary, "A concise reviewed summary") {
		t.Fatal("revocation did not reach actual provider")
	}
	events, err = log.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	index := 0
	for _, ev := range events {
		if ev.Kind != log.KindLLMReq {
			continue
		}
		req, err := window.ReplayRequest(events, ev.Seq)
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(req)
		if !bytes.Equal(raw, capture.requests[index]) {
			t.Fatal("historical request changed", ev.Seq)
		}
		index++
	}
	if index != 5 {
		t.Fatal("requests not all checked", index)
	}
}
