package core

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/fclaude/nibb/internal/budget"
	"github.com/fclaude/nibb/internal/event"
	"github.com/fclaude/nibb/internal/guard"
)

func TestPendingPromptsUseCurrentStateAndRedact(t *testing.T) {
	const canary = "sk-ant-abcdefgh1234"
	s := &Session{
		prompts: map[string]pendingConfirmation{
			"p2": {checkpoint: &pendingCheckpoint{info: budget.Checkpoint{Dimension: "actions", State: budget.State{Commands: []string{"echo " + canary}}}, reply: make(chan budget.Choice, 1)}},
			"p1": {prompt: guard.Prompt{Detail: "contains " + canary}, reply: make(chan bool, 1)},
		},
		secrets: map[string]pendingSecret{"s3": {class: "test", reply: make(chan []byte, 1)}},
	}
	pending, err := s.PendingPrompts()
	if err != nil || len(pending) != 3 {
		t.Fatal(pending, err)
	}
	if pending[0].Kind != event.PromptConfirm || pending[1].Kind != event.PromptCheckpoint || pending[2].Kind != event.PromptSecret {
		t.Fatal("unstable prompt order", pending)
	}
	raw, err := json.Marshal(pending)
	if err != nil || strings.Contains(string(raw), canary) {
		t.Fatal("unsafe snapshot", err)
	}
	// Snapshots must not alias mutable budget slices in the core's pending state.
	pending[1].Payload.(event.Checkpoint).Checkpoint.State.Commands[0] = "changed"
	if s.prompts["p2"].checkpoint.info.State.Commands[0] != "echo "+canary {
		t.Fatal("snapshot mutated core")
	}
	if err := s.Answer("p1", "n"); err != nil {
		t.Fatal(err)
	}
	if err := s.Answer("p2", "n"); err != nil {
		t.Fatal(err)
	}
	if err := s.SecretReply("s3", nil); err != nil {
		t.Fatal(err)
	}
	pending, err = s.PendingPrompts()
	if err != nil || len(pending) != 0 {
		t.Fatal("resolved prompt survived", pending, err)
	}
}
