package core

import (
	"context"
	"crypto/rand"
	"fmt"
	"strings"

	"github.com/fclaude/nibb/internal/budget"
	"github.com/fclaude/nibb/internal/event"
	"github.com/fclaude/nibb/internal/guard"
	"github.com/fclaude/nibb/internal/log"
)

type pendingConfirmation struct {
	ctx        context.Context
	checkpoint *pendingCheckpoint
	prompt     guard.Prompt
	reply      chan guard.Approval
}

// Answer preserves the terminal's y/n/a/t vocabulary. Transport callers use
// AnswerFrom or CreateApproval so widening authority is an explicit operation.
func (s *Session) Answer(id, reply string) error { return s.answer(id, reply, "", "", false) }

// AnswerFrom accepts only a one-time answer or a budget choice. Actor is set by
// the frontend's authenticated transport, never by the model or request body.
func (s *Session) AnswerFrom(id, reply, actor string) error {
	if actor == "" {
		return fmt.Errorf("core: answering actor is required")
	}
	return s.answer(id, reply, actor, "", true)
}

// CreateApproval widens a specific pending ordinary confirmation. There is no
// arbitrary prefix setter: scope and target are derived from the actual call.
func (s *Session) CreateApproval(id, scope, actor string) error {
	if actor == "" || (scope != "session" && scope != "turn") {
		return fmt.Errorf("core: actor and session or turn scope are required")
	}
	return s.answer(id, "y", actor, scope, true)
}
func (s *Session) answer(id, reply, actor, scope string, strict bool) error {
	s.promptMu.Lock()
	defer s.promptMu.Unlock()
	pending, ok := s.prompts[id]
	if !ok || (pending.ctx != nil && pending.ctx.Err() != nil) {
		return fmt.Errorf("core: no pending prompt %q", id)
	}
	if pending.checkpoint != nil {
		if scope != "" {
			return fmt.Errorf("core: a checkpoint cannot grant tool approval")
		}
		choice, err := budget.ParseChoice(reply, pending.checkpoint.info)
		if err != nil {
			return err
		}
		delete(s.prompts, id)
		pending.checkpoint.reply <- checkpointReply{choice: choice, actor: actor}
		return nil
	}
	r := strings.ToLower(strings.TrimSpace(reply))
	p := pending.prompt
	if strict && scope == "" && r != "y" && r != "yes" && r != "n" && r != "no" {
		return fmt.Errorf("core: ordinary answers accept y or n; use explicit approval creation to widen scope")
	}
	if !strict {
		switch r {
		case "a", "always":
			if len(p.Argv) > 0 || p.Path != "" {
				scope = "session"
				r = "y"
			}
		case "t", "turn":
			if p.Path != "" {
				scope = "turn"
				r = "y"
			}
		}
		// Keep the terminal's protected-path fallback: approve this call only.
		if p.Security || guard.IsProtected(p.Path) {
			scope = ""
		}
	}
	options := p.ApprovalOptions()
	if scope == "binding" {
		if !options.Binding {
			return fmt.Errorf("core: this prompt has no eligible binding proposal")
		}
	} else if scope != "" {
		if !options.Session || (scope == "turn" && !options.Turn) {
			return fmt.Errorf("core: this prompt is not eligible for that approval scope")
		}
	}
	if scope == "" {
		scope = "once"
	}
	delete(s.prompts, id)
	pending.reply <- guard.Approval{Allowed: r == "y" || r == "yes", Actor: actor, PromptID: id, Scope: scope}
	return nil
}

// Allowed lists the command prefixes with a standing approval.
func (s *Session) Allowed() []string { return s.allow.List() }

// AllowedWrites lists the write paths with a standing approval.
func (s *Session) AllowedWrites() []string { return s.writes.List() }

// TurnApproval reports whether ordinary writes are approved for the rest of
// the current turn, and who approved them.
func (s *Session) TurnApproval() (bool, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.turnTrust, s.turnActor
}

// Confirm satisfies the existing boolean interface; Guard uses the attributed
// extension below when available.
func (s *Session) Confirm(ctx context.Context, p guard.Prompt) (bool, error) {
	a, err := s.ConfirmApproval(ctx, p)
	return a.Allowed, err
}

// ConfirmApproval puts a guard prompt to the front-end and returns the
// attributed answer: who answered, and whether for this call, this turn or
// standing. Turn trust answers ordinary writes without asking.
func (s *Session) ConfirmApproval(ctx context.Context, p guard.Prompt) (guard.Approval, error) {
	// A proposal must be completely visible, exact, and derived from this call.
	if !s.bindingEligible(p) {
		p.Binding = nil
	}
	if p.Path != "" && !guard.IsProtected(p.Path) && !p.Security {
		trust, actor := s.TurnApproval()
		if trust {
			if _, err := s.Record(log.KindNote, log.Note{Text: "write approved by turn trust: " + p.Prim + " " + p.Path + "\n" + p.Detail}); err != nil {
				return guard.Approval{}, err
			}
			return guard.Approval{Allowed: true, Actor: actor, Scope: "turn"}, nil
		}
	}
	s.promptMu.Lock()
	id := "p" + rand.Text()
	ch := make(chan guard.Approval, 1)
	s.prompts[id] = pendingConfirmation{ctx: ctx, prompt: p, reply: ch}
	s.promptMu.Unlock()
	defer func() { s.promptMu.Lock(); delete(s.prompts, id); s.promptMu.Unlock() }()
	s.Emit(event.Event{Kind: event.PromptConfirm, Payload: event.NewConfirm(id, p)})
	select {
	case a := <-ch:
		if err := ctx.Err(); err != nil {
			return a, err
		}
		if a.Allowed && a.Scope != "once" {
			if err := s.installApproval(ctx, p, a); err != nil {
				return a, err
			}
		}
		return a, nil
	case <-ctx.Done():
		return guard.Approval{PromptID: id}, ctx.Err()
	}
}

// The waiting execution goroutine installs a grant, after checking cancellation
// and recording its issuer. Reply delivery itself never mutates permission.
func (s *Session) installApproval(ctx context.Context, p guard.Prompt, a guard.Approval) error {
	if a.Scope == "binding" {
		return s.installBinding(ctx, p, a)
	}
	kind, prefix := "write", guard.DirPrefix(p.Path)
	if len(p.Argv) > 0 {
		kind, prefix = "command", guard.Prefix(p.Argv)
	}
	if a.Scope == "turn" {
		prefix = "*"
	}
	if _, err := s.Record(log.KindApprovalChange, log.ApprovalChange{Actor: a.Actor, PromptID: a.PromptID, Scope: a.Scope, Kind: kind, Prefix: prefix}); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if a.Scope == "turn" {
		s.mu.Lock()
		s.turnTrust = true
		s.turnActor = a.Actor
		s.mu.Unlock()
	} else if kind == "command" {
		s.allow.Add(prefix)
	} else {
		s.writes.Add(prefix)
	}
	return nil
}
