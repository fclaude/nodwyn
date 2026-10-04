package core

import (
	"context"
	"crypto/rand"
	"fmt"
	"strings"

	"github.com/fclaude/nodwyn/internal/budget"
	"github.com/fclaude/nodwyn/internal/event"
	"github.com/fclaude/nodwyn/internal/guard"
	"github.com/fclaude/nodwyn/internal/log"
)

type pendingConfirmation struct {
	ctx        context.Context
	checkpoint *pendingCheckpoint
	choice     *pendingChoice
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
	if pending.choice != nil {
		if scope != "" {
			return fmt.Errorf("core: a question's answer is one of its choices")
		}
		return s.answerChoice(id, pending, reply, actor)
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

// ApproveAll reports whether "don't ask" is on, ordinary writes and
// commands approved without a prompt, and who turned it on.
func (s *Session) ApproveAll() (bool, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.approveAll, s.approveActor
}

// SetApproveAll turns "don't ask" on or off for this session, as actor, the
// person at a front-end's authenticated transport. Only a front-end calls
// it: no Scheme primitive reads or changes it, whatever its origin, since a
// repository's init.scm runs as user-origin Scheme. It answers only the
// confirms a standing approval could answer (ordinary writes and commands);
// security confirms and protected paths still ask, and nothing the guard
// refuses is allowed. It is never saved: a resumed session starts with it
// off. A session whose confirms something else answers (--yolo, a judge,
// --no-confirm, an unattended run) refuses it.
func (s *Session) SetApproveAll(on bool, actor string) error {
	if actor == "" {
		return fmt.Errorf("core: answering actor is required")
	}
	if s.opts.NoConfirm || s.opts.AutoApprove || s.opts.JudgeApprove || s.opts.Unattended {
		return fmt.Errorf("core: this session's confirms are not asked of a person, so there is nothing to stop asking")
	}
	s.mu.Lock()
	same := s.approveAll == on
	s.mu.Unlock()
	if same {
		return nil
	}
	// What the switch would answer now: an ordinary write or command
	// waiting for a person, never a checkpoint, a choice or a spent
	// context.
	answers := func(p pendingConfirmation) bool {
		return p.checkpoint == nil && p.choice == nil && p.reply != nil && p.prompt.ApprovalOptions().Session &&
			(p.ctx == nil || p.ctx.Err() == nil)
	}
	// The record names the question whose wait the switch ends, as every
	// log event that resolves a prompt does: the console's clocks keep a
	// person's wait out of the working time by reading prompt_id on the
	// event that ends it. Several may wait; the smallest id stands in the
	// record, and each answer the switch gives carries its own.
	var first string
	if on {
		s.promptMu.Lock()
		for id, pending := range s.prompts {
			if answers(pending) && (first == "" || id < first) {
				first = id
			}
		}
		s.promptMu.Unlock()
	}
	if _, err := s.Record(log.KindApprovalChange, log.ApprovalChange{Actor: actor, PromptID: first, Scope: "session", Kind: "all", Prefix: "*", Off: !on}); err != nil {
		return err
	}
	s.mu.Lock()
	s.approveAll, s.approveActor = on, actor
	if !on {
		s.approveActor = ""
	}
	s.mu.Unlock()
	if on {
		// What is waiting for an answer now that it would have answered.
		s.promptMu.Lock()
		for id, pending := range s.prompts {
			if answers(pending) {
				delete(s.prompts, id)
				pending.reply <- guard.Approval{Allowed: true, Actor: actor, PromptID: id, Scope: "all"}
			}
		}
		s.promptMu.Unlock()
	}
	return nil
}

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
	if p.Prim == uploadPrim && s.uploadPolicy() == "" {
		return s.chooseUploadPolicy(ctx, p)
	}
	return s.confirmPrompt(ctx, p)
}

// confirmPrompt asks p as a yes-or-no confirm.
func (s *Session) confirmPrompt(ctx context.Context, p guard.Prompt) (guard.Approval, error) {
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
	// "Don't ask" answers what a standing approval could: an ordinary write
	// or command, never a security confirm or a protected path.
	if on, actor := s.ApproveAll(); on && p.ApprovalOptions().Session {
		return guard.Approval{Allowed: true, Actor: actor, Scope: "all"}, nil
	}
	s.promptMu.Lock()
	id := "p" + rand.Text()
	ch := make(chan guard.Approval, 1)
	s.prompts[id] = pendingConfirmation{ctx: ctx, prompt: p, reply: ch}
	s.promptMu.Unlock()
	defer func() {
		s.promptMu.Lock()
		delete(s.prompts, id)
		s.promptMu.Unlock()
		s.Emit(event.Event{Kind: event.PromptClosed, Payload: event.Closed{ID: id}})
	}()
	s.Emit(event.Event{Kind: event.PromptConfirm, Payload: event.NewConfirm(id, p)})
	select {
	case a := <-ch:
		if err := ctx.Err(); err != nil {
			return a, err
		}
		// An answer from "don't ask" (scope all) installs nothing: the
		// switch itself is the approval.
		if a.Allowed && a.Scope != "once" && a.Scope != "all" {
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
