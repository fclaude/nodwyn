package rpc

import (
	"strings"
	"testing"
	"time"

	"github.com/fclaude/nodwyn/internal/budget"
)

// A task's limits are its own and none unless set (ADR 49, amended
// 2026-10-27): it opens with the ones session.open names, or none, which
// session.info reports, with a budget of the same limits stopping at each;
// session.limits.set replaces them all, a resumed task keeps them, and a
// paired device may open a task with limits, add one or lower one, but
// neither raise nor remove one.
func TestTasksSetTheirOwnLimits(t *testing.T) {
	path, _ := start(t, options(t))
	c := connect(t, path)
	c.hello()

	type details struct {
		ID      string      `json:"id"`
		Limits  budget.Task `json:"limits"`
		Budgets budgetInfo  `json:"budgets"`
	}
	var none, some details
	into(t, c.ok("session.open", map[string]any{"repo": t.TempDir()}), &none)
	if none.Limits != (budget.Task{}) || none.Budgets != (budgetInfo{}) {
		t.Fatalf("a task opened without limits: %+v", none)
	}
	hour := int64(time.Hour)
	into(t, c.ok("session.open", map[string]any{"repo": t.TempDir(), "limits": map[string]any{"tool_calls": 40, "wall_ns": hour, "cost": 2.5}}), &some)
	want := budget.Task{ToolCalls: 40, Wall: hour, Cost: 2.5}
	b := some.Budgets
	if some.Limits != want || b.ToolCalls != (budget.Limit{Soft: 40, Hard: 40}) || b.Wall.Hard != hour || b.Wall.Soft != hour || b.Cost.Hard != 2_500_000 || b.Tokens.Hard != 0 {
		t.Fatalf("a task opened with limits: %+v", some)
	}
	if r := c.call("session.open", map[string]any{"repo": t.TempDir(), "limits": map[string]any{"tokens": -1}}); r.Error == nil {
		t.Fatal("a negative limit opened a task")
	}

	var set budget.Task
	into(t, c.ok("session.limits.set", map[string]any{"id": none.ID, "limits": map[string]any{"actions": 5}}), &set)
	var now details
	into(t, c.ok("session.info", map[string]any{"id": none.ID}), &now)
	if set != (budget.Task{Actions: 5}) || now.Limits != set || now.Budgets.Actions.Hard != 5 {
		t.Fatalf("limits set: %+v, then %+v", set, now)
	}
	into(t, c.ok("session.limits.set", map[string]any{"id": none.ID, "limits": map[string]any{"tokens": 1000}}), &set)
	if set != (budget.Task{Tokens: 1000}) {
		t.Fatalf("a set did not replace the limits: %+v", set)
	}
	if r := c.call("session.limits.set", map[string]any{"id": none.ID, "limits": map[string]any{"cost": -2}}); r.Error == nil {
		t.Fatal("a negative spend limit was taken")
	}
	c.ok("session.close", map[string]any{"id": none.ID})
	into(t, c.ok("session.resume", map[string]any{"id": none.ID}), &now)
	if now.Limits != (budget.Task{Tokens: 1000}) {
		t.Fatalf("resumed: %+v", now)
	}

	phone := connect(t, path)
	phone.ok("hello", map[string]any{"protocol": 1, "via": "web:phone"})
	var mine details
	into(t, phone.ok("session.open", map[string]any{"repo": t.TempDir(), "limits": map[string]any{"wall_ns": hour}}), &mine)
	if mine.Limits != (budget.Task{Wall: hour}) {
		t.Fatalf("a device opened a task with limits: %+v", mine)
	}
	for _, tighter := range []map[string]any{
		{"tool_calls": 40, "wall_ns": hour, "cost": 2.5, "tokens": 500}, // one added
		{"tool_calls": 20, "wall_ns": hour, "cost": 1, "tokens": 500},   // two lowered
	} {
		if r := phone.call("session.limits.set", map[string]any{"id": some.ID, "limits": tighter}); r.Error != nil {
			t.Fatalf("a device could not tighten %v: %v", tighter, r.Error)
		}
	}
	for _, looser := range []map[string]any{
		{"tool_calls": 30, "wall_ns": hour, "cost": 1, "tokens": 500}, // raised
		{"tool_calls": 20, "cost": 1, "tokens": 500},                  // one removed
		{}, // all removed
	} {
		if r := phone.call("session.limits.set", map[string]any{"id": some.ID, "limits": looser}); r.Error == nil || !strings.Contains(r.Error.Message, "paired device") {
			t.Fatalf("a device loosened to %v: %+v", looser, r.Error)
		}
	}
	into(t, c.ok("session.info", map[string]any{"id": some.ID}), &now)
	if now.Limits != (budget.Task{ToolCalls: 20, Tokens: 500, Wall: hour, Cost: 1}) {
		t.Fatalf("after the device's changes: %+v", now.Limits)
	}
	into(t, c.ok("session.limits.set", map[string]any{"id": some.ID, "limits": map[string]any{}}), &set)
	if set != (budget.Task{}) {
		t.Fatalf("this computer could not remove the limits: %+v", set)
	}
}
