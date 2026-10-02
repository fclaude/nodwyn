package tui

import (
	"encoding/json"
	"fmt"

	"github.com/fclaude/nodwyn/internal/frontend/plain"
	"github.com/fclaude/nodwyn/internal/log"
)

// contextGauge follows how big the conversation's context is, from the log:
// the window's estimate of the latest control-model request made in the
// conversation itself, not in a planner's or sub-agent's narrowed context,
// what the provider counted for that request's prompt, the window's high
// water from the session's start, and the compactions so far.
type contextGauge struct {
	depth       int  // narrowed contexts open
	await       bool // the next llm.resp answers the request being followed
	est         int
	reported    int // 0: not reported (yet)
	high        int
	compactions int
	last        *log.ContextCompact
}

func (g *contextGauge) observe(kind string, raw json.RawMessage) {
	switch log.Kind(kind) {
	case log.KindSessionStart, log.KindSessionResume:
		if p, err := decode[log.SessionStart](raw); err == nil && p.Context != nil {
			g.high = p.Context.HighWater
		}
	case log.KindUserMsg, log.KindContextWiden:
		g.depth = 0 // a narrowed context ends with the turn
	case log.KindContextNarrow:
		if p, err := decode[log.ContextNarrow](raw); err == nil && !p.Lasting {
			g.depth++
		}
	case log.KindContextFold:
		g.depth = max(0, g.depth-1)
	case log.KindLLMReq:
		p, err := decode[log.LLMReq](raw)
		g.await = err == nil && g.depth == 0 && p.Endpoint == ""
		if g.await {
			g.est, g.reported = p.EstTokens, 0
		}
	case log.KindLLMResp:
		if g.await {
			if p, err := decode[log.LLMResp](raw); err == nil {
				g.reported = p.Usage.PromptTokens
			}
			g.await = false
		}
	case log.KindContextCompact:
		if p, err := decode[log.ContextCompact](raw); err == nil {
			g.compactions++
			g.last = &p
			if p.Compacted {
				g.est, g.reported = p.After, 0
			}
		}
	}
}

// title is the gauge for the title bar: "context ~45k of ~61k".
func (g *contextGauge) title() string {
	if g.est == 0 {
		return ""
	}
	s := "context ~" + kilo(g.est)
	if g.high > 0 {
		s += " of ~" + kilo(g.high)
	}
	return s
}

// report is the gauge for the budget inspector.
func (g *contextGauge) report() string {
	if g.est == 0 {
		return "Context: no model request yet.\n"
	}
	s := fmt.Sprintf("Context: ~%s tokens as nodwyn estimates the latest request", plain.Thousands(int64(g.est)))
	if g.reported > 0 {
		s += fmt.Sprintf("; the provider counted %s", plain.Thousands(int64(g.reported)))
	}
	if g.high > 0 {
		s += fmt.Sprintf(". Older context is trimmed past ~%s, and compaction comes just before that", plain.Thousands(int64(g.high)))
	}
	s += ".\n"
	switch {
	case g.compactions == 0:
		s += "No compaction in this session.\n"
	default:
		s += fmt.Sprintf("Compactions in this session: %d; the latest: %s\n", g.compactions, plain.CompactLine(*g.last))
	}
	return s
}

func kilo(n int) string {
	if n < 1000 {
		return fmt.Sprint(n)
	}
	return fmt.Sprintf("%dk", (n+500)/1000)
}
