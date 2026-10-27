package budget

import (
	"math"
	"testing"
	"time"
)

// A task's limits are its own and none unless set (ADR 49, amended
// 2026-10-28): each is one number, so a turn stops at it without a
// checkpoint, and a change is within the old limits only when it adds or
// lowers one.
func TestTaskLimits(t *testing.T) {
	for _, bad := range []Task{{ToolCalls: -1}, {Tokens: -5}, {Actions: -1}, {Wall: -1}, {Cost: -1}, {Cost: math.NaN()}, {Cost: math.Inf(1)}, {Cost: 1e-9}} {
		if bad.Validate() == nil {
			t.Errorf("%+v was taken", bad)
		}
	}
	if err := (Task{}).Validate(); err != nil {
		t.Fatalf("no limits refused: %v", err)
	}
	limits := Task{ToolCalls: 3, Wall: int64(time.Minute), Cost: 2.5}.Limits()
	want := Limits{ToolCalls: Limit{3, 3}, Wall: Limit{int64(time.Minute), int64(time.Minute)}, Cost: Limit{2_500_000, 2_500_000}}
	if limits != want {
		t.Fatalf("limits %+v, want %+v", limits, want)
	}
	if (Task{}).Limits() != (Limits{}) {
		t.Fatalf("no limits gave %+v", Task{}.Limits())
	}

	was := Task{ToolCalls: 10, Cost: 2}
	for _, tc := range []struct {
		now  Task
		want bool
	}{
		{was, true},
		{Task{ToolCalls: 10, Cost: 2, Tokens: 100}, true}, // one added
		{Task{ToolCalls: 5, Cost: 1.5}, true},             // both lowered
		{Task{ToolCalls: 11, Cost: 2}, false},             // one raised
		{Task{ToolCalls: 10, Cost: 2.01}, false},          // spend raised
		{Task{Cost: 2}, false},                            // one removed
		{Task{}, false},                                   // all removed
	} {
		if got := tc.now.Within(was); got != tc.want {
			t.Errorf("%+v within %+v: %v, want %v", tc.now, was, got, tc.want)
		}
	}
	if !(Task{Wall: 1}).Within(Task{}) || (Task{}).Within(Task{Wall: 1}) {
		t.Fatal("from none, any limit is within; to none from one is not")
	}
	for task, want := range map[Task]string{
		{}:                                      "none",
		{ToolCalls: 40, Wall: int64(time.Hour)}: "tool calls 40, time 1h0m0s a turn",
		{Tokens: 1000, Cost: 2}:                 "tokens 1000 a turn; spend $2.00 for the task",
		{Actions: 3}:                            "actions 3 a turn",
		{Cost: 0.5}:                             "spend $0.50 for the task",
	} {
		if got := task.String(); got != want {
			t.Errorf("%+v reads %q, want %q", task, got, want)
		}
	}
}
