package core

import (
	"strings"
	"testing"
	"time"

	"github.com/fclaude/nodwyn/internal/budget"
	"github.com/fclaude/nodwyn/internal/event"
	"github.com/fclaude/nodwyn/internal/llm"
	"github.com/fclaude/nodwyn/internal/log"
	fake "github.com/fclaude/nodwyn/test/fakes/ollama"
)

// A task whose limits are off (ADR 49) goes past the turn limits that stop
// the same turn in a task whose limits are on: here a hard limit of one
// tool call, which the model's three calls pass.
func TestTurnLimitsOffLetATurnGoOn(t *testing.T) {
	t.Parallel()
	calls := []fake.ToolCall{{ID: "a", Name: "fs-list", Args: map[string]any{"path": "."}}, {ID: "b", Name: "fs-list", Args: map[string]any{"path": "sub"}}, {ID: "c", Name: "fs-list", Args: map[string]any{"path": "other"}}}
	for _, tc := range []struct {
		name      string
		limits    *LimitsChoice
		turns     []fake.Turn
		wantCalls int
		outcome   string
	}{
		{"on", nil, []fake.Turn{{ToolCalls: calls}}, 1, "hard_budget"},
		{"off", &LimitsChoice{On: false, Actor: "test"}, []fake.Turn{{ToolCalls: calls}, {Content: "listed"}}, 3, "completed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			srv := fake.NewT(t, tc.turns...)
			defer srv.Close()
			o := diskOptions(t)
			o.Config.Budget.ToolCallsSoft = 1
			o.Config.Budget.ToolCallsHard = 1
			o.Provider = llm.NewOllama(srv.URL)
			o.Limits = tc.limits
			s, err := New(o)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
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
				}
			}
			if got != tc.wantCalls || outcome != tc.outcome {
				t.Fatalf("tool calls %d, outcome %q; want %d, %q", got, outcome, tc.wantCalls, tc.outcome)
			}
		})
	}
}

// A turn in a task whose limits are off has no wall clock: it may wait, as
// for a person's answer overnight, past the hard wall limit that cancels the
// same wait with them on (TestBudgetWallCancelsBlockedModel).
func TestTurnLimitsOffHaveNoWallClock(t *testing.T) {
	t.Parallel()
	o := diskOptions(t)
	o.Provider = waitingProvider{}
	o.Config.Budget.WallSoft = "50ms"
	o.Config.Budget.WallHard = "100ms"
	o.Limits = &LimitsChoice{On: false, Actor: "test"}
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
		t.Fatal("the wall limit stopped a turn in a task whose limits are off")
	}
	s.Abort()
	events := followBudget(t, s, func(event.Checkpoint) { t.Fatal("a checkpoint asked") })
	for _, ev := range events {
		if ev.Kind == event.Error && strings.Contains(ev.Payload.(event.ErrorInfo).Err, "wall budget") {
			t.Fatal("the turn ended at the wall limit")
		}
	}
}

// The choice is the task's: recorded as it opens and as it changes, kept
// when the task is resumed (a schedule resumes it for every run), and it
// leaves the spend limit, which is not a turn limit, as it was.
func TestTurnLimitsChoiceOutlivesResume(t *testing.T) {
	t.Parallel()
	o := diskOptions(t)
	o.Config.Budget.CostHard = 2
	o.Provider = waitingProvider{}
	o.Limits = &LimitsChoice{On: false, Actor: "opener"}
	configured, err := o.Config.Budget.Limits(o.Config.Loop.MaxToolCalls)
	if err != nil {
		t.Fatal(err)
	}
	off := budget.Limits{Cost: configured.Cost}
	s, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	if on, actor := s.LimitsOn(); on || actor != "opener" || s.Limits() != off {
		t.Fatalf("opened with limits off: on %v by %q, limits %+v", on, actor, s.Limits())
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
	if on, _ := s.LimitsOn(); on || s.Limits() != off {
		t.Fatalf("resumed: on %v, limits %+v", on, s.Limits())
	}
	if err := s.SetLimits(true, "desk"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetLimits(true, "desk"); err != nil {
		t.Fatal(err)
	}
	if on, actor := s.LimitsOn(); !on || actor != "desk" || s.Limits() != configured {
		t.Fatalf("turned on: on %v by %q, limits %+v", on, actor, s.Limits())
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	s, err = Resume(o, id)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if on, _ := s.LimitsOn(); !on || s.Limits() != configured {
		t.Fatalf("resumed after turning them on: on %v, limits %+v", on, s.Limits())
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
	want := []log.SessionLimits{{Actor: "opener", On: false}, {Actor: "desk", On: true}}
	if len(records) != len(want) || records[0] != want[0] || records[1] != want[1] {
		t.Fatalf("session.limits records %+v, want %+v (one per change)", records, want)
	}
}
