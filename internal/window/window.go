// Package window builds the model's context from the log (ADR 9). It is a
// derived view: rebuilt from the events on every request, never the other
// way round.
//
// The policy is shaped for a prefix cache on a single shared model slot:
//
//   - The system prompt carries the current task (the user message that
//     started the turn), so the ask is never trimmed away and the prefix
//     changes only when a new turn starts.
//   - Elision is sticky: every earlier request's elided set (recorded in
//     the log as llm.req.elided) stays elided. Nothing ever comes back, so
//     the prefix never changes behind the model's back.
//   - Hysteresis: above HighWater the window is trimmed, oldest first, down
//     to LowWater, then left alone until it crosses HighWater again. Between
//     trims every request is a byte-identical prefix plus a delta.
//   - Tool results larger than MaxResultTokens are shown head+tail with a
//     marker; the model re-reads what it needs.
//   - User messages are never elided; they are small and they are the ask.
//     Neither are harness messages (add-message): they are the loop's
//     instructions to the model, and it reads them as user messages.
package window

import (
	"fmt"

	"github.com/fclaude/nibb/internal/llm"
	"github.com/fclaude/nibb/internal/log"
)

// Policy is the window's tuning (config [window]).
type Policy struct {
	HighWater       int
	LowWater        int
	MaxResultTokens int
}

// Result is the built window.
type Result struct {
	Messages  []llm.Message
	Elided    []uint64 // seqs whose content is replaced by a marker (sticky)
	EstTokens int
	Trimmed   bool // this build crossed HighWater and trimmed
	Task      string
}

// Keep is how many trailing entries are never elided.
const Keep = 4

// Build assembles messages from events. system is the base system prompt;
// fixedTokens is the estimated size of everything else that rides along
// (the tool schemas).
func Build(events []log.Event, system string, fixedTokens int, pol Policy) Result {
	return buildV1(summaryWindow(events), system, fixedTokens, pol)
}

// buildV1 preserves the pre-summary recipe for exact historical request replay.
func buildV1(events []log.Event, system string, fixedTokens int, pol Policy) Result {
	type entry struct {
		seq  uint64
		msg  llm.Message
		kind string // "user" | "assistant" | "tool"
		size int
	}
	var entries []entry
	sticky := map[uint64]bool{}
	task, plan := "", ""
	for _, ev := range events {
		switch ev.Kind {
		case log.KindUserMsg:
			var p log.UserMsg
			if ev.Decode(&p) == nil {
				entries = append(entries, entry{seq: ev.Seq, kind: "user", msg: llm.Message{Role: llm.RoleUser, Content: p.Text}})
				task, plan = p.Text, ""
			}
		case log.KindHarnessMsg:
			// Shown as a user message, but not the task: the ask is still
			// the user's.
			var p log.HarnessMsg
			if ev.Decode(&p) == nil {
				entries = append(entries, entry{seq: ev.Seq, kind: "user", msg: llm.Message{Role: llm.RoleUser, Content: p.Text}})
			}
		case log.KindGatePlan:
			var p log.GatePlan
			if ev.Decode(&p) == nil && p.Plan != "" {
				plan = p.Plan // the gate's plan for the current task (P5)
			}
		case log.KindLLMResp:
			var p log.LLMResp
			if ev.Decode(&p) == nil {
				m := llm.Message{Role: llm.RoleAssistant, Content: p.Content}
				for _, tc := range p.ToolCalls {
					m.ToolCalls = append(m.ToolCalls, llm.ToolCall{ID: tc.ID, Name: tc.Name, Args: tc.Args})
				}
				entries = append(entries, entry{seq: ev.Seq, kind: "assistant", msg: m})
			}
		case log.KindToolResult:
			var p log.ToolResult
			if ev.Decode(&p) == nil {
				content := p.Value
				if !p.OK {
					content = fmt.Sprintf(`{"error":%q}`, p.Error)
				}
				content = capResult(content, pol.MaxResultTokens)
				entries = append(entries, entry{seq: ev.Seq, kind: "tool", msg: llm.Message{Role: llm.RoleTool, Content: content, ToolName: p.Name, ToolCallID: p.ID}})
			}
		case log.KindLLMReq:
			var p log.LLMReq
			if ev.Decode(&p) == nil {
				for _, s := range p.Elided {
					sticky[s] = true
				}
			}
		}
	}

	sys := system
	if task != "" {
		sys = system + "\n\nCurrent task (the user's latest request):\n" + task
	}
	if plan != "" {
		sys += "\n\nPlan for the current task (follow it; say so if you must deviate):\n" + plan
	}

	var elided []uint64
	elide := func(i int) {
		entries[i].msg.Content = fmt.Sprintf("[elided seq=%d]", entries[i].seq)
		entries[i].msg.ToolCalls = nil
		entries[i].size = estimate(entries[i].msg)
		elided = append(elided, entries[i].seq)
	}
	for i := range entries {
		entries[i].size = estimate(entries[i].msg)
		if sticky[entries[i].seq] {
			elide(i)
		}
	}
	isElided := func(i int) bool { return contains(elided, entries[i].seq) }
	total := func() int {
		n := fixedTokens + Estimate(sys)
		for _, e := range entries {
			n += e.size
		}
		return n
	}

	trimmed := false
	if total() > pol.HighWater {
		trimmed = true
		protect := len(entries) - Keep
		// Pass 1: oldest tool results. Pass 2: oldest assistant turns.
		for _, kind := range []string{"tool", "assistant"} {
			for i := 0; i < protect && total() > pol.LowWater; i++ {
				if entries[i].kind == kind && !isElided(i) {
					elide(i)
				}
			}
		}
	}

	out := Result{Elided: elided, EstTokens: total(), Trimmed: trimmed, Task: task}
	out.Messages = append(out.Messages, llm.Message{Role: llm.RoleSystem, Content: sys})
	for _, e := range entries {
		out.Messages = append(out.Messages, e.msg)
	}
	return out
}

// capResult shortens a tool result to about max tokens: two thirds head,
// one third tail, and a marker the model can act on. Deterministic, so it
// never changes between requests.
func capResult(s string, max int) string {
	if max <= 0 || Estimate(s) <= max {
		return s
	}
	headBytes := max * 3 * 2 / 3
	tailBytes := max * 3 / 3
	if headBytes+tailBytes >= len(s) {
		return s
	}
	omitted := Estimate(s[headBytes : len(s)-tailBytes])
	return s[:headBytes] + fmt.Sprintf("\n[... about %d tokens omitted; re-read the file or narrow the command for the full content ...]\n", omitted) + s[len(s)-tailBytes:]
}

func contains(xs []uint64, x uint64) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

// Estimate is the token estimate for a piece of text: bytes/3, which is
// conservative for prose and about right for code.
func Estimate(s string) int { return (len(s) + 2) / 3 }

func estimate(m llm.Message) int {
	n := Estimate(m.Content) + 6
	for _, tc := range m.ToolCalls {
		n += Estimate(tc.Name) + Estimate(string(tc.Args)) + 6
	}
	return n
}
