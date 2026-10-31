package rpc

import (
	"strings"
	"testing"
)

// Who answers a task's commands is approval.commands' unless the task was
// given its own (ADR 35, amended 2026-10-30): session.open takes one,
// session.commands.set changes it, session.info reports it with who chose
// it, a resumed task keeps it, a value approval.commands does not take is
// refused, and a paired device chooses none of it.
func TestTasksChooseWhoAnswersTheirCommands(t *testing.T) {
	path, _ := start(t, options(t))
	c := connect(t, path)
	c.hello()

	type details struct {
		ID        string        `json:"id"`
		Answering commandsState `json:"answering"`
	}
	var plain, own details
	into(t, c.ok("session.open", map[string]any{"repo": t.TempDir()}), &plain)
	if plain.Answering != (commandsState{Commands: "judge"}) {
		t.Fatalf("a task opened without its own: %+v", plain.Answering)
	}
	into(t, c.ok("session.open", map[string]any{"repo": t.TempDir(), "commands": "judge-auto"}), &own)
	if own.Answering.Commands != "judge-auto" || own.Answering.Actor == "" {
		t.Fatalf("a task opened with its own: %+v", own.Answering)
	}
	if r := c.call("session.open", map[string]any{"repo": t.TempDir(), "commands": "sometimes"}); r.Error == nil {
		t.Fatal("a value approval.commands does not take opened a task")
	}

	var set commandsState
	into(t, c.ok("session.commands.set", map[string]any{"id": plain.ID, "commands": "allow"}), &set)
	var now details
	into(t, c.ok("session.info", map[string]any{"id": plain.ID}), &now)
	if set.Commands != "allow" || now.Answering != set {
		t.Fatalf("set: %+v, then %+v", set, now.Answering)
	}
	if r := c.call("session.commands.set", map[string]any{"id": plain.ID, "commands": "never"}); r.Error == nil {
		t.Fatal("a value approval.commands does not take was set")
	}
	c.ok("session.close", map[string]any{"id": plain.ID})
	into(t, c.ok("session.resume", map[string]any{"id": plain.ID}), &now)
	if now.Answering.Commands != "allow" {
		t.Fatalf("resumed: %+v", now.Answering)
	}

	phone := connect(t, path)
	phone.ok("hello", map[string]any{"protocol": 1, "via": "web:phone"})
	if r := phone.call("session.commands.set", map[string]any{"id": own.ID, "commands": "allow"}); r.Error == nil || !strings.Contains(r.Error.Message, "paired device") {
		t.Fatalf("a device chose who answers a task's commands: %+v", r.Error)
	}
	if r := phone.call("session.open", map[string]any{"repo": t.TempDir(), "commands": "allow"}); r.Error == nil || !strings.Contains(r.Error.Message, "paired device") {
		t.Fatalf("a device opened a task with its own: %+v", r.Error)
	}
}
