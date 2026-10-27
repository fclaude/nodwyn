package budget

import (
	"errors"
	"math"
	"strings"
)

// Task is a task's own limits (ADR 49, amended 2026-10-27): each is none
// unless the person set it, for that task, on the task's panel or on the
// command line. There is no configured default. ToolCalls, Tokens, Actions
// and Wall (nanoseconds) bound each turn; Cost, in US dollars, is a spend
// limit on the whole task. A limit is one number: reaching it stops the
// turn, and no checkpoint asks before it.
type Task struct {
	ToolCalls int64   `json:"tool_calls,omitempty"`
	Tokens    int64   `json:"tokens,omitempty"`
	Actions   int64   `json:"actions,omitempty"`
	Wall      int64   `json:"wall_ns,omitempty"`
	Cost      float64 `json:"cost,omitempty"`
}

// Validate refuses a limit no turn could be held to: a negative count or
// duration, or a spend that is not a number of dollars a millionth of one
// can count.
func (t Task) Validate() error {
	if t.ToolCalls < 0 || t.Tokens < 0 || t.Actions < 0 || t.Wall < 0 {
		return errors.New("limits are positive, or 0 for none")
	}
	if math.IsNaN(t.Cost) || math.IsInf(t.Cost, 0) || t.Cost < 0 || t.Cost > 1e9 {
		return errors.New("the spend limit is US dollars, more than 0, or 0 for none")
	}
	if t.Cost > 0 && Micros(t.Cost) <= 0 {
		return errors.New("the spend limit is below a millionth of a dollar")
	}
	return nil
}

// Limits is the budget a turn of the task starts from: each soft limit is
// its hard one, so a turn stops at a limit without asking first.
func (t Task) Limits() Limits {
	same := func(n int64) Limit { return Limit{Soft: n, Hard: n} }
	return Limits{ToolCalls: same(t.ToolCalls), Tokens: same(t.Tokens), Actions: same(t.Actions), Wall: same(t.Wall), Cost: same(Micros(t.Cost))}
}

// Within reports whether t keeps every limit of was, or tightens it: a
// limit added, or lowered, is within; one removed or raised is not.
func (t Task) Within(was Task) bool {
	within := func(now, before int64) bool { return before == 0 || (now > 0 && now <= before) }
	return within(t.ToolCalls, was.ToolCalls) && within(t.Tokens, was.Tokens) && within(t.Actions, was.Actions) && within(t.Wall, was.Wall) && within(Micros(t.Cost), Micros(was.Cost))
}

// String is the limits as a person reads them: "none", or each one set,
// such as "tool calls 40, time 1h0m0s a turn; spend $2.00 for the task".
func (t Task) String() string {
	var turn []string
	for _, l := range []struct {
		name, dimension string
		n               int64
	}{{"tool calls", "tool_calls", t.ToolCalls}, {"tokens", "tokens", t.Tokens}, {"actions", "actions", t.Actions}, {"time", "wall", t.Wall}} {
		if l.n > 0 {
			turn = append(turn, l.name+" "+Amount(l.dimension, l.n))
		}
	}
	var parts []string
	if len(turn) > 0 {
		parts = append(parts, strings.Join(turn, ", ")+" a turn")
	}
	if t.Cost > 0 {
		parts = append(parts, "spend "+Amount("cost", Micros(t.Cost))+" for the task")
	}
	if len(parts) == 0 {
		return "none"
	}
	return strings.Join(parts, "; ")
}
