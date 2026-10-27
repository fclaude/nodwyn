package tui

import (
	"context"
	"fmt"

	"github.com/fclaude/nodwyn/internal/binding"
	"github.com/fclaude/nodwyn/internal/budget"
	"github.com/fclaude/nodwyn/internal/core"
	"github.com/fclaude/nodwyn/internal/frontend/rpc"
	"github.com/fclaude/nodwyn/internal/llm"
	"github.com/fclaude/nodwyn/internal/log"
)

type policyView struct {
	Bindings         []binding.Binding
	Commands, Writes []string
	TurnWrites       bool
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

// Tools is the model-facing catalog captured when the session was
// constructed, not the interpreter's mutable registry or configuration
// files on disk.
func (r *remote) Tools(ctx context.Context, id string) ([]llm.Tool, error) {
	var info struct {
		Tools []llm.Tool `json:"tools"`
	}
	if err := r.client.Call(ctx, "session.info", map[string]any{"id": id}, &info); err != nil {
		return nil, err
	}
	return info.Tools, nil
}

// These are the limits for the task's next turn, not current usage: the
// task's own (ADR 49, amended 2026-10-27), with a scheduled run's spend
// limit in place of the task's.
func (r *remote) Budgets(ctx context.Context, id string) (budget.Limits, error) {
	var info rpc.SessionDetails
	if err := r.client.Call(ctx, "session.info", map[string]any{"id": id}, &info); err != nil {
		return budget.Limits{}, err
	}
	limits := budget.Limits{ToolCalls: info.Budgets.ToolCalls, Tokens: info.Budgets.Tokens, Actions: info.Budgets.Actions, Wall: info.Budgets.Wall, Cost: info.Budgets.Cost}
	if err := checkTurnLimits(limits); err != nil {
		return budget.Limits{}, err
	}
	return limits, nil
}

// checkTurnLimits refuses turn limits a daemon could not have set. A limit
// of 0 is none, as every limit of a task is unless a person set it.
func checkTurnLimits(limits budget.Limits) error {
	for _, limit := range []budget.Limit{limits.ToolCalls, limits.Tokens, limits.Actions, limits.Wall, limits.Cost} {
		if limit == (budget.Limit{}) {
			continue
		}
		if limit.Soft <= 0 || limit.Hard < limit.Soft {
			return fmt.Errorf("daemon did not provide valid configured budget limits")
		}
	}
	return nil
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
