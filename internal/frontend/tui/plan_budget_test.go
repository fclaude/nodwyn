package tui

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/fclaude/nodwyn/internal/budget"
	"github.com/fclaude/nodwyn/internal/config"
	"github.com/fclaude/nodwyn/internal/event"
	"github.com/fclaude/nodwyn/internal/llm"
	"github.com/fclaude/nodwyn/internal/log"
	fake "github.com/fclaude/nodwyn/test/fakes/ollama"
)

func historyRecord(seq uint64, kind log.Kind, payload any) log.Event {
	e := wire(string(kind), payload, seq)
	return log.Event{Seq: seq, Kind: kind, Payload: e.Payload}
}

func TestPlanViewSeparatesGateOutcomesAndMessages(t *testing.T) {
	for _, tc := range []struct {
		gate log.GatePlan
		want string
		omit string
	}{
		{log.GatePlan{Noul: "plan", Score: .8, Revisions: 2, Plan: "Read then edit"}, "Recorded plan score: 0.80 · revisions: 2", "Need-plan probability"},
		{log.GatePlan{Noul: "proceed", Score: .2}, "Need-plan probability: 0.20", "Recorded plan score"},
		{log.GatePlan{Noul: "degraded", Plan: "Fallback plan", Reason: "judge failed"}, "Gate fallback; no plan score recorded", "score: 0.00"},
	} {
		events := []log.Event{historyRecord(1, log.KindUserMsg, log.UserMsg{Text: "request"}), historyRecord(2, log.KindGatePlan, tc.gate)}
		text, err := formatPlan(events)
		if err != nil || !strings.Contains(text, tc.want) || strings.Contains(text, tc.omit) {
			t.Fatalf("wrong gate semantics: %s (%v)", text, err)
		}
		events = append(events, historyRecord(3, log.KindUserMsg, log.UserMsg{Text: "new request"}))
		text, err = formatPlan(events)
		if err != nil || !strings.Contains(text, "No planning outcome recorded") || strings.Contains(text, tc.want) {
			t.Fatal("prior message's plan carried into new request")
		}
	}
}

func TestBudgetViewUsesLatestTurnCheckpointsNotLiveUsage(t *testing.T) {
	limits, _ := config.Default().Budget.Limits(3)
	record := log.TurnCheckpoint{Checkpoint: budget.Checkpoint{Dimension: "wall", Used: int64(time.Minute), Limit: budget.Limit{Soft: int64(time.Minute), Hard: int64(time.Hour)}, State: budget.State{ToolCalls: 2, Tokens: 123, Actions: 1, Elapsed: time.Minute, Files: []string{"note"}}}, Choice: budget.Choice{Continue: true, Soft: int64(2 * time.Minute)}}
	events := []log.Event{historyRecord(1, log.KindUserMsg, log.UserMsg{Text: "first"}), historyRecord(2, log.KindTurnCheckpoint, record), historyRecord(3, log.KindTurnEnd, log.Note{Text: "done"})}
	text, err := formatBudgets(limits, events)
	for _, want := range []string{"tool_calls · soft 3 · hard 3", "tokens: 123", "elapsed: 1m0s", "continue requested · new soft limit 2m0s", "Turn end is recorded", "not live counters"} {
		if err != nil || !strings.Contains(text, want) {
			t.Fatalf("budget view missing %q: %s (%v)", want, text, err)
		}
	}
	events = append(events, historyRecord(4, log.KindEvalReq, log.EvalRequest{Source: "(+ 1 2)"}))
	text, err = formatBudgets(limits, events)
	if err != nil || strings.Contains(text, "tokens: 123") || strings.Contains(text, "Turn end is recorded") || !strings.Contains(text, "usage is unavailable here") {
		t.Fatal("prior turn checkpoint appeared as current usage")
	}
	record.Error = "context canceled"
	if got := checkpointDecision(record); got != "error: context canceled" {
		t.Fatal("error presented as an applied continuation", got)
	}
	record.Error = ""
	record.Choice = budget.Choice{}
	if checkpointDecision(record) != "stop" {
		t.Fatal("stop choice missing")
	}
}

