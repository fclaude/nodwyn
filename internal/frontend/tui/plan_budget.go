package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/fclaude/nodwyn/internal/budget"
	"github.com/fclaude/nodwyn/internal/log"
)

func formatPlan(events []log.Event) (string, error) {
	start := -1
	for i, e := range events {
		if e.Kind == log.KindUserMsg {
			start = i
		}
	}
	if start < 0 {
		return "No user message recorded in this snapshot.", nil
	}
	var request log.UserMsg
	if err := events[start].Decode(&request); err != nil {
		return "", err
	}
	heading := fmt.Sprintf("Latest user message · #%d\n%s\n\n", events[start].Seq, request.Text)
	for i := len(events) - 1; i > start; i-- {
		if events[i].Kind == log.KindGatePlan {
			var gate log.GatePlan
			if err := events[i].Decode(&gate); err != nil {
				return "", err
			}
			marks := map[int]log.PlanStep{}
			for _, e := range events[i+1:] {
				var m log.PlanStep
				if e.Kind == log.KindPlanStep && e.Decode(&m) == nil {
					marks[m.Step] = m
				}
			}
			return heading + fmt.Sprintf("Recorded outcome · #%d\n", events[i].Seq) + formatGatePlan(gate) + formatSteps(gate, marks), nil
		}
	}
	return heading + "No planning outcome recorded for this message in this snapshot.", nil
}

func formatGatePlan(gate log.GatePlan) string {
	var out strings.Builder
	fmt.Fprintf(&out, "Planning gate: %s\n", gate.Noul)
	switch gate.Noul {
	case "proceed":
		fmt.Fprintf(&out, "No plan requested by the gate.\nNeed-plan probability: %.2f\n", gate.Score)
	case "plan":
		fmt.Fprintf(&out, "Recorded plan score: %.2f · revisions: %d\n", gate.Score, gate.Revisions)
	case "required":
		fmt.Fprintf(&out, "Required plan score: %.2f · revisions: %d\n", gate.Score, gate.Revisions)
	case "degraded":
		out.WriteString("Gate fallback; no plan score recorded.\n")
	}
	if gate.Reason != "" {
		fmt.Fprintf(&out, "Reason: %s\n", gate.Reason)
	}
	if gate.Plan != "" {
		fmt.Fprintf(&out, "\n%s\n", gate.Plan)
	}
	return out.String()
}

// formatSteps lists the plan's steps with the model's marks (plan.step),
// once it has made any.
func formatSteps(gate log.GatePlan, marks map[int]log.PlanStep) string {
	steps := gate.Steps()
	if len(marks) == 0 || len(steps) == 0 {
		return ""
	}
	var out strings.Builder
	done := 0
	for _, m := range marks {
		if m.State == "done" {
			done++
		}
	}
	fmt.Fprintf(&out, "\nSteps (%d of %d done, as the model marked them):\n", done, len(steps))
	for i, s := range steps {
		box := "[ ]"
		m, ok := marks[i+1]
		switch {
		case ok && m.State == "done":
			box = "[x]"
		case ok:
			box = "[-]"
		}
		fmt.Fprintf(&out, "%s %d. %s", box, i+1, s)
		if ok && m.Note != "" {
			fmt.Fprintf(&out, " (%s)", m.Note)
		}
		out.WriteString("\n")
	}
	return out.String()
}

func formatBudgets(limits budget.Limits, events []log.Event) (string, error) {
	var out strings.Builder
	out.WriteString("Configured per-turn limits · current session lifetime\nInitial soft limits; checkpoint choices apply only within their turn.\n\n")
	for _, row := range []struct {
		name  string
		limit budget.Limit
	}{{"tool_calls", limits.ToolCalls}, {"tokens", limits.Tokens}, {"actions", limits.Actions}, {"wall", limits.Wall}} {
		fmt.Fprintf(&out, "%s · soft %s · hard %s\n", row.name, budgetAmount(row.name, row.limit.Soft), budgetAmount(row.name, row.limit.Hard))
	}
	out.WriteString("\nUsage below is recorded at checkpoints, not live counters or remaining allowance.\n")
	start := -1
	for i, e := range events {
		if e.Kind == log.KindUserMsg || e.Kind == log.KindEvalReq {
			start = i
		}
	}
	if start < 0 {
		out.WriteString("\nNo submitted turn recorded in this snapshot.\n")
		return out.String(), nil
	}
	fmt.Fprintf(&out, "\nLatest submitted turn · #%d (%s)\n", events[start].Seq, events[start].Kind)
	count, ended := 0, false
	for _, e := range events[start+1:] {
		if e.Kind == log.KindTurnEnd {
			ended = true
		}
		if e.Kind != log.KindTurnCheckpoint {
			continue
		}
		var record log.TurnCheckpoint
		if err := e.Decode(&record); err != nil {
			return "", err
		}
		p := record.Checkpoint
		count++
		fmt.Fprintf(&out, "\nCheckpoint #%d · %s\nUsed %s · soft %s · hard %s\n%s\nDecision: %s\n", e.Seq, p.Dimension, budgetAmount(p.Dimension, p.Used), budgetAmount(p.Dimension, p.Limit.Soft), budgetAmount(p.Dimension, p.Limit.Hard), formatBudgetState(p.State), checkpointDecision(record))
		if record.Actor != "" {
			fmt.Fprintf(&out, "Actor: %s\n", record.Actor)
		}
	}
	if count == 0 {
		out.WriteString("No checkpoint recorded for this turn; usage is unavailable here.\n")
	}
	if ended {
		out.WriteString("\nTurn end is recorded.\n")
	} else {
		out.WriteString("\nNo turn end recorded in this snapshot.\n")
	}
	return out.String(), nil
}

func formatBudgetState(state budget.State) string {
	return fmt.Sprintf("Tool calls: %d · tokens: %d · actions: %d · elapsed: %s\nFiles: %s\nCommands: %s", state.ToolCalls, state.Tokens, state.Actions, state.Elapsed.Round(time.Millisecond), strings.Join(state.Files, ", "), strings.Join(state.Commands, ", "))
}

func checkpointDecision(record log.TurnCheckpoint) string {
	if record.Error != "" {
		return "error: " + record.Error
	}
	if !record.Choice.Continue {
		return "stop"
	}
	return "continue requested · new soft limit " + budgetAmount(record.Checkpoint.Dimension, record.Choice.Soft)
}
