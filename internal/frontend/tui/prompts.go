package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"github.com/fclaude/nibb/internal/budget"
	"github.com/fclaude/nibb/internal/event"
	"github.com/fclaude/nibb/internal/frontend/plain"
	"github.com/fclaude/nibb/internal/frontend/rpc"
	"github.com/fclaude/nibb/internal/guard"
)

type pendingPrompt struct {
	id, kind, title, detail, choices string
	session, turn                    bool
	checkpoint                       budget.Checkpoint
}

func (m *model) setPrompt(e rpc.ClientEvent) error {
	p := &pendingPrompt{kind: e.Kind}
	switch e.Kind {
	case event.PromptConfirm:
		value, err := decode[event.Confirm](e.Payload)
		if err != nil {
			return err
		}
		g := value.Prompt
		p.id = value.ID
		p.title = g.Prim
		p.detail = "Operation: " + g.Prim + "\nArguments: " + g.Args + "\n\n" + strings.Join(g.Reasons, "\n") + "\n\n" + g.Detail
		if scope := plain.EgressSummary(g.Egress); scope != "" {
			p.detail = scope + "\n\n" + p.detail
		}
		p.choices = "Type y (once) or n (deny), then Enter."
		eligible := !g.Security && !guard.IsProtected(g.Path)
		p.session = eligible && (g.Path != "" || len(g.Argv) > 0)
		p.turn = eligible && g.Path != ""
		if p.session {
			target := guard.Prefix(g.Argv)
			if g.Path != "" {
				target = guard.DirPrefix(g.Path)
			}
			p.choices += "\na = approve " + target + " for this session."
		}
		if p.turn {
			p.choices += "\nt = approve ordinary writes for this turn."
		}
	case event.PromptCheckpoint:
		value, err := decode[event.Checkpoint](e.Payload)
		if err != nil {
			return err
		}
		p.id = value.ID
		p.title = "budget checkpoint"
		p.checkpoint = value.Checkpoint
		raw := value.Checkpoint
		p.detail = fmt.Sprintf("Budget: %s\nUsed: %s · soft: %s · hard: %s\n\nUsage at checkpoint\n%s", raw.Dimension, budgetAmount(raw.Dimension, raw.Used), budgetAmount(raw.Dimension, raw.Limit.Soft), budgetAmount(raw.Dimension, raw.Limit.Hard), formatBudgetState(raw.State))
		p.choices = "y = double soft limit · n = stop · or enter an absolute soft limit (duration for wall).\nThis does not grant tool authority."
	case event.PromptSecret:
		value, err := decode[event.SecretPrompt](e.Payload)
		if err != nil {
			return err
		}
		p.id = value.ID
		p.title = "credential: " + value.Class
		p.detail = "Enter the credential for " + value.Class + ".\nIt goes to the shared keychain; only an opaque handle reaches Scheme."
		p.choices = "Input is masked. Enter submits; empty input declines. Ctrl+C cancels the turn."
	}
	if p.id == "" {
		return fmt.Errorf("missing prompt ID")
	}
	if m.resolved[p.id] || (m.prompt != nil && m.prompt.id == p.id) {
		return nil
	}
	m.prompt = p
	m.composer.Blur()
	m.promptInput.Focus()
	m.busy = true
	m.promptInput.Reset()
	m.promptInput.EchoMode = textinput.EchoNormal
	m.promptInput.Placeholder = "Type a displayed choice, then Enter"
	if p.kind == event.PromptSecret {
		m.promptInput.EchoMode = textinput.EchoPassword
		m.promptInput.Placeholder = "Secret (masked; empty declines)"
	}
	p.title, p.detail, p.choices = safe(p.title), safe(p.detail), safe(p.choices)
	m.refresh(false)
	m.viewport.GotoTop()
	return nil
}
func (m *model) reply(value string) tea.Cmd {
	p := *m.prompt
	b, id := m.backend, m.id
	if p.kind == event.PromptSecret {
		secret := []byte(value)
		m.promptInput.Reset()
		return m.action("reply", p.id, func(ctx context.Context) error { defer clear(secret); return b.Secret(ctx, id, p.id, secret) })
	}
	reply := strings.ToLower(strings.TrimSpace(value))
	scope := ""
	if p.kind == event.PromptCheckpoint {
		if _, err := budget.ParseChoice(reply, p.checkpoint); err != nil {
			m.status = safe(err.Error())
			return nil
		}
	} else {
		switch reply {
		case "y", "yes", "n", "no":
		case "a":
			if p.session {
				scope = "session"
			} else {
				m.status = "This prompt allows only a one-time answer."
				return nil
			}
		case "t":
			if p.turn {
				scope = "turn"
			} else {
				m.status = "This prompt cannot grant turn-wide write approval."
				return nil
			}
		default:
			m.status = "Type one of the displayed choices, then Enter."
			return nil
		}
	}
	m.promptInput.Reset()
	return m.action("reply", p.id, func(ctx context.Context) error {
		if scope != "" {
			return b.Grant(ctx, id, p.id, scope)
		}
		return b.Answer(ctx, id, p.id, reply)
	})
}

func budgetAmount(dimension string, value int64) string {
	if dimension == "wall" {
		return time.Duration(value).String()
	}
	return fmt.Sprint(value)
}
