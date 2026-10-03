package run

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fclaude/nodwyn/internal/config"
	"github.com/fclaude/nodwyn/internal/core"
	"github.com/fclaude/nodwyn/internal/event"
	"github.com/fclaude/nodwyn/internal/llm"
	"github.com/fclaude/nodwyn/internal/log"
	fake "github.com/fclaude/nodwyn/test/fakes/ollama"
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
// .nodwyn/init.scm. answer is how the operator answers prompts: "y", "n",
// "wait" (never), or "expire", where the run's time runs out the moment a
// prompt appears — the deadline passing while the operator is away, without
// racing a real clock against the turns before it.
func driveIn(t *testing.T, repo string, cfg config.Config, wall time.Duration, max int, answer string, turns ...fake.Turn) (State, []log.Event) {
	t.Helper()
	// These tests pin the run's conversation turn by turn; the default loop's
	// explorer and evidence reminders (docs/scheme.md §9) are off here, ahead
	// of any init.scm the test brings, and tested in internal/core.
	init := filepath.Join(repo, ".nodwyn", "init.scm")
	own, _ := os.ReadFile(init)
	os.MkdirAll(filepath.Dir(init), 0o755)
	os.WriteFile(init, append([]byte("(define plan-explore-rounds 0)\n(define finish-reminders 0)\n"), own...), 0o644)
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
	expire := func() {}
	if answer == "expire" {
		d.cancel()
		ctx, cancel := context.WithCancelCause(context.Background())
		d.ctx, d.cancel = ctx, func() { cancel(context.Canceled) }
		expire = func() { cancel(context.DeadlineExceeded) }
	}
	drained := make(chan struct{})
	go func() {
		defer close(drained)
		for ev := range s.Events() {
			d.Observe(ev)
			if ev.Kind == event.PromptConfirm && answer == "expire" {
				expire()
			}
			if ev.Kind == event.PromptConfirm && answer != "wait" && answer != "expire" {
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

// The plan is the run's: turn 1 drafts it, turn 2 carries it with the step
// turn 1 marked, without asking the planner again, and works on the next.
func TestSupervisedRun(t *testing.T) {
	state, history := drive(t, time.Minute, 3, "y",
		fake.Turn{Content: "1. First step.\n2. Second step."},
		fake.Turn{ToolCalls: []fake.ToolCall{stepDone("a", 1)}}, fake.Turn{Content: "first done"},
		fake.Turn{ToolCalls: []fake.ToolCall{stepDone("b", 2)}}, fake.Turn{Content: "all done\n" + CompleteLine})
	if state.Status != "completed" || state.Turns != 2 {
		t.Fatalf("%+v", state)
	}
	var plans []log.GatePlan
	checks, planners := 0, 0
	for _, ev := range history {
		switch ev.Kind {
		case log.KindGatePlan:
			var p log.GatePlan
			ev.Decode(&p)
			plans = append(plans, p)
		case log.KindContextNarrow:
			var p log.ContextNarrow
			if ev.Decode(&p) == nil && p.Agent == "planner" {
				planners++
			}
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
	if len(plans) != 2 || checks != 1 || planners != 1 {
		t.Fatalf("plans=%+v checks=%d planners=%d", plans, checks, planners)
	}
	if plans[0].Noul != "required" || plans[1].Noul != "carried" || plans[1].Plan != plans[0].Plan ||
		len(plans[1].Marks) != 1 || plans[1].Marks[0] != (log.PlanStep{Step: 1, Of: 2, State: "done", Note: "did it"}) {
		t.Fatalf("plans %+v", plans)
	}
	restored, err := Restore(history)
	if err != nil || restored.Turns != 2 || !restored.Deadline.Equal(state.Deadline) {
		t.Fatalf("restore %+v %v", restored, err)
	}
}

// A person reads a supervised run's turn at the checkpoint after it, so
// that turn is reviewed; nobody reads the last turn before the run ends.
func TestRunTurnsAreReviewedWhenACheckpointFollows(t *testing.T) {
	repo := t.TempDir()
	os.MkdirAll(filepath.Join(repo, ".nodwyn"), 0o755)
	os.WriteFile(filepath.Join(repo, ".nodwyn", "init.scm"), []byte(`
(define base-agent-turn agent-turn)
(define (agent-turn request)
  (log-note (if (reviewed?) "reviewed" "unreviewed"))
  (base-agent-turn request))
`), 0o644)
	state, history := driveIn(t, repo, config.Default(), time.Minute, 2, "y",
		fake.Turn{Content: "1. One step."}, fake.Turn{Content: "first"}, fake.Turn{Content: "second"})
	if state.Status != "limited" || state.Turns != 2 {
		t.Fatalf("%+v", state)
	}
	var said []string
	for _, ev := range history {
		var n log.Note
		if ev.Kind == log.KindNote && ev.Decode(&n) == nil && strings.HasSuffix(n.Text, "reviewed") {
			said = append(said, n.Text)
		}
	}
	if strings.Join(said, " ") != "reviewed unreviewed" {
		t.Fatalf("turns were %v", said)
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
		{"deadline-at-checkpoint", "expire", 3, time.Minute, []fake.Turn{{Content: "1. work"}, {Content: "step"}}, "stopped"},
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
		fake.Turn{Content: CompleteLine}) // turn 2 carries the plan
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
	os.MkdirAll(filepath.Join(repo, ".nodwyn"), 0o755)
	os.WriteFile(filepath.Join(repo, ".nodwyn", "init.scm"), []byte(`
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

// The message each run turn sends is Scheme: a project's run-turn-prompt is
// what the model gets.
func TestRunTurnPromptIsScheme(t *testing.T) {
	repo := t.TempDir()
	os.MkdirAll(filepath.Join(repo, ".nodwyn"), 0o755)
	os.WriteFile(filepath.Join(repo, ".nodwyn", "init.scm"), []byte(`
(define (run-turn-prompt task complete-line turn max-turns)
  (string-append "Turn " (number->string turn) ": " task ". Say " complete-line " when done."))`), 0o644)
	_, history := driveIn(t, repo, config.Default(), time.Minute, 1, "y",
		fake.Turn{Content: "1. Do it."}, fake.Turn{Content: "done\n" + CompleteLine})
	for _, ev := range history {
		if ev.Kind == log.KindUserMsg {
			var p log.UserMsg
			ev.Decode(&p)
			if p.Text != "Turn 1: perform test task. Say "+CompleteLine+" when done." {
				t.Fatalf("run turn message %q", p.Text)
			}
			return
		}
	}
	t.Fatal("no run turn was sent")
}

// driveUnattended runs an unattended run (ADR 18 §5) over the fake model:
// nobody answers, so any prompt that reaches a front-end fails the test.
func driveUnattended(t *testing.T, cfg config.Config, max int, yolo bool, turns ...fake.Turn) (State, []log.Event, string) {
	t.Helper()
	repo := t.TempDir()
	init := filepath.Join(repo, ".nodwyn", "init.scm")
	os.MkdirAll(filepath.Dir(init), 0o755)
	os.WriteFile(init, []byte("(define plan-explore-rounds 0)\n(define finish-reminders 0)\n"), 0o644)
	srv := fake.New(turns...)
	defer srv.Close()
	cfg.Budget.TokensSoft = 100000
	cfg.Budget.TokensHard = 100000
	s, err := core.New(core.Options{Repo: repo, Paths: config.PathsUnder(t.TempDir(), t.TempDir()), Config: cfg, Provider: llm.NewOllama(srv.URL), Unattended: true, AutoApprove: yolo})
	if err != nil {
		t.Fatal(err)
	}
	d, err := New(s, State{Task: "perform test task", Deadline: time.Now().Add(time.Minute), MaxTurns: max, Unattended: true, AutoApprove: yolo})
	if err != nil {
		t.Fatal(err)
	}
	drained := make(chan struct{})
	go func() {
		defer close(drained)
		for ev := range s.Events() {
			d.Observe(ev)
			switch ev.Kind {
			case event.PromptConfirm, event.PromptCheckpoint, event.PromptSecret:
				t.Errorf("an unattended run asked a person: %s %+v", ev.Kind, ev.Payload)
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
	return d.Info(), s.EventsSoFar(), repo
}

func stepDone(id string, n int) fake.ToolCall {
	return fake.ToolCall{ID: id, Name: "plan-step", Args: map[string]any{"step": n, "state": "done", "note": "did it"}}
}

func checkpoints(t *testing.T, history []log.Event) []log.RunCheckpoint {
	t.Helper()
	var out []log.RunCheckpoint
	for _, ev := range history {
		if ev.Kind == log.KindRunCheckpoint {
			var p log.RunCheckpoint
			ev.Decode(&p)
			out = append(out, p)
		}
	}
	return out
}

// An unattended run goes on after a turn that marked a plan step done, and
// stops after one that marked none; nobody is asked either time.
func TestUnattendedRunContinuesOnProgressAndStopsWithout(t *testing.T) {
	state, history, _ := driveUnattended(t, config.Default(), 5, false,
		fake.Turn{Content: "1. first\n2. second"},
		fake.Turn{ToolCalls: []fake.ToolCall{stepDone("a", 1), stepDone("b", 2)}},
		fake.Turn{Content: "both done"},
		fake.Turn{Content: "1. look again"},
		fake.Turn{Content: "nothing to do"})
	if state.Status != "stopped" || state.Turns != 2 || !strings.Contains(state.Reason, "turn 2 marked no plan step done") {
		t.Fatalf("%+v", state)
	}
	cps := checkpoints(t, history)
	if len(cps) != 2 || !cps[0].Allowed || cps[0].Actor != "unattended" || cps[0].Reason != "turn 1 marked 2 plan steps done" ||
		cps[1].Allowed || cps[1].Reason != "turn 2 marked no plan step done" {
		t.Fatalf("checkpoints %+v", cps)
	}
	// Turn 1 marked every step, so turn 2 plans again, told how the last
	// plan went.
	var decisions []string
	replanned := false
	for _, ev := range history {
		var p log.GatePlan
		if ev.Kind == log.KindGatePlan && ev.Decode(&p) == nil {
			decisions = append(decisions, p.Noul)
		}
		var h log.HarnessMsg
		if ev.Kind == log.KindHarnessMsg && ev.Decode(&h) == nil &&
			strings.Contains(h.Text, "The run's previous plan, every step of it now marked:\n1. first\n2. second\n\nHow it went:\nStep 1 done: did it\nStep 2 done: did it") {
			replanned = true
		}
	}
	if strings.Join(decisions, " ") != "required required" || !replanned {
		t.Fatalf("decisions %v, replanned with progress %v", decisions, replanned)
	}
	var recorded log.RunState
	for _, ev := range history {
		if ev.Kind == log.KindRunState {
			ev.Decode(&recorded)
			break
		}
	}
	if !recorded.Unattended || recorded.AutoApprove {
		t.Fatalf("run.state %+v", recorded)
	}
	// Restore does not bring unattended back: a resume says it again.
	if restored, err := Restore(history); err != nil || restored.Unattended {
		t.Fatalf("restore %+v %v", restored, err)
	}
}

// Its tool confirms are declined without asking; with --yolo ordinary ones
// are answered yes. A write in the folder needs neither (approval.writes,
// ADR 35). Either way the run still ends on the model's word.
func TestUnattendedRunAnswersConfirmsItself(t *testing.T) {
	write := fake.ToolCall{ID: "w", Name: "fs-write", Args: map[string]any{"path": "out.txt", "content": "hello\n"}}
	command := fake.ToolCall{ID: "c", Name: "shell-exec", Args: map[string]any{"cmd": "true", "args": []any{}}}
	for _, yolo := range []bool{false, true} {
		state, history, repo := driveUnattended(t, config.Default(), 2, yolo,
			fake.Turn{Content: "1. write the file and check it"},
			fake.Turn{ToolCalls: []fake.ToolCall{write, command, stepDone("s", 1)}},
			fake.Turn{Content: "done\n" + CompleteLine})
		if state.Status != "completed" {
			t.Fatalf("yolo=%v: %+v", yolo, state)
		}
		if _, err := os.Stat(filepath.Join(repo, "out.txt")); err != nil {
			t.Fatalf("yolo=%v: the write in the folder did not go ahead: %v", yolo, err)
		}
		var answer string
		for _, ev := range history {
			var d log.GuardDecision
			if ev.Kind == log.KindGuardDecision && ev.Decode(&d) == nil && d.Prim == "shell-exec" {
				answer = d.Answer
			}
		}
		if want := map[bool]string{false: "no", true: "yes"}[yolo]; answer != want {
			t.Fatalf("yolo=%v: shell-exec answered %q", yolo, answer)
		}
	}
}

// A soft budget checkpoint goes on to the hard limit without asking, and is
// recorded as answered by the run.
func TestUnattendedRunPassesSoftBudgetCheckpoints(t *testing.T) {
	cfg := config.Default()
	cfg.Budget.ToolCallsSoft = 1
	cfg.Budget.ToolCallsHard = 10
	state, history, _ := driveUnattended(t, cfg, 1, false,
		fake.Turn{Content: "1. look"},
		fake.Turn{ToolCalls: []fake.ToolCall{{ID: "a", Name: "fs-list", Args: map[string]any{"path": "."}}, {ID: "b", Name: "fs-list", Args: map[string]any{"path": "."}}, stepDone("s", 1)}},
		fake.Turn{Content: "looked\n" + CompleteLine})
	if state.Status != "completed" {
		t.Fatalf("%+v", state)
	}
	var cp log.TurnCheckpoint
	for _, ev := range history {
		if ev.Kind == log.KindTurnCheckpoint {
			ev.Decode(&cp)
		}
	}
	if cp.Actor != "unattended" || !cp.Choice.Continue || cp.Choice.Soft != 10 {
		t.Fatalf("budget checkpoint %+v", cp)
	}
}

// A run's spend limit is the whole run's: the turn that reaches it stops,
// and no turn follows it.
func TestRunStopsAtTheSpendLimit(t *testing.T) {
	cfg := config.Default()
	input, output := 4000.0, 0.0 // about $1.20 a request from the fake
	cfg.Control.PriceInput, cfg.Control.PriceOutput = &input, &output
	cfg.Budget.CostHard = 1
	state, history := driveConfigured(t, cfg, time.Minute, 5, "y", fake.Turn{Content: "1. work"}, fake.Turn{Content: "step"}, fake.Turn{Content: "more"}, fake.Turn{Content: "more"})
	if state.Status != "limited" || state.Turns != 1 || !strings.Contains(state.Reason, "spend limit") {
		t.Fatalf("%+v", state)
	}
	var end log.TurnEnd
	for _, ev := range history {
		if ev.Kind == log.KindTurnEnd {
			ev.Decode(&end)
		}
	}
	if end.Outcome != "spend_limit" {
		t.Fatalf("turn.end %+v", end)
	}
	if restored, err := Restore(history); err != nil || restored.Status != "limited" {
		t.Fatalf("restore %+v %v", restored, err)
	}
}
