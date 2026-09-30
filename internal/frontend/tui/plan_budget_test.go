package tui

import (
	"context"
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
