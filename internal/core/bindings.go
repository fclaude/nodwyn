package core

import (
	"context"
	"fmt"
	"github.com/fclaude/nodwyn/internal/binding"
	"github.com/fclaude/nodwyn/internal/guard"
	"github.com/fclaude/nodwyn/internal/log"
	"github.com/fclaude/nodwyn/internal/redact"
	"reflect"
)

// Bindings returns a copy of the last validated policy snapshot. The guard
// reloads the file before egress; failed reloads deny rather than use stale policy.
func (s *Session) Bindings() []binding.Binding { return s.bindings.Snapshot().Bind }

// CreateBinding approves the exact row attached to a still-pending call. The
// authenticated frontend supplies its actor; callers cannot choose row fields.
func (s *Session) CreateBinding(id, actor string) error {
	if actor == "" {
		return fmt.Errorf("core: binding actor is required")
	}
	return s.answer(id, "y", actor, "binding", true)
}

func (s *Session) bindingEligible(p guard.Prompt) bool {
	if s.opts.Paths.Bindings == "" || p.Security || p.Binding == nil || p.Egress == nil {
		return false
	}
	f := p.Egress
	row, err := binding.Exact(f.Class, f.Host, f.Method, f.Position)
	if err != nil || !reflect.DeepEqual(row, *p.Binding) {
		return false
	}
	_, redactions, err := redact.Payload(row, nil)
	return err == nil && len(redactions) == 0
}

func (s *Session) installBinding(ctx context.Context, p guard.Prompt, a guard.Approval) error {
	if !s.bindingEligible(p) {
		return fmt.Errorf("core: binding proposal is no longer eligible")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	change := log.BindingChange{Actor: a.Actor, PromptID: a.PromptID, Binding: *p.Binding, State: "requested"}
	if _, err := s.Record(log.KindBindingChange, change); err != nil {
		return err
	}
	err := binding.AppendExact(ctx, s.opts.Paths.Bindings, *p.Binding)
	if err == nil {
		err = s.bindings.Refresh()
	}
	change.State = "applied"
	if err != nil {
		change.State, change.Error = "failed", err.Error()
	}
	if _, recordErr := s.Record(log.KindBindingChange, change); recordErr != nil {
		return recordErr
	}
	return err
}
