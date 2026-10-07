package core

import (
	"strings"
	"testing"

	fake "github.com/fclaude/nodwyn/test/fakes/ollama"
)

// A model that sends a list parameter as a JSON string holding the list
// (qwen and glm did, five calls in the local logs) has its call run as the
// list, and the log says what was read that way (ADR 29, amended
// 2026-10-06). Before, the call came back as a type error and cost a round.
func TestAListSentAsAJSONStringRunsAsTheList(t *testing.T) {
	t.Parallel()
	r := unattendedRig(t,
		fake.Turn{ToolCalls: []fake.ToolCall{{ID: "s", Name: "shell-exec", Args: map[string]any{"cmd": "sh", "args": `["-c", "echo said-hi"]`}}}},
		fake.Turn{Content: "done"}, fake.Turn{Content: "done"}, fake.Turn{Content: "done"}, // the no-change reminders
	)
	if e := r.turn("say hi"); e != "" {
		t.Fatal(e)
	}
	log := r.logb.String()
	if strings.Contains(log, "expected list") {
		t.Fatalf("the call was refused for its shape:\n%s", log)
	}
	if !strings.Contains(log, `said-hi`) {
		t.Fatalf("the command did not run:\n%s", log)
	}
	if !strings.Contains(log, `argument \"args\" came as a JSON string holding its list`) {
		t.Fatalf("the log does not say the call was read as the plain one:\n%s", log)
	}
}
