package core

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/fclaude/nodwyn/internal/budget"
	"github.com/fclaude/nodwyn/internal/event"
	"github.com/fclaude/nodwyn/internal/log"
	fake "github.com/fclaude/nodwyn/test/fakes/ollama"
)

func TestTurnOutcome(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want string
	}{
		{"success", nil, "completed"},
		{"hard", fmt.Errorf("wrapped: %w", &budget.Stop{Hard: true, Reason: "hard limit"}), "hard_budget"},
		{"declined", &budget.Stop{Reason: "checkpoint declined"}, "stopped"},
		{"cancel", context.Canceled, "canceled"},
		{"failure", errors.New("provider failed"), "error"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := turnOutcome(context.Background(), tc.err)
			if got.Outcome != tc.want {
				t.Fatalf("got %+v", got)
			}
			if tc.err != nil && got.Error != tc.err.Error() {
				t.Fatalf("lost cause: %+v", got)
			}
		})
	}
	ctx, cancel := context.WithCancelCause(context.Background())
	cancel(&budget.Stop{Hard: true, Reason: "wall ceiling"})
	if got := turnOutcome(ctx, context.Canceled); got.Outcome != "hard_budget" || got.Error != "wall ceiling" {
		t.Fatalf("lost deadline cause: %+v", got)
	}
}

func TestRequiredPlanPrecedesExecution(t *testing.T) {
	for _, empty := range []bool{false, true} {
		t.Run(fmt.Sprint(empty), func(t *testing.T) {
			turns := []fake.Turn{{Content: "1. Read main.go."}, {Content: "done"}}
			if empty {
				// Asked three times, and empty every time.
				turns = []fake.Turn{{Content: "   "}, {Content: ""}, {Content: "\n"}}
			}
			r := newRig(t, true, turns...)
			if err := r.s.SendPlanned("read main.go"); err != nil {
				t.Fatal(err)
			}
			events := r.drain("n")
			planned, responded := false, false
			outcome, depth := "", 0 // the planner drafts in a narrowed context of its own
			for _, ev := range events {
				switch ev.Kind {
				case string(log.KindGatePlan):
					planned = true
				case string(log.KindContextNarrow):
					depth++
				case string(log.KindContextFold):
					depth--
				case string(log.KindLLMResp):
					if depth > 0 {
						continue
					}
					if !planned {
						t.Fatal("execution before plan")
					}
					responded = true
				case string(log.KindTurnEnd):
					outcome = ev.Payload.(log.TurnEnd).Outcome
				case event.PromptConfirm:
					t.Fatal("unexpected permission")
				}
			}
			if empty {
				if responded || outcome != "error" {
					t.Fatalf("empty plan executed: %v %s", responded, outcome)
				}
			} else if !responded || outcome != "completed" {
				t.Fatalf("planned turn did not finish: %v %s", responded, outcome)
			}
		})
	}
}
