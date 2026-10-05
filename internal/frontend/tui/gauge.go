package tui

import (
	"encoding/json"
	"fmt"

	"github.com/fclaude/nodwyn/internal/frontend/plain"
	"github.com/fclaude/nodwyn/internal/log"
)

// contextGauge follows how big the context the model holds is, from the
// log: the window's estimate of the latest control-model request and what
// the provider counted for its prompt, the window's high water (the
// session's, or the model's it was moved to), and the compactions so far.
// While a narrowed context is open (the explorer's, the planner's, a
// sub-agent's) and has asked, its latest request is the one shown, named by
// its agent; once it folds, the conversation's is again, as the console's
// gauge does (transcript.js contextGauge).
type contextGauge struct {
	conversation gaugeFrame
	frames       []gaugeFrame // narrowed contexts open, innermost last
	followed     *gaugeFrame  // the frame the next llm.resp answers
	high         int
	compactions  int
	last         *log.ContextCompact
}

// gaugeFrame is one context's latest request: the window's estimate and
// what the provider counted (0: not reported yet).
type gaugeFrame struct {
	agent    string
	est      int
	reported int
}

// shown is the context the gauge shows: the innermost narrowed one once it
// has asked, else the conversation.
func (g *contextGauge) shown() gaugeFrame {
	if n := len(g.frames); n > 0 && g.frames[n-1].est > 0 {
		return g.frames[n-1]
	}
	return g.conversation
}

func (g *contextGauge) observe(kind string, raw json.RawMessage) {
	switch log.Kind(kind) {
	case log.KindSessionStart, log.KindSessionResume:
		if p, err := decode[log.SessionStart](raw); err == nil && p.Context != nil {
			g.high = p.Context.HighWater
		}
	case log.KindSessionModel:
		// A conversation moved to another model is trimmed by the window
		// sized for it (ADR 40).
		if p, err := decode[log.SessionModel](raw); err == nil && p.Context != nil {
			g.high = p.Context.HighWater
		}
	case log.KindUserMsg, log.KindContextWiden:
		g.frames, g.followed = nil, nil // a narrowed context ends with the turn
	case log.KindContextNarrow:
		if p, err := decode[log.ContextNarrow](raw); err == nil && !p.Lasting {
			agent := p.Agent
			if agent == "" {
				agent = "narrowed"
			}
			// followed points into frames, which this may move.
			g.frames, g.followed = append(g.frames, gaugeFrame{agent: agent}), nil
		}
	case log.KindContextFold:
		if n := len(g.frames); n > 0 {
			g.frames, g.followed = g.frames[:n-1], nil
		}
	case log.KindLLMReq:
		g.followed = nil
		if p, err := decode[log.LLMReq](raw); err == nil && p.Endpoint == "" {
			g.followed = &g.conversation
			if n := len(g.frames); n > 0 {
				g.followed = &g.frames[n-1]
			}
			g.followed.est, g.followed.reported = p.EstTokens, 0
		}
	case log.KindLLMResp:
		if g.followed != nil {
			if p, err := decode[log.LLMResp](raw); err == nil {
				g.followed.reported = p.Usage.PromptTokens
			}
			g.followed = nil
		}
	case log.KindContextCompact:
		if p, err := decode[log.ContextCompact](raw); err == nil {
			g.compactions++
			g.last = &p
			if p.Compacted {
				g.conversation.est, g.conversation.reported = p.After, 0
			}
		}
	}
}

// title is the gauge for the title bar: "context ~45k of ~61k", with the
// agent of a narrowed context it follows: "context ~88k of ~691k
// (explorer)".
func (g *contextGauge) title() string {
	f := g.shown()
	if f.est == 0 {
		return ""
	}
	s := "context ~" + kilo(f.est)
	if g.high > 0 {
		s += " of ~" + kilo(g.high)
	}
	if f.agent != "" {
		s += " (" + f.agent + ")"
	}
	return s
}

// report is the gauge for the budget inspector.
func (g *contextGauge) report() string {
	f := g.shown()
	if f.est == 0 {
		return "Context: no model request yet.\n"
	}
	whose := "the latest request"
	if f.agent != "" {
		whose = "the " + f.agent + "'s latest request, in a context of its own that folds back into the conversation"
	}
	s := fmt.Sprintf("Context: ~%s tokens as nodwyn estimates %s", plain.Thousands(int64(f.est)), whose)
	if f.reported > 0 {
		s += fmt.Sprintf("; the provider counted %s", plain.Thousands(int64(f.reported)))
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
