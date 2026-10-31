package core

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/fclaude/nodwyn/internal/budget"
	"github.com/fclaude/nodwyn/internal/config"
	"github.com/fclaude/nodwyn/internal/event"
	"github.com/fclaude/nodwyn/internal/llm"
	"github.com/fclaude/nodwyn/internal/log"
	fake "github.com/fclaude/nodwyn/test/fakes/ollama"
)

// A task's limits are its own and none unless set (ADR 49, amended
// 2026-10-27): a task opened without any goes past the tool calls that stop
// the same turn in a task with a limit of one, which stops it there without
// asking first.
func TestATaskHasNoLimitsUnlessSet(t *testing.T) {
	t.Parallel()
	calls := []fake.ToolCall{{ID: "a", Name: "fs-list", Args: map[string]any{"path": "."}}, {ID: "b", Name: "fs-list", Args: map[string]any{"path": "sub"}}, {ID: "c", Name: "fs-list", Args: map[string]any{"path": "other"}}}
	for _, tc := range []struct {
		name      string
		limits    *LimitsChoice
		turns     []fake.Turn
		wantCalls int
		outcome   string
	}{
		{"none", nil, []fake.Turn{{ToolCalls: calls}, {Content: "listed"}}, 3, "completed"},
		{"one call", &LimitsChoice{Limits: budget.Task{ToolCalls: 1}, Actor: "test"}, []fake.Turn{{ToolCalls: calls}}, 1, "hard_budget"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			srv := fake.NewT(t, tc.turns...)
			defer srv.Close()
			o := diskOptions(t)
			o.NoConfirm = false
			o.Provider = llm.NewOllama(srv.URL)
			o.Limits = tc.limits
			s, err := New(o)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			if tc.limits == nil && s.Limits() != (budget.Limits{}) {
				t.Fatalf("a task opened without limits has %+v", s.Limits())
			}
			if err := s.Send("list everything"); err != nil {
				t.Fatal(err)
			}
			followBudget(t, s, func(event.Checkpoint) { t.Fatal("a checkpoint asked") })
			got, outcome := 0, ""
			for _, ev := range s.EventsSoFar() {
				switch ev.Kind {
				case log.KindToolCall:
					got++
				case log.KindTurnEnd:
					var end log.TurnEnd
					if err := ev.Decode(&end); err != nil {
						t.Fatal(err)
					}
					outcome = end.Outcome
				case log.KindSessionLimits:
					if tc.limits == nil {
						t.Fatal("a task opened without limits recorded some")
					}
				}
			}
			if got != tc.wantCalls || outcome != tc.outcome {
				t.Fatalf("tool calls %d, outcome %q; want %d, %q", got, outcome, tc.wantCalls, tc.outcome)
			}
		})
	}
}

