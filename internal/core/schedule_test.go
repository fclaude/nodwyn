package core

import (
	"math"
	"strings"
	"testing"
	"time"

	"github.com/fclaude/nodwyn/internal/config"
	"github.com/fclaude/nodwyn/internal/event"
	"github.com/fclaude/nodwyn/internal/llm"
	"github.com/fclaude/nodwyn/internal/log"
	"github.com/fclaude/nodwyn/internal/schedule"
	fake "github.com/fclaude/nodwyn/test/fakes/ollama"
)

func TestScheduledRunsResetSpendAndResume(t *testing.T) {
	srv := fake.NewT(t, fake.Turn{Content: "first"}, fake.Turn{Content: "second"})
	defer srv.Close()
	o := diskOptions(t)
	o.NoConfirm = false
	o.Provider = llm.NewOllama(srv.URL)
	s, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	d := log.SessionSchedule{Definition: schedule.Definition{Message: "check", Repeat: "daily", At: "2026-10-16T09:00", Cost: 2}}
	if _, err = s.Record(log.KindSessionSchedule, d); err != nil {
		t.Fatal(err)
	}
	cost := 1.5
	if _, err = s.Record(log.KindLLMResp, log.LLMResp{Usage: log.Usage{Cost: &cost}}); err != nil {
		t.Fatal(err)
	}
	for n := 0; n < 2; n++ {
		if err = s.SendScheduled("check", log.ScheduleRun{Due: time.Now(), Started: time.Now(), Cost: 2, Manual: true}); err != nil {
			t.Fatal(err)
		}
		followBudget(t, s, func(event.Checkpoint) { t.Fatal("earlier run's spend carried over") })
		got, ok := s.RunSpend()
		if !ok || got != 0 {
			t.Fatalf("run spend %v %v", got, ok)
		}
		if _, err = s.Record(log.KindLLMResp, log.LLMResp{Usage: log.Usage{Cost: &cost}}); err != nil {
			t.Fatal(err)
		}
		id := s.ID()
		if err = s.Close(); err != nil {
			t.Fatal(err)
		}
		s, err = Resume(o, id)
		if err != nil {
			t.Fatal(err)
		}
		got, ok = s.RunSpend()
		if !ok || math.Abs(got-1.5) > 1e-8 {
			t.Fatalf("resumed spend %v %v", got, ok)
		}
	}
	defer s.Close()
	state, err := (Store{Dir: o.Paths.SessionsDir}).Schedule(s.ID())
	if err != nil {
		t.Fatal(err)
	}
	if state.Definition == nil || state.LastRun == nil || !state.Cursor.IsZero() {
		t.Fatalf("%+v", state)
	}
	totals, err := (Store{Dir: o.Paths.SessionsDir}).Totals(s.ID())
	if err != nil {
		t.Fatal(err)
	}
	if totals.Runs != 2 {
		t.Fatalf("run boundaries: %d", totals.Runs)
	}
}

func TestScheduledHandoverWaitsForNextRun(t *testing.T) {
	srv := fake.NewT(t, fake.Turn{Content: "first answer"}, fake.Turn{Content: "Carry the release number 42."}, fake.Turn{Content: "human answer"}, fake.Turn{Content: "next run answer"})
	defer srv.Close()
	o := diskOptions(t)
	o.NoConfirm = false
	o.Provider = llm.NewOllama(srv.URL)
	s, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	def := log.SessionSchedule{Definition: schedule.Definition{Message: "check release", Repeat: "daily", At: "2026-10-16T09:00"}}
	if _, err = s.Record(log.KindSessionSchedule, def); err != nil {
		t.Fatal(err)
	}
	bulk := "Earlier release evidence " + strings.Repeat("z", 50000)
	if _, err = s.Record(log.KindUserMsg, log.UserMsg{Text: bulk}); err != nil {
		t.Fatal(err)
	}
	if err = s.SendScheduled(def.Message, log.ScheduleRun{Manual: true}); err != nil {
		t.Fatal(err)
	}
	followBudget(t, s, func(event.Checkpoint) { t.Fatal("checkpoint") })
	note := s.handover.Load()
	if note == nil || !strings.Contains(note.Text, "42") {
		t.Fatalf("handover: %+v", note)
	}
	for _, ev := range s.EventsSoFar() {
		if ev.Kind == log.KindContextNarrow {
			t.Fatal("handover applied before next run")
		}
	}
	// The note is for the next run: the agent's last answer is still its own.
	if reply, ok := s.FinalReply(); !ok || reply.Content != "first answer" {
		t.Fatalf("the last answer after the handover: %q", reply.Content)
	}
	if err = s.Send("What was that evidence?"); err != nil {
		t.Fatal(err)
	}
	followBudget(t, s, func(event.Checkpoint) { t.Fatal("checkpoint") })
	if err = s.SendScheduled(def.Message, log.ScheduleRun{Manual: true}); err != nil {
		t.Fatal(err)
	}
	followBudget(t, s, func(event.Checkpoint) { t.Fatal("checkpoint") })
	// Reading the fake's requests is safe after the final idle event.
	if len(srv.Requests) != 4 {
		t.Fatalf("%d requests", len(srv.Requests))
	}
	if !strings.Contains(string(srv.Requests[2].Raw), "Earlier release evidence") {
		t.Fatal("human follow-up lost the whole conversation")
	}
	// The ask for the note and the note stay out of the conversation.
	if follow := string(srv.Requests[2].Raw); strings.Contains(follow, "Carry the release number 42") || strings.Contains(follow, "handover note") {
		t.Fatal("the human follow-up carries the handover's exchange")
	}
	last := string(srv.Requests[3].Raw)
	if strings.Contains(last, "Earlier release evidence") || !strings.Contains(last, "Carry the release number 42") {
		t.Fatal("next run did not start from its handover")
	}
}

func TestScheduledRunDiscardsDontAskAndRequiresApproval(t *testing.T) {
	srv := fake.NewT(t, fake.Turn{ToolCalls: []fake.ToolCall{{ID: "write", Name: "fs-write", Args: map[string]any{"path": "scheduled.txt", "content": "scheduled"}}}}, fake.Turn{Content: "written"})
	defer srv.Close()
	o := diskOptions(t)
	o.NoConfirm = false
	o.Config.Approval.DontAsk = true
	o.Config.Approval.Writes = config.ApproveAsk
	o.Provider = llm.NewOllama(srv.URL)
	s, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err = s.SetApproveAll(true, "test"); err != nil {
		t.Fatal(err)
	}
	if err = s.SendScheduled("write", log.ScheduleRun{Manual: true}); err != nil {
		t.Fatal(err)
	}
	events := followBudget(t, s, func(event.Checkpoint) { t.Fatal("checkpoint") })
	confirms := 0
	for _, ev := range events {
		if ev.Kind == event.PromptConfirm {
			confirms++
		}
	}
	if confirms != 1 {
		t.Fatalf("scheduled write asked %d times", confirms)
	}
	all, _ := s.ApproveAll()
	if all {
		t.Fatal("scheduled run retained Don't ask")
	}
}
