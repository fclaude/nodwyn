package core

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/fclaude/nodwyn/internal/log"
	"github.com/fclaude/nodwyn/internal/redact"
	"github.com/fclaude/nodwyn/internal/window"
)

// longLog writes a session of turns turns straight to its log, as a task
// that has run every day for months leaves one: a reply priced at a cent
// each, the task's limits turned off near its start, a compaction
// (a lasting narrowing and its handover) before its last few turns, and
// whatever extra the caller adds at the end.
func longLog(t *testing.T, o Options, id string, turns, compactAt int, extra func(write func(log.Kind, any))) {
	t.Helper()
	if err := os.MkdirAll(o.Paths.SessionsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(log.SessionPath(o.Paths.SessionsDir, id))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	w := bufio.NewWriter(f)
	ts := time.Date(2026, 9, 1, 7, 0, 0, 0, time.UTC)
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
		if _, err := w.Write(append(line, '\n')); err != nil {
			t.Fatal(err)
		}
	}
	write(log.KindSessionStart, log.SessionStart{Repo: o.Repo})
	write(log.KindSessionLimits, log.SessionLimits{Actor: "desk", On: false})
	for i := range turns {
		if i == compactAt {
			write(log.KindHarnessMsg, log.HarnessMsg{Text: "write the handover"})
			write(log.KindLLMResp, log.LLMResp{Content: "the note: what the runs so far did"})
			write(log.KindContextNarrow, log.ContextNarrow{System: "sys", Agent: "compaction", Lasting: true})
			write(log.KindHarnessMsg, log.HarnessMsg{Text: "The conversation was cleared. The note: what the runs so far did"})
			write(log.KindContextCompact, log.ContextCompact{Trigger: "auto", Compacted: true})
		}
		cost := 0.01 + float64(i%7)/1000
		write(log.KindUserMsg, log.UserMsg{Text: fmt.Sprintf("run %d: look at last night's failures", i)})
		write(log.KindLLMResp, log.LLMResp{ToolCalls: []log.ToolCall{{ID: "c", Name: "fs-list", Args: json.RawMessage(`{"path":"."}`)}}, Usage: log.Usage{Cost: &cost}})
		write(log.KindToolResult, log.ToolResult{ID: "c", Name: "fs-list", OK: true, Value: fmt.Sprintf("listing %d", i)})
		write(log.KindLLMResp, log.LLMResp{Content: fmt.Sprintf("run %d done", i), Usage: log.Usage{Cost: &cost}})
		write(log.KindTurnEnd, log.TurnEnd{Outcome: "completed"})
	}
	if extra != nil {
		extra(write)
	}
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
}

// A resumed session holds the end of its log (ADR 49 §10): from where its
// window and its other readers need it, not from its first event. What it
// holds builds the window the whole log builds, adds up to the spend the
// whole log has, keeps the limits chosen before it, gives the judge the
// same earlier requests, and keeps an unfinished turn's plan. A log with a
// summary is held whole.
func TestResumeHoldsTheEndOfALongLog(t *testing.T) {
	t.Parallel()
	o := diskOptions(t)
	o.Provider = waitingProvider{}
	id := "20260901T070000Z-0000cccc"
	longLog(t, o, id, 400, 395, func(write func(log.Kind, any)) {
		// The last two turns did not finish: the plan of the first is
		// carried, so the session holds them both.
		write(log.KindUserMsg, log.UserMsg{Text: "run 400: and fix it"})
		write(log.KindGatePlan, log.GatePlan{Origin: "model", Noul: "model", Plan: "1. Read the failure\n2. Fix it"})
		write(log.KindTurnEnd, log.TurnEnd{Outcome: "error"})
		write(log.KindUserMsg, log.UserMsg{Text: "continue"})
		write(log.KindTurnEnd, log.TurnEnd{Outcome: "stopped"})
	})
	o.Repo = ""
	s, err := Resume(o, id)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	held := s.EventsSoFar()
	// The whole log, redacted as a session's events are.
	whole, err := log.ReadFile(log.SessionPath(o.Paths.SessionsDir, id))
	if err != nil {
		t.Fatal(err)
	}
	for i := range whole {
		if whole[i].Payload, _, err = redact.JSON(whole[i].Payload, nil); err != nil {
			t.Fatal(err)
		}
	}
	if held[0].Seq <= 1 || len(held) > len(whole)/5 {
		t.Fatalf("the resumed session holds %d of %d events, from seq %d", len(held), len(whole), held[0].Seq)
	}
	p := window.Policy{HighWater: 1 << 30, LowWater: 1 << 29, MaxResultTokens: 1000, InConversation: true, PairTools: true, Thinking: true, NamedCuts: true}
	if a, b := window.Build(held, "sys", 10, p), window.Build(whole, "sys", 10, p); !reflect.DeepEqual(a, b) {
		t.Fatalf("the window from the events held differs from the whole log's:\n%+v\n%+v", a.Messages, b.Messages)
	}
	var spend float64
	for _, ev := range whole {
		var r log.LLMResp
		if ev.Kind == log.KindLLMResp && ev.Decode(&r) == nil && r.Usage.Cost != nil && *r.Usage.Cost > 0 {
			spend += *r.Usage.Cost
		}
	}
	if got := s.knownSpend(); got != spend {
		t.Fatalf("known spend %v, the whole log's %v", got, spend)
	}
	if on, actor := s.LimitsOn(); on || actor != "desk" {
		t.Fatalf("the limits chosen near the log's start: on %v by %q", on, actor)
	}
	if a, b := earlierRequests(held), earlierRequests(whole); !reflect.DeepEqual(a, b) {
		t.Fatalf("the judge's earlier requests %q, the whole log's %q", a, b)
	}
	plan, err := s.Eval(t.Context(), "(assoc-ref (unfinished-plan) 'plan)")
	if err != nil || plan.String() != `"1. Read the failure\n2. Fix it"` {
		t.Fatalf("the unfinished turn's plan: %v, %v", plan, err)
	}

	// A summary can replace a span the window's start knows nothing of:
	// such a log is held whole.
	summarized := "20260901T070000Z-0000dddd"
	o.Repo = t.TempDir()
	longLog(t, o, summarized, 300, 290, func(write func(log.Kind, any)) {
		write(log.KindSummary, log.Summary{From: 3, Through: 6, Text: "early runs"})
	})
	o.Repo = ""
	r, err := Resume(o, summarized)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if first := r.EventsSoFar()[0].Seq; first != 1 {
		t.Fatalf("a log with a summary is held from seq %d", first)
	}
}
