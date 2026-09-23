package tui

import (
	"context"
	"fmt"

	"github.com/fclaude/nibb/internal/binding"
	"github.com/fclaude/nibb/internal/budget"
	"github.com/fclaude/nibb/internal/core"
	"github.com/fclaude/nibb/internal/frontend/rpc"
	"github.com/fclaude/nibb/internal/llm"
	"github.com/fclaude/nibb/internal/log"
)

type policyView struct {
	Bindings         []binding.Binding
	Commands, Writes []string
	TurnWrites       bool
}

func (l *local) Policy(ctx context.Context, _ string) (policyView, error) {
	if err := ctx.Err(); err != nil {
		return policyView{}, err
	}
	trust, _ := l.session.TurnApproval()
	return policyView{Bindings: l.session.Bindings(), Commands: l.session.Allowed(), Writes: l.session.AllowedWrites(), TurnWrites: trust}, nil
}
func (r *remote) Policy(ctx context.Context, id string) (policyView, error) {
	// A missing field on an older daemon must not look like an empty policy.
	var info struct {
		Bindings   *[]binding.Binding `json:"bindings"`
		Commands   []string           `json:"allowed_commands"`
		Writes     []string           `json:"allowed_writes"`
		TurnWrites bool               `json:"turn_writes"`
	}
	if err := r.client.Call(ctx, "session.info", map[string]any{"id": id}, &info); err != nil {
		return policyView{}, err
	}
	if info.Bindings == nil {
		return policyView{}, fmt.Errorf("daemon lacks binding inspection; upgrade it before using :bindings")
	}
	return policyView{Bindings: *info.Bindings, Commands: info.Commands, Writes: info.Writes, TurnWrites: info.TurnWrites}, nil
}

// Read the model-facing catalog captured when the session was constructed,
// not the interpreter's mutable registry or configuration files on disk.
func (l *local) Tools(ctx context.Context, _ string) ([]llm.Tool, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return l.session.Tools(), nil
}
func (r *remote) Tools(ctx context.Context, id string) ([]llm.Tool, error) {
	var info struct {
		Tools []llm.Tool `json:"tools"`
	}
	if err := r.client.Call(ctx, "session.info", map[string]any{"id": id}, &info); err != nil {
		return nil, err
	}
	return info.Tools, nil
}

// These are configured limits for new turns in this session lifetime, not
// current usage or a soft limit changed by an in-progress checkpoint reply.
func (l *local) Budgets(ctx context.Context, _ string) (budget.Limits, error) {
	if err := ctx.Err(); err != nil {
		return budget.Limits{}, err
	}
	return l.options.Config.Budget.Limits(l.options.Config.Loop.MaxToolCalls)
}
func (r *remote) Budgets(ctx context.Context, id string) (budget.Limits, error) {
	var info rpc.SessionDetails
	if err := r.client.Call(ctx, "session.info", map[string]any{"id": id}, &info); err != nil {
		return budget.Limits{}, err
	}
	limits := budget.Limits{ToolCalls: info.Budgets.ToolCalls, Tokens: info.Budgets.Tokens, Actions: info.Budgets.Actions, Wall: info.Budgets.Wall}
	for _, limit := range []budget.Limit{limits.ToolCalls, limits.Tokens, limits.Actions, limits.Wall} {
		if limit.Soft <= 0 || limit.Hard < limit.Soft {
			return budget.Limits{}, fmt.Errorf("daemon did not provide valid configured budget limits")
		}
	}
	return limits, nil
}
func (l *local) History(ctx context.Context, id string) ([]log.Event, error) {
	store := core.Store{Dir: l.options.Paths.SessionsDir}
	return readHistory(ctx, func(after, through uint64) (core.LogPage, error) { return store.ReadLog(id, after, through, 100) })
}
func (r *remote) History(ctx context.Context, id string) ([]log.Event, error) {
	return readHistory(ctx, func(after, through uint64) (page core.LogPage, err error) {
		err = r.client.Call(ctx, "log.read", map[string]any{"id": id, "after": after, "through": through, "limit": 100}, &page)
		return
	})
}
func readHistory(ctx context.Context, read func(uint64, uint64) (core.LogPage, error)) ([]log.Event, error) {
	var result []log.Event
	var after, through uint64
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		page, err := read(after, through)
		if err != nil {
			return nil, err
		}
		if page.Truncated {
			return nil, fmt.Errorf("history has an incomplete tail; refresh after the writer finishes, or resume after a crash")
		}
		if through == 0 {
			through = page.Through
		}
		result = append(result, page.Events...)
		if !page.More {
			return result, nil
		}
		if page.Next <= after {
			return nil, fmt.Errorf("log cursor did not advance")
		}
		after = page.Next
	}
}