func TestPlanAndBudgetInspectorsRejectMalformedRecords(t *testing.T) {
	limits, _ := config.Default().Budget.Limits(0)
	events := []log.Event{historyRecord(1, log.KindUserMsg, log.UserMsg{Text: "request"}), historyRecord(2, log.KindGatePlan, "invalid")}
	if _, err := formatPlan(events); err == nil {
		t.Fatal("malformed plan accepted")
	}
	events[1] = historyRecord(2, log.KindTurnCheckpoint, "invalid")
	if _, err := formatBudgets(limits, events); err == nil {
		t.Fatal("malformed checkpoint accepted")
	}
	for _, command := range []string{":plan extra", ":budget extra"} {
		if _, _, err := inspectorArgs(strings.Fields(command)); err == nil {
			t.Fatal("inspector ignored extra arguments")
		}
	}
}

func TestBudgetInspectionAndCheckpointThroughBothBackends(t *testing.T) {
	for _, mode := range []string{"local", "rpc"} {
		t.Run(mode, func(t *testing.T) {
			o := testOptions(t)
			o.Config.Budget.ToolCallsSoft = 1
			o.Config.Loop.MaxToolCalls = 3
			server := fake.New(fake.Turn{ToolCalls: []fake.ToolCall{{ID: "one", Name: "fs-list", Args: map[string]any{"path": "."}}, {ID: "two", Name: "fs-list", Args: map[string]any{"path": "."}}}})
			defer server.Close()
			o.Provider = llm.NewOllama(server.URL)
			var b backend
			if mode == "local" {
				b = newLocal(o)
			} else {
				b, _ = testRemote(t, o)
			}
			defer b.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			snap, err := b.Attach(ctx, o.Repo, "")
			if err != nil {
				t.Fatal(err)
			}
			m := newModel(ctx, b, o.Repo, snap.ID, false)
			m.restore(snap)
			before, err := b.History(ctx, snap.ID)
			if err != nil {
				t.Fatal(err)
			}
			m.Update(m.inspect(":budget")())
			if !strings.Contains(m.inspector.content, "tool_calls · soft 1 · hard 3") {
				t.Fatal(m.inspector.content)
			}
			m.Update(m.inspect(":plan")())
			if !strings.Contains(m.inspector.content, "No user message recorded") {
				t.Fatal(m.inspector.content)
			}
			after, err := b.History(ctx, snap.ID)
			if err != nil || len(before) != len(after) {
				t.Fatal("inspection changed history", err)
			}
			if err := b.Send(ctx, snap.ID, "list twice", false); err != nil {
				t.Fatal(err)
			}
			completed, prompted := false, false
			for {
				ev, err := b.Next(ctx)
				if err != nil {
					t.Fatal(err)
				}
				m.consume(ev)
				if ev.Kind == event.PromptCheckpoint {
					prompted = true
					if m.prompt == nil || !strings.Contains(m.prompt.detail, "Tool calls: 1") || !strings.Contains(m.prompt.detail, "tokens:") {
						t.Fatal("checkpoint usage missing or inspector hid prompt")
					}
					p, _ := decode[event.Checkpoint](ev.Payload)
					if err := b.Answer(ctx, snap.ID, p.ID, "n"); err != nil {
						t.Fatal(err)
					}
				}
				if ev.Kind == string(log.KindTurnEnd) {
					completed = true
				}
				if completed && ev.Kind == event.Status && !m.busy {
					break
				}
			}
			m.Update(m.inspect(":budget")())
			if !prompted || !strings.Contains(m.inspector.content, "Decision: stop") || !strings.Contains(m.inspector.content, "Turn end is recorded") {
				t.Fatal(m.inspector.content)
			}
			m.Update(m.inspect(":plan")())
			if !strings.Contains(m.inspector.content, "No planning outcome recorded for this message") {
				t.Fatal("missing gate was presented as planning success")
			}
		})
	}
}

func TestPlanAndCheckpointTextIsSanitizedForDisplay(t *testing.T) {
	const canary = "sk-ant-abcdefgh1234"
	limits, _ := config.Default().Budget.Limits(0)
	events := []log.Event{
		historyRecord(1, log.KindUserMsg, log.UserMsg{Text: canary}),
		historyRecord(2, log.KindGatePlan, log.GatePlan{Noul: "degraded", Plan: canary + "\x1b[2Jplan", Reason: canary}),
		historyRecord(3, log.KindTurnCheckpoint, log.TurnCheckpoint{Checkpoint: budget.Checkpoint{Dimension: "wall", State: budget.State{Commands: []string{canary + "\x1b[2Jcommand"}}}, Error: canary}),
	}
	plan, err := formatPlan(events)
	if err != nil {
		t.Fatal(err)
	}
	budgets, err := formatBudgets(limits, events)
	if err != nil {
		t.Fatal(err)
	}
	for _, content := range []string{plan, budgets} {
		m := testModel()
		m.inspector = &inspector{token: 1}
		m.Update(inspectResult{token: 1, generation: m.generation, content: content})
		if strings.Contains(m.inspector.content, canary) || strings.Contains(m.inspector.content, "\x1b") {
			t.Fatal("unsafe recorded text in inspector")
		}
	}
}

