package core

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fclaude/nibb/internal/budget"
	"github.com/fclaude/nibb/internal/event"
	"github.com/fclaude/nibb/internal/guard"
)

func TestPendingPromptsUseCurrentStateAndRedact(t *testing.T) {
	const canary = "sk-ant-abcdefgh1234"
	s := &Session{
		prompts: map[string]pendingConfirmation{
			"p2": {checkpoint: &pendingCheckpoint{info: budget.Checkpoint{Dimension: "actions", State: budget.State{Commands: []string{"echo " + canary}}}, reply: make(chan checkpointReply, 1)}},
			"p1": {prompt: guard.Prompt{Detail: "contains " + canary}, reply: make(chan guard.Approval, 1)},
		},
		secrets: map[string]pendingSecret{"s3": {class: "test", reply: make(chan secretReply, 1)}},
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

func TestCanceledPromptsRejectRepliesBeforeCleanup(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s := &Session{
		prompts: map[string]pendingConfirmation{"p1": {ctx: ctx, prompt: guard.Prompt{Path: "notes/a"}, reply: make(chan guard.Approval, 1)}},
		secrets: map[string]pendingSecret{"s2": {ctx: ctx, reply: make(chan secretReply, 1)}},
	}
	if s.AnswerFrom("p1", "y", "test-actor") == nil {
		t.Fatal("answered canceled prompt")
	}
	if s.CreateApproval("p1", "turn", "test-actor") == nil {
		t.Fatal("granted canceled scope")
	}
	if s.SecretReplyFrom("s2", []byte("canary"), "test-actor") == nil {
		t.Fatal("answered canceled secret prompt")
	}
	if len(s.prompts["p1"].reply) != 0 || len(s.secrets["s2"].reply) != 0 {
		t.Fatal("queued canceled reply")
	}
}

func TestTurnApprovalCannotWaiveSecurityConfirmation(t *testing.T) {
	r := newRig(t, false)
	source := `(begin (fs-write "ordinary.txt" "ordinary") (fs-write "secret.txt" "sk-ant-abcdefgh1234"))`
	if err := r.s.StartEval(source, "test-actor"); err != nil {
		t.Fatal(err)
	}
	count := 0
	timeout := time.After(5 * time.Second)
	for {
		select {
		case ev := <-r.s.Events():
			switch ev.Kind {
			case event.PromptConfirm:
				p := ev.Payload.(event.Confirm)
				count++
				if count == 1 {
					if err := r.s.CreateApproval(p.ID, "turn", "test-actor"); err != nil {
						t.Fatal(err)
					}
				} else {
					if !p.Prompt.Security {
						t.Fatal("missing security confirmation")
					}
					if err := r.s.AnswerFrom(p.ID, "n", "test-actor"); err != nil {
						t.Fatal(err)
					}
				}
			case event.Status:
				if ev.Payload.(event.StatusInfo).State == "idle" {
					if count != 2 {
						t.Fatalf("got %d prompts, want ordinary then security", count)
					}
					if _, err := os.Stat(filepath.Join(r.repo, "secret.txt")); !os.IsNotExist(err) {
						t.Fatal("denied secret write ran")
					}
					if trust, _ := r.s.TurnApproval(); trust {
						t.Fatal("turn approval survived turn")
					}
					return
				}
			}
		case <-timeout:
			t.Fatal("turn did not finish")
		}
	}
}
