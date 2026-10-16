package core

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/fclaude/nodwyn/internal/budget"
	"github.com/fclaude/nodwyn/internal/config"
	"github.com/fclaude/nodwyn/internal/event"
	"github.com/fclaude/nodwyn/internal/log"
	fake "github.com/fclaude/nodwyn/test/fakes/ollama"
)

func TestTurnOutcome(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
	for _, empty := range []bool{false, true} {
		t.Run(fmt.Sprint(empty), func(t *testing.T) {
			// The explorer looks first, then the planner drafts; a run's turn
			// that ends with nothing changed is reminded twice (the default
			// evidence loop) before it ends.
			explored := fake.Turn{Content: "main.go declares package main."}
			turns := []fake.Turn{explored, {Content: "1. Read main.go."}, {Content: "done"}, {Content: "done"}, {Content: "done"}}
			if empty {
				// Asked three times, and empty every time.
				turns = []fake.Turn{explored, {Content: "   "}, {Content: ""}, {Content: "\n"}}
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

// A turn that stops short says where it had got to: the plan's marks, the
// files the file tools wrote and the last command with how it ended. A turn
// that hit a budget in the middle of go test, was stopped for polling, or met
// a 402 ended with an error line and nothing about the work
// (20261003T002506Z-f564a28c, 20261004T182412Z-747045aa; ADR 18, amended
// 2026-10-05). One that completes, or was cancelled by the person, says
// nothing.
func TestAStoppedTurnSaysWhereItStopped(t *testing.T) {
	t.Parallel()
	const plan = "1. Change: a.go Run — do it.\n2. Test: a_test.go TestRun — runs; fails today: no Run.\n3. Docs: none — nothing a person reads changes.\n4. Check: go test ./..."
	r := newRigWith(t, false, func(c *config.Config) { c.Loop.MaxToolCalls = 5 },
		fake.Turn{ToolCalls: []fake.ToolCall{{ID: "p", Name: "plan-write", Args: map[string]any{"plan": plan}}}},
		fake.Turn{ToolCalls: []fake.ToolCall{{ID: "w", Name: "fs-write", Args: map[string]any{"path": "a.go", "content": "package main\n"}}}},
		fake.Turn{ToolCalls: []fake.ToolCall{{ID: "m", Name: "plan-step", Args: map[string]any{"step": 1, "state": "done", "note": "a.go has Run"}}}},
		fake.Turn{ToolCalls: []fake.ToolCall{{ID: "t", Name: "shell-exec", Args: map[string]any{"cmd": "sh", "args": []any{"-c", "exit 3"}}}}},
		fake.Turn{ToolCalls: []fake.ToolCall{{ID: "r", Name: "fs-read", Args: map[string]any{"path": "a.go"}}}},
		fake.Turn{ToolCalls: []fake.ToolCall{{ID: "x", Name: "fs-read", Args: map[string]any{"path": "main.go"}}}},
	)
	r.s.Send("add Run")
	r.drain("y")
	end := lastTurnEnd(t, r)
	if end.Outcome != "hard_budget" && end.Outcome != "stopped" {
		t.Fatalf("outcome %q: %+v", end.Outcome, end)
	}
	for _, want := range []string{
		"plan: 1 of 4 steps marked, next is step 2 (Test: a_test.go TestRun — runs; fails today: no Run.)",
		"files written this turn: a.go",
		"last command: sh -c exit 3 (exited 3)",
	} {
		if !strings.Contains(end.Where, want) {
			t.Errorf("where lacks %q: %q", want, end.Where)
		}
	}

	done := newRig(t, true, fake.Turn{Content: "hello"})
	done.s.Send("hi")
	done.drain("n")
	if end := lastTurnEnd(t, done); end.Outcome != "completed" || end.Where != "" {
		t.Fatalf("a completed turn: %+v", end)
	}
}

// An evaluation, a policy write or a friction read writes no user message,
// so "this turn" is its own events: one that fails after a chat turn that
// wrote a file does not report the file (P2-038).
func TestAFailedEvaluationDoesNotReportTheLastChatTurn(t *testing.T) {
	t.Parallel()
	r := newRig(t, true,
		fake.Turn{ToolCalls: []fake.ToolCall{{ID: "w", Name: "fs-write", Args: map[string]any{"path": "a.txt", "content": "a"}}}},
		fake.Turn{Content: "written"},
	)
	if err := r.s.Send("write a.txt"); err != nil {
		t.Fatal(err)
	}
	r.drain("y")
	if err := r.s.StartEval(`(error "boom")`, "test"); err != nil {
		t.Fatal(err)
	}
	r.drain("y")
	end := lastTurnEnd(t, r)
	if end.Outcome != "error" {
		t.Fatalf("outcome %q: %+v", end.Outcome, end)
	}
	if strings.Contains(end.Where, "a.txt") || strings.Contains(end.Where, "files written") {
		t.Fatalf("the evaluation reports the chat turn's work: %q", end.Where)
	}
}

// lastTurnEnd is the last turn.end in the rig's log.
func lastTurnEnd(t *testing.T, r *rig) log.TurnEnd {
	t.Helper()
	var end log.TurnEnd
	found := false
	for _, ev := range r.logged() {
		if ev.Kind == log.KindTurnEnd {
			if err := ev.Decode(&end); err != nil {
				t.Fatal(err)
			}
			found = true
		}
	}
	if !found {
		t.Fatal("no turn.end in the log")
	}
	return end
}
