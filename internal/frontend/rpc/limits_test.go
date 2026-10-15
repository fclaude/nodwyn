package rpc

import (
	"strings"
	"testing"
)

// A task's turn limits are on unless this computer turns them off (ADR 49):
// as it opens, or between turns. Off, session.info says so and reports no
// limit, a hard limit of 0, on the four turn dimensions, keeping the spend
// limit; a resumed task keeps the choice; and a paired device can neither
// open a task without limits nor change a task's.
func TestTasksTurnTheirLimitsOff(t *testing.T) {
	o := options(t)
	o.Config.Budget.CostHard = 3
	path, _ := start(t, o)
	c := connect(t, path)
	c.hello()

	type details struct {
		ID      string     `json:"id"`
		Limits  bool       `json:"limits"`
		Budgets budgetInfo `json:"budgets"`
	}
	var on, off details
	into(t, c.ok("session.open", map[string]any{"repo": t.TempDir()}), &on)
	if !on.Limits || on.Budgets.ToolCalls.Hard != 500 || on.Budgets.Wall.Hard == 0 {
		t.Fatalf("a task opened without a choice: %+v", on)
	}
	into(t, c.ok("session.open", map[string]any{"repo": t.TempDir(), "limits": false}), &off)
	b := off.Budgets
	if off.Limits || b.ToolCalls.Hard != 0 || b.Tokens.Hard != 0 || b.Actions.Hard != 0 || b.Wall.Hard != 0 || b.Cost != on.Budgets.Cost || b.Cost.Hard != 3_000_000 {
		t.Fatalf("a task opened with its limits off: %+v", off)
	}

	if r := c.call("session.limits.set", map[string]any{"id": off.ID}); r.Error == nil || !strings.Contains(r.Error.Message, "on") {
		t.Fatalf("a change that says neither on nor off was taken: %+v", r)
	}
	var state limitsState
	into(t, c.ok("session.limits.set", map[string]any{"id": off.ID, "on": true}), &state)
	var now details
	into(t, c.ok("session.info", map[string]any{"id": off.ID}), &now)
	if !state.On || !now.Limits || now.Budgets != on.Budgets {
		t.Fatalf("turned on: %+v, then %+v", state, now)
	}
	into(t, c.ok("session.limits.set", map[string]any{"id": off.ID, "on": false}), &state)
	c.ok("session.close", map[string]any{"id": off.ID})
	into(t, c.ok("session.resume", map[string]any{"id": off.ID}), &now)
	if state.On || now.Limits || now.Budgets.Wall.Hard != 0 {
		t.Fatalf("resumed after turning them off: %+v, then %+v", state, now)
	}

	phone := connect(t, path)
	phone.ok("hello", map[string]any{"protocol": 1, "via": "web:phone"})
	if r := phone.call("session.open", map[string]any{"repo": t.TempDir(), "limits": false}); r.Error == nil || !strings.Contains(r.Error.Message, "paired device") {
		t.Fatalf("a device opened a task without limits: %+v", r.Error)
	}
	phone.ok("session.open", map[string]any{"repo": t.TempDir(), "limits": true})
	for _, want := range []bool{true, false} {
		if r := phone.call("session.limits.set", map[string]any{"id": on.ID, "on": want}); r.Error == nil || !strings.Contains(r.Error.Message, "paired device") {
			t.Fatalf("a device set a task's limits on=%v: %+v", want, r.Error)
		}
	}
}
