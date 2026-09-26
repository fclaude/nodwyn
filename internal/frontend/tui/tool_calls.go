package tui

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
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

// render draws a call as the one line a reader scans: what ran, on what,
// how the guard judged it, and what came back. details adds the arguments,
// the guard's reasoning and the result body underneath — a 13 kB file read
// belongs behind a toggle, not in the middle of a conversation.
func (card *toolCallView) render(details bool) string {
	name, id := "", ""
	if card.call != nil {
		name, id = card.call.Name, card.call.ID
	}
	if card.result != nil {
		name, id = card.result.Name, card.result.ID
	}
	line := "⎿ " + name
	if card.call != nil {
		if summary := argSummary(card.call.Args); summary != "" {
			line += "  " + summary
		}
	}
	if verdict := card.verdict(); verdict != "" {
		line += "  [" + verdict + "]"
	}
	line += "  " + card.state()
	if !details {
		return safe(line)
	}

	var out strings.Builder
	out.WriteString(line + "\n")
	if id != "" {
		fmt.Fprintf(&out, "  call ID: %s\n", id)
	}
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

// state is the right-hand end of the compact line.
func (card *toolCallView) state() string {
	if card.result == nil {
		if card.ended {
			return "no result recorded"
		}
		return "awaiting result"
	}
	if !card.result.OK {
		return fmt.Sprintf("✕ error · %d ms", card.result.Millis)
	}
	return fmt.Sprintf("✓ %s · %d ms", byteSize(len(card.result.Value)), card.result.Millis)
}

// verdict is the last guard decision recorded against this call, which is
// the one that let it run or stopped it.
func (card *toolCallView) verdict() string {
	if len(card.decisions) == 0 {
		return ""
	}
	last := card.decisions[len(card.decisions)-1]
	out := last.Decision
	if last.Answer != "" {
		out += "→" + last.Answer
	}
	return out
}

func byteSize(n int) string {
	switch {
	case n < 1024:
		return fmt.Sprintf("%d B", n)
	case n < 1024*1024:
		return fmt.Sprintf("%.1f kB", float64(n)/1024)
	default:
		return fmt.Sprintf("%.1f MB", float64(n)/(1024*1024))
	}
}

// argSummary renders a call's arguments as one line. What identifies a call
// is its target, so those keys lead; once a target is named, a long prose
// argument says nothing a scanning reader needs, and the details view still
// has every argument in full.
var targetKeys = []string{"path", "file", "dir", "pkg", "target", "url", "cmd", "name", "glob", "pattern"}

func argSummary(raw json.RawMessage) string {
	var fields map[string]any
	if len(raw) == 0 || json.Unmarshal(raw, &fields) != nil {
		return oneLine(string(raw))
	}
	rank := func(key string) int {
		for i, k := range targetKeys {
			if k == key {
				return i
			}
		}
		return len(targetKeys)
	}
	keys := make([]string, 0, len(fields))
	named := false
	for key := range fields {
		keys = append(keys, key)
		if rank(key) < len(targetKeys) {
			named = true
		}
	}
	sort.SliceStable(keys, func(i, j int) bool {
		if a, b := rank(keys[i]), rank(keys[j]); a != b {
			return a < b
		}
		return keys[i] < keys[j]
	})
	var parts []string
	for _, key := range keys {
		value := ""
		if s, ok := fields[key].(string); ok {
			value = s
		} else if encoded, err := json.Marshal(fields[key]); err == nil {
			value = string(encoded)
		}
		if value == "" || value == "[]" || value == "{}" || value == "null" {
			continue
		}
		if named && rank(key) == len(targetKeys) && len(value) > 48 {
			continue
		}
		parts = append(parts, value)
	}
	return oneLine(strings.Join(parts, " "))
}

func oneLine(value string) string {
	flat := strings.Join(strings.Fields(value), " ")
	if len(flat) > 140 {
		return flat[:139] + "…"
	}
	return flat
}

func section(out *strings.Builder, label, text string) {
	fmt.Fprintf(out, "  %s\n", label)
	indent(out, text)
}

func indent(out *strings.Builder, text string) {
	out.WriteString("    " + strings.ReplaceAll(text, "\n", "\n    ") + "\n")
}
