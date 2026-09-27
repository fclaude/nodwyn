package run

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fclaude/nibb/internal/config"
	"github.com/fclaude/nibb/internal/core"
	"github.com/fclaude/nibb/internal/event"
	"github.com/fclaude/nibb/internal/llm"
	"github.com/fclaude/nibb/internal/log"
	fake "github.com/fclaude/nibb/test/fakes/ollama"
)

func TestCompletionLine(t *testing.T) {
	for _, s := range []string{CompleteLine, "done\n" + CompleteLine + "\n"} {
		if !Complete(s) {
			t.Fatalf("missed %q", s)
		}
	}
	for _, s := range []string{"", "tool returned " + CompleteLine, CompleteLine + " extra", " " + CompleteLine, CompleteLine + "\nmore"} {
		if Complete(s) {
			t.Fatalf("false completion %q", s)
		}
	}
}
func drive(t *testing.T, wall time.Duration, max int, answer string, turns ...fake.Turn) (State, []log.Event) {
	t.Helper()
	return driveConfigured(t, config.Default(), wall, max, answer, turns...)
}
func driveConfigured(t *testing.T, cfg config.Config, wall time.Duration, max int, answer string, turns ...fake.Turn) (State, []log.Event) {
	t.Helper()
	return driveIn(t, t.TempDir(), cfg, wall, max, answer, turns...)
}

// driveIn runs a driver over a session in repo, which may hold a
// .nibb/init.scm.
func driveIn(t *testing.T, repo string, cfg config.Config, wall time.Duration, max int, answer string, turns ...fake.Turn) (State, []log.Event) {
	t.Helper()
	srv := fake.New(turns...)
	defer srv.Close()
	cfg.Budget.TokensSoft = 100000
	cfg.Budget.TokensHard = 100000
	s, err := core.New(core.Options{Repo: repo, Paths: config.PathsUnder(t.TempDir(), t.TempDir()), Config: cfg, Provider: llm.NewOllama(srv.URL)})
	if err != nil {
		t.Fatal(err)
	}
	d, err := New(s, State{Task: "perform test task", Deadline: time.Now().Add(wall), MaxTurns: max})
	if err != nil {
		t.Fatal(err)
	}
	drained := make(chan struct{})
	go func() {
		defer close(drained)
		for ev := range s.Events() {
			d.Observe(ev)
			if ev.Kind == event.PromptConfirm && answer != "wait" {
				p := ev.Payload.(event.Confirm)
				_ = s.AnswerFrom(p.ID, answer, "test-operator")
			}
		}
	}()
	d.Start("test-creator")
	select {
	case <-d.done:
	case <-time.After(5 * time.Second):
		d.Stop()
		t.Error("driver did not finish")
	}
	d.Wait()
	s.Close()
	<-drained
	return d.Info(), s.EventsSoFar()
}
func TestSupervisedRun(t *testing.T) {
	state, history := drive(t, time.Minute, 3, "y", fake.Turn{Content: "1. First step."}, fake.Turn{Content: "first done"}, fake.Turn{Content: "1. Remaining step."}, fake.Turn{Content: "all done\n" + CompleteLine})
	if state.Status != "completed" || state.Turns != 2 {
		t.Fatalf("%+v", state)
	}
	plans, checks := 0, 0
	for _, ev := range history {
		switch ev.Kind {
		case log.KindGatePlan:
			plans++
		case log.KindRunCheckpoint:
			checks++
			var p log.RunCheckpoint
			ev.Decode(&p)
			if !p.Allowed || p.Actor != "test-operator" || p.PromptID == "" {
				t.Fatalf("missing checkpoint attribution: %+v", p)
			}
		case log.KindApprovalChange:
			t.Fatal("run checkpoint granted tool authority")
		}
	}
	if plans != 2 || checks != 1 {
		t.Fatalf("plans=%d checks=%d", plans, checks)
	}
	restored, err := Restore(history)
	if err != nil || restored.Turns != 2 || !restored.Deadline.Equal(state.Deadline) {
		t.Fatalf("restore %+v %v", restored, err)
	}
}
func TestRunStops(t *testing.T) {
	// A provider error is retried before it stops the run; keep that quick.
	defer func(d time.Duration) { llm.RetryDelay = d }(llm.RetryDelay)
	llm.RetryDelay = time.Millisecond
	for _, tc := range []struct {
		name, answer string
		max          int
		wall         time.Duration
		turns        []fake.Turn
		want         string
	}{
		{"decline", "n", 3, time.Minute, []fake.Turn{{Content: "1. work"}, {Content: "step"}}, "stopped"},
		{"ceiling", "y", 1, time.Minute, []fake.Turn{{Content: "1. work"}, {Content: "step"}}, "limited"},
		{"empty-plan", "y", 3, time.Minute, []fake.Turn{{Content: " "}}, "stopped"},
		{"provider-error", "y", 3, time.Minute, []fake.Turn{{Content: "1. work"}, {Error: "outage"}}, "stopped"},
		{"deadline-at-checkpoint", "wait", 3, 100 * time.Millisecond, []fake.Turn{{Content: "1. work"}, {Content: "step"}}, "stopped"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state, _ := drive(t, tc.wall, tc.max, tc.answer, tc.turns...)
			if state.Status != tc.want || state.Turns != 1 {
				t.Fatalf("%+v", state)
			}
		})
	}
}
func TestRestoreRequiresDurableBoundary(t *testing.T) {
	makeEvent := func(k log.Kind, p any) log.Event {
		data, _ := json.Marshal(p)
		return log.Event{Kind: k, Payload: data}
	}
	history := []log.Event{makeEvent(log.KindRunState, log.RunState{Task: "task", Deadline: time.Now().Add(time.Minute), MaxTurns: 4, Status: "running"}), makeEvent(log.KindRunTurn, log.RunTurn{Number: 1}), makeEvent(log.KindUserMsg, log.UserMsg{Text: "task"})}
	if _, err := Restore(history); err == nil {
		t.Fatal("resumed interrupted turn")
	}
	history = append(history, makeEvent(log.KindTurnEnd, log.TurnEnd{Text: "old event"}))
	if _, err := Restore(history); err == nil {
		t.Fatal("treated unknown outcome as safe boundary")
	}
	history[len(history)-1] = makeEvent(log.KindTurnEnd, log.TurnEnd{Outcome: "canceled"})
	state, err := Restore(history)
	if err != nil || state.Turns != 1 || state.MaxTurns != 4 {
		t.Fatalf("%+v %v", state, err)
	}
	// A later manual evaluation cannot repair an interrupted run boundary.
	interrupted := append([]log.Event(nil), history[:len(history)-1]...)
	interrupted = append(interrupted, makeEvent(log.KindEvalReq, log.EvalRequest{Source: "(+ 1 2)"}), makeEvent(log.KindTurnEnd, log.TurnEnd{Outcome: "completed"}))
	if _, err := Restore(interrupted); err == nil {
		t.Fatal("manual evaluation masked interrupted run")
	}
	state.Deadline = time.Now().Add(-time.Second)
	if err := state.Validate(); err == nil {
		t.Fatal("expired run revived")
	}
}

