package core

import (
	"context"
	"errors"
	"fmt"

	"github.com/fclaude/nibb/internal/budget"
	"github.com/fclaude/nibb/internal/llm"
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
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	resp, err := s.opts.Provider.Chat(cctx, llm.Request{
		Model:  s.opts.Config.Control.Model,
		NumCtx: s.opts.Config.Control.NumCtx,
		Messages: []llm.Message{
			{Role: llm.RoleSystem, Content: redact.Redact("You are nibb's planner for the repository at "+s.opts.Repo+". Return only the plan text.", nil).Text},
			{Role: llm.RoleUser, Content: redact.Redact(prompt, nil).Text},
		},
	}, nil)
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
