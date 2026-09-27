package core

import (
	"context"
	"errors"
	"fmt"

	"github.com/fclaude/nibb/internal/budget"
	"github.com/fclaude/nibb/internal/llm"
	"github.com/fclaude/nibb/internal/log"
	"github.com/fclaude/nibb/internal/plan"
	"github.com/fclaude/nibb/internal/redact"
	"github.com/fclaude/nibb/internal/scheme"
)

// ApplyPrim applies the named primitive through the apply→guard path (the
// same path a model tool call takes), with harness origin.
func (s *Session) ApplyPrim(ctx context.Context, name string, args []scheme.Value) (scheme.Value, error) {
	p, ok := s.interp.Lookup(name)
	if !ok {
		return scheme.Value{}, fmt.Errorf("core: unknown primitive %q", name)
	}
	return s.interp.Apply(scheme.WithOrigin(ctx, scheme.OriginHarness), scheme.Value{Kind: scheme.KPrim, Prim: p}, args)
}

// JevHandle returns the Jev credential handle, or nil if none is registered.
func (s *Session) JevHandle() *scheme.Handle { return s.HandleForClass("jev-key") }

// HandleForClass returns the handle of a stored credential of class, or nil
// when there is none or no keychain is open (prim.CredentialStore).
func (s *Session) HandleForClass(class string) *scheme.Handle {
	if s.keychain == nil {
		return nil
	}
	return s.keychain.HandleForClass(class)
}

// PlanPrompts returns the planner's prompts (plan.Gate).
func (s *Session) PlanPrompts() plan.Prompts { return s.planPrompts }

// Draft asks the control-plane model for a plan (plan.Gate).
func (s *Session) Draft(ctx context.Context, prompt string) (string, error) {
	if s.opts.Provider == nil {
		return "", errors.New("core: no control-plane provider")
	}
	turn := budget.From(ctx)
	if turn != nil {
		if err := turn.Check(ctx); err != nil {
			return "", err
		}
	}
	timeout, _ := s.opts.Config.Loop.Durations()
	attempt := func(parent context.Context) (context.Context, context.CancelFunc) {
		return context.WithTimeout(parent, timeout)
	}
	resp, err := llm.ChatRetrying(ctx, s.opts.Provider, llm.Request{
		Model:  s.opts.Config.Control.Model,
		NumCtx: s.opts.Config.Control.NumCtx,
		Messages: []llm.Message{
			{Role: llm.RoleSystem, Content: redact.Redact(s.planPrompts.System, nil).Text},
			{Role: llm.RoleUser, Content: redact.Redact(prompt, nil).Text},
		},
	}, attempt, func(n int, err error) {
		_, _ = s.Record(log.KindNote, log.Note{Text: fmt.Sprintf("plan request failed, retrying (%d/%d): %s", n, llm.Retries, err)})
	})
	if err != nil {
		return "", err
	}
	if turn != nil {
		turn.AddTokens(resp.Usage.PromptTokens, resp.Usage.OutputTokens)
		if err := turn.Finish(ctx); err != nil {
			return "", err
		}
	}
	return redact.Redact(resp.Message.Content, nil).Text, nil
}