// A turn in a task without a time limit has no wall clock: it may wait, as
// for a person's answer overnight, past the time a limit of 100ms cancels
// the same wait at (TestBudgetWallCancelsBlockedModel).
func TestNoTimeLimitHasNoWallClock(t *testing.T) {
	t.Parallel()
	o := diskOptions(t)
	o.Provider = waitingProvider{}
	o.Limits = &LimitsChoice{Limits: budget.Task{ToolCalls: 5}, Actor: "test"}
	s, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.Send("wait"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(400 * time.Millisecond)
	if !s.Busy() {
		t.Fatal("a turn without a time limit stopped")
	}
	s.Abort()
	events := followBudget(t, s, func(event.Checkpoint) { t.Fatal("a checkpoint asked") })
	for _, ev := range events {
		if ev.Kind == event.Error && strings.Contains(ev.Payload.(event.ErrorInfo).Err, "wall budget") {
			t.Fatal("the turn ended at a wall limit")
		}
	}
}

// The limits are the task's: recorded as it opens and as they change, one
// record per change, kept when the task is resumed (a schedule resumes it
// for every run), and none again once removed.
func TestTaskLimitsOutliveResume(t *testing.T) {
	t.Parallel()
	o := diskOptions(t)
	o.Provider = waitingProvider{}
	opened := budget.Task{ToolCalls: 40, Wall: int64(time.Hour), Cost: 2}
	o.Limits = &LimitsChoice{Limits: opened, Actor: "opener"}
	s, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	if limits, actor := s.TaskLimits(); limits != opened || actor != "opener" || s.Limits() != opened.Limits() {
		t.Fatalf("opened: %+v by %q, budget %+v", limits, actor, s.Limits())
	}
	id := s.ID()
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	o.Repo, o.Limits = "", nil
	s, err = Resume(o, id)
	if err != nil {
		t.Fatal(err)
	}
	if limits, _ := s.TaskLimits(); limits != opened {
		t.Fatalf("resumed: %+v", limits)
	}
	changed := budget.Task{Tokens: 1000, Cost: 1.5}
	if err := s.SetLimits(changed, "desk"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetLimits(changed, "desk"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetLimits(budget.Task{Tokens: -1}, "desk"); err == nil {
		t.Fatal("a negative limit was taken")
	}
	if limits, actor := s.TaskLimits(); limits != changed || actor != "desk" || s.Limits() != changed.Limits() {
		t.Fatalf("changed: %+v by %q, budget %+v", limits, actor, s.Limits())
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	s, err = Resume(o, id)
	if err != nil {
		t.Fatal(err)
	}
	if limits, _ := s.TaskLimits(); limits != changed {
		t.Fatalf("resumed after the change: %+v", limits)
	}
	if err := s.SetLimits(budget.Task{}, "desk"); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Resume(o, id)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if limits, _ := s.TaskLimits(); limits != (budget.Task{}) || s.Limits() != (budget.Limits{}) {
		t.Fatalf("resumed after removing them: %+v", limits)
	}
	var records []log.SessionLimits
	for _, ev := range s.EventsSoFar() {
		if ev.Kind == log.KindSessionLimits {
			var rec log.SessionLimits
			if err := ev.Decode(&rec); err != nil {
				t.Fatal(err)
			}
			records = append(records, rec)
		}
	}
	want := []log.SessionLimits{{Actor: "opener", Task: opened}, {Actor: "desk", Task: changed}, {Actor: "desk"}}
	if len(records) != len(want) || records[0] != want[0] || records[1] != want[1] || records[2] != want[2] {
		t.Fatalf("session.limits records %+v, want %+v (one per change)", records, want)
	}
}

// A record from before a task's limits were its own held only on or off,
// against limits config.toml no longer sets: either reads as none.
func TestAnOldLimitsRecordReadsAsNone(t *testing.T) {
	t.Parallel()
	for _, raw := range []string{`{"actor":"desk","on":true}`, `{"on":false}`} {
		events := []log.Event{{Kind: log.KindSessionLimits, Payload: json.RawMessage(raw)}}
		rec, found := lastLimits(events)
		if !found || rec.Task != (budget.Task{}) {
			t.Fatalf("%s read as %+v (found %v)", raw, rec, found)
		}
	}
}

// Who answers a task's commands is the task's once chosen: recorded as it
// opens and as it changes, and kept when the task is resumed, whatever
// approval.commands says by then.
func TestTaskCommandsOutliveResume(t *testing.T) {
	t.Parallel()
	o := diskOptions(t)
	o.Provider = waitingProvider{}
	o.Config.Approval.Commands = config.ApproveAsk
	o.Commands = &CommandsChoice{Mode: config.ApproveJudgeAuto, Actor: "opener"}
	s, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	id := s.ID()
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	o.Repo, o.Commands = "", nil
	o.Config.Approval.Commands = config.ApproveJudge
	s, err = Resume(o, id)
	if err != nil {
		t.Fatal(err)
	}
	if mode, actor := s.Commands(); mode != config.ApproveJudgeAuto || actor != "opener" {
		t.Fatalf("resumed: %q by %q", mode, actor)
	}
	if err := s.SetCommands(config.ApproveAllow, "desk"); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Resume(o, id)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if mode, actor := s.Commands(); mode != config.ApproveAllow || actor != "desk" {
		t.Fatalf("resumed after the change: %q by %q", mode, actor)
	}
}
