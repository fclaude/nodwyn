package tui

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/fclaude/nibb/internal/log"
)

// Tool calls execute serially. Keep one open interval, not a map keyed by
// provider IDs: IDs can be reused, and skipped calls have results without a
// tool.call event. These records are display state, never authorization state.
type toolCallView struct {
	call      *log.ToolCallEvent
	result    *log.ToolResult
	decisions []log.GuardDecision
	ended     bool
}

func (m *model) startToolCall(call log.ToolCallEvent) {
	m.endToolInterval()
	card := &toolCallView{call: &call}
	m.transcript = append(m.transcript, entry{tool: card})
	m.activeTool = card
}

func (m *model) recordToolResult(result log.ToolResult) {
	card := m.activeTool
	if card == nil || card.call.ID != result.ID || card.call.Name != result.Name {
		// Never attach an unmatched result to a guessed call. In particular,
		// budget/progress stops record results for calls that did not start.
		m.endToolInterval()
		card = &toolCallView{}
		m.transcript = append(m.transcript, entry{tool: card})
	}
	card.result = &result
	card.ended = true
	m.activeTool = nil
}

func (m *model) endToolInterval() {
	if m.activeTool != nil {
		m.activeTool.ended = true
		m.activeTool = nil
	}
}

func (m *model) recordToolDecision(decision log.GuardDecision) bool {
	if m.activeTool == nil || m.activeTool.call.Origin != "model" || decision.Origin != "model" {
		return false
	}
	m.activeTool.decisions = append(m.activeTool.decisions, decision)
	return true
}

func (card *toolCallView) render() string {
	var out strings.Builder
	name, id, state := "", "", "awaiting result"
	if card.call != nil {
		name, id = card.call.Name, card.call.ID
	}
	if card.ended {
		state = "no result recorded"
	}
	if card.result != nil {
		name, id = card.result.Name, card.result.ID
		state = "error"
		if card.result.OK {
			state = "ok"
		}
		state += fmt.Sprintf(" · %d ms", card.result.Millis)
	}
	fmt.Fprintf(&out, "\n▸ %s · %s\n  call ID: %s\n", name, state, id)
	if card.call != nil {
		var args bytes.Buffer
		if err := json.Indent(&args, card.call.Args, "", "  "); err == nil {
			section(&out, "arguments", args.String())
		} else {
			section(&out, "arguments", string(card.call.Args))
		}
	} else {
		out.WriteString("  No matching call record; arguments unavailable.\n")
	}
	if len(card.decisions) > 0 {
		out.WriteString("  guard decisions during this call\n")
		for _, d := range card.decisions {
			text := d.Prim + " · " + d.Decision
			if d.Answer != "" {
				text += " → " + d.Answer
			}
			if d.Scope != "" {
				text += " · scope: " + d.Scope
			}
			if d.Checker != "" {
				text += " · checker: " + d.Checker
			}
			if d.Reason != "" {
				text += "\n" + d.Reason
			}
			if d.Detail != "" {
				text += "\n" + d.Detail
			}
			indent(&out, text)
		}
	}
	if card.result != nil {
		if card.result.OK {
			section(&out, "result", card.result.Value)
		} else {
			section(&out, "error", card.result.Error)
		}
	}
	return safe(out.String())
}

func section(out *strings.Builder, label, text string) {
	fmt.Fprintf(out, "  %s\n", label)
	indent(out, text)
}

func indent(out *strings.Builder, text string) {
	out.WriteString("    " + strings.ReplaceAll(text, "\n", "\n    ") + "\n")
}
