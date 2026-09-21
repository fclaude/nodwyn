// Package window builds the model's context from the log (ADR 9). It is a
// derived view: rebuilt from the events on every turn, never the other way
// round. The bootstrap policy is deliberately naive — elide the oldest tool
// results first, then the oldest turns, always in place so the request
// prefix stays byte-stable and the provider's KV cache keeps hitting.
package window

import (
	"fmt"

	"github.com/fclaude/nibb/internal/llm"
	"github.com/fclaude/nibb/internal/log"
)

// Result is the built window.
type Result struct {
	Messages  []llm.Message
	Elided    []uint64 // seqs whose content was replaced by a marker
	EstTokens int
}

// Keep is how many trailing entries are never elided.
const Keep = 4

// Build assembles messages from events under a token budget. system is the
// system prompt; fixedTokens is the estimated size of everything else that
// rides along (the tool schemas).
func Build(events []log.Event, system string, fixedTokens, budget int) Result {
	type entry struct {
		seq  uint64
		msg  llm.Message
		tool bool
		size int
	}
	var entries []entry
	for _, ev := range events {
		switch ev.Kind {
		case log.KindUserMsg:
			var p log.UserMsg
			if ev.Decode(&p) == nil {
				entries = append(entries, entry{seq: ev.Seq, msg: llm.Message{Role: llm.RoleUser, Content: p.Text}})
			}
		case log.KindLLMResp:
			var p log.LLMResp
			if ev.Decode(&p) == nil {
				m := llm.Message{Role: llm.RoleAssistant, Content: p.Content}
				for _, tc := range p.ToolCalls {
					m.ToolCalls = append(m.ToolCalls, llm.ToolCall{ID: tc.ID, Name: tc.Name, Args: tc.Args})
				}
				entries = append(entries, entry{seq: ev.Seq, msg: m})
			}
		case log.KindToolResult:
			var p log.ToolResult
			if ev.Decode(&p) == nil {
				content := p.Value
				if !p.OK {
					content = fmt.Sprintf(`{"error":%q}`, p.Error)
				}
				entries = append(entries, entry{seq: ev.Seq, tool: true, msg: llm.Message{Role: llm.RoleTool, Content: content, ToolName: p.Name, ToolCallID: p.ID}})
			}
		}
	}
	for i := range entries {
		entries[i].size = estimate(entries[i].msg)
	}
	total := func() int {
		n := fixedTokens + Estimate(system)
		for _, e := range entries {
			n += e.size
		}
		return n
	}
	var elided []uint64
	elide := func(i int) {
		entries[i].msg.Content = fmt.Sprintf("[elided seq=%d]", entries[i].seq)
		entries[i].size = estimate(entries[i].msg)
		elided = append(elided, entries[i].seq)
	}
	isElided := func(i int) bool {
		for _, s := range elided {
			if s == entries[i].seq {
				return true
			}
		}
		return false
	}
	protect := len(entries) - Keep
	// Pass 1: oldest tool results.
	for i := 0; i < protect && total() > budget; i++ {
		if entries[i].tool && !isElided(i) {
			elide(i)
		}
	}
	// Pass 2: oldest turns of any kind.
	for i := 0; i < protect && total() > budget; i++ {
		if !isElided(i) {
			elide(i)
		}
	}
	out := Result{Elided: elided, EstTokens: total()}
	out.Messages = append(out.Messages, llm.Message{Role: llm.RoleSystem, Content: system})
	for _, e := range entries {
		out.Messages = append(out.Messages, e.msg)
	}
	return out
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