func TestPlanViewShowsStepMarks(t *testing.T) {
	gate := log.GatePlan{Noul: "required", Plan: "1. read\n2. edit\n3. test", Score: 0.8}
	marks := map[int]log.PlanStep{1: {Step: 1, Of: 3, State: "done", Note: "read main.go"}, 3: {Step: 3, Of: 3, State: "skipped"}}
	got := formatGatePlan(gate) + formatSteps(gate, marks)
	for _, want := range []string{"Required plan score: 0.80", "Steps (1 of 3 done, as the model marked them):", "[x] 1. read (read main.go)", "[ ] 2. edit", "[-] 3. test"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in:\n%s", want, got)
		}
	}
	if formatSteps(gate, nil) != "" {
		t.Fatal("no marks, no step list")
	}
}

// A run's later turn shows its carried plan with the marks of the run's
// earlier turns, and the marks made since over them.
func TestPlanViewShowsACarriedPlansMarks(t *testing.T) {
	gate := log.GatePlan{Noul: "carried", Plan: "1. read\n2. edit\n3. test", Marks: []log.PlanStep{{Step: 1, Of: 3, State: "done", Note: "read main.go"}, {Step: 2, Of: 3, State: "skipped"}}}
	got, err := formatPlan([]log.Event{
		historyRecord(1, log.KindUserMsg, log.UserMsg{Text: "run turn 2"}),
		historyRecord(2, log.KindGatePlan, gate),
		historyRecord(3, log.KindPlanStep, log.PlanStep{Step: 2, Of: 3, State: "done", Note: "edited"}),
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"The run's plan, carried from an earlier turn · 2 of 3 steps marked there", "Steps (2 of 3 done, as the model marked them):", "[x] 1. read (read main.go)", "[x] 2. edit (edited)", "[ ] 3. test"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in:\n%s", want, got)
		}
	}
}

func llmResp(t *testing.T, seq uint64, u log.Usage) log.Event {
	t.Helper()
	raw, _ := json.Marshal(log.LLMResp{Content: "x", Usage: u})
	return log.Event{Seq: seq, Kind: log.KindLLMResp, Payload: raw}
}

func TestBudgetViewSpend(t *testing.T) {
	user, _ := json.Marshal(log.UserMsg{Text: "do it"})
	start := log.Event{Seq: 1, Kind: log.KindUserMsg, Payload: user}
	c1, c2 := 0.01, 0.02
	cached := 750
	known := []log.Event{start, llmResp(t, 2, log.Usage{PromptTokens: 1000, OutputTokens: 10, CachedTokens: &cached, Cost: &c1}), llmResp(t, 3, log.Usage{PromptTokens: 1000, OutputTokens: 10, CachedTokens: &cached, Cost: &c2})}
	got, err := formatBudgets(budget.Limits{}, known)
	if err != nil || !strings.Contains(got, "Spend this turn: $0.030 (2 requests, 75% of prompt tokens from cache)") {
		t.Fatalf("known:\n%s %v", got, err)
	}
	unknown := []log.Event{start, llmResp(t, 2, log.Usage{PromptTokens: 1000, OutputTokens: 10, TotalMS: 900})}
	if got, _ := formatBudgets(budget.Limits{}, unknown); !strings.Contains(got, "Spend unknown: 1 requests, and the provider reports no cost and no prices are configured") {
		t.Fatalf("unknown:\n%s", got)
	}
	// An older log's llm.resp has no cost fields: unknown, never $0.
	oldResp := log.Event{Seq: 2, Kind: log.KindLLMResp, Payload: json.RawMessage(`{"content":"x","usage":{"prompt_tokens":5,"output_tokens":1,"prompt_eval_ms":0,"eval_ms":0,"total_ms":0,"load_ms":0}}`)}
	if got, _ := formatBudgets(budget.Limits{}, []log.Event{start, oldResp}); strings.Contains(got, "$0") || !strings.Contains(got, "Spend unknown") {
		t.Fatalf("old log:\n%s", got)
	}
}