// Cancellation must release a blocked provider and reach a recorded boundary.
type blockedProvider struct{}

func (blockedProvider) Name() string                         { return "blocked" }
func (blockedProvider) Ping(context.Context) (string, error) { return "test", nil }
func (blockedProvider) Chat(ctx context.Context, _ llm.Request, _ func(llm.Delta)) (*llm.Response, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}
func TestRunDeadlineCancelsProvider(t *testing.T) {
	s, err := core.New(core.Options{Repo: t.TempDir(), Paths: config.PathsUnder(t.TempDir(), t.TempDir()), Config: config.Default(), Provider: blockedProvider{}})
	if err != nil {
		t.Fatal(err)
	}
	d, err := New(s, State{Task: "task", Deadline: time.Now().Add(80 * time.Millisecond), MaxTurns: 2})
	if err != nil {
		t.Fatal(err)
	}
	drained := make(chan struct{})
	go func() {
		defer close(drained)
		for ev := range s.Events() {
			d.Observe(ev)
		}
	}()
	d.Start("test")
	select {
	case <-d.done:
	case <-time.After(3 * time.Second):
		d.Stop()
		t.Fatal("deadline did not stop provider")
	}
	s.Close()
	<-drained
	if state := d.Info(); state.Status != "stopped" || !strings.Contains(state.Reason, "deadline") {
		t.Fatalf("%+v", state)
	}
	if _, err := Restore(s.EventsSoFar()); err != nil {
		t.Fatal(err)
	}
}

func TestHardTurnBudgetCanContinueOnlyAfterCheckpoint(t *testing.T) {
	cfg := config.Default()
	cfg.Budget.ToolCallsSoft = 1
	cfg.Budget.ToolCallsHard = 1
	state, history := driveConfigured(t, cfg, time.Minute, 3, "y",
		fake.Turn{Content: "1. Inspect files."},
		fake.Turn{ToolCalls: []fake.ToolCall{{ID: "a", Name: "fs-list", Args: map[string]any{"path": "."}}, {ID: "b", Name: "fs-list", Args: map[string]any{"path": "."}}}},
		fake.Turn{Content: "1. Finish."}, fake.Turn{Content: CompleteLine})
	if state.Status != "completed" || state.Turns != 2 {
		t.Fatalf("%+v", state)
	}
	hard, checkpoint := false, false
	for _, ev := range history {
		if ev.Kind == log.KindTurnEnd {
			var p log.TurnEnd
			ev.Decode(&p)
			hard = hard || p.Outcome == "hard_budget"
		}
		if ev.Kind == log.KindRunCheckpoint {
			checkpoint = true
		}
	}
	if !hard || !checkpoint {
		t.Fatalf("hard=%v checkpoint=%v", hard, checkpoint)
	}
}

// A turn's answer is the model's, not a sub-agent's: a reviewer that reads
// the answer in its own, folded, context does not replace it.
func TestRunCompletesWhenAFoldedReviewerHasTheLastWord(t *testing.T) {
	repo := t.TempDir()
	os.MkdirAll(filepath.Join(repo, ".nibb"), 0o755)
	os.WriteFile(filepath.Join(repo, ".nibb", "init.scm"), []byte(`
(define (agent-turn request)
  (plan-gate request) ; a run's turns must plan
  (let ((answer (assoc-ref (model-turn) 'content)))
    (narrow-context "You review work." "reviewer")
    (add-message (string-append "Review this answer: " answer))
    (model-turn)
    (fold-context)))`), 0o644)
	state, _ := driveIn(t, repo, config.Default(), time.Minute, 1, "y",
		fake.Turn{Content: "1. Do the task."},
		fake.Turn{Content: "task done\n" + CompleteLine},
		fake.Turn{Content: "VERDICT: done"},
	)
	if state.Status != "completed" {
		t.Fatalf("run ended %+v, want completed", state)
	}
}
