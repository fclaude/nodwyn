package core

import (
	"context"
	"fmt"
	"slices"

	"github.com/fclaude/brote/scheme"
	"github.com/fclaude/nodwyn/internal/config"
	"github.com/fclaude/nodwyn/internal/guard"
	"github.com/fclaude/nodwyn/internal/log"
)

// A session a person can answer asks them less (ADR 35). approval.writes
// makes writes the pin lets through a standing approval (guard.FolderWrites,
// installed by newGuard), and approval.commands, or the task's own choice,
// decides who answers the confirms about commands; the person answers the
// rest.

// approval returns the session's [approval] settings, defaults filled in.
func (s *Session) approval() config.Approval {
	a, d := s.opts.Config.Approval, config.Default().Approval
	if a.Writes == "" {
		a.Writes = d.Writes
	}
	if a.Commands == "" {
		a.Commands = d.Commands
	}
	return a
}

// approvalConfirmer is the confirmer of a session a person can answer. It
// answers what approval.commands covers: a confirm that is not a write
// (approval.writes says whether those ask) and carries no credential (an
// egress, a binding proposal, an upload), a security confirm about a
// command included; the usual one is a path outside the repository, such
// as a read of the Go module cache, and the person asked who answers
// commands so that they stop being asked about it (ADR 35, amended
// 2026-10-30). Who answers is read at each confirm (Commands), the task's
// own choice or the setting's:
//
//   - judge: the judge is asked first, as in a judged run (judge.go), and
//     the person when it refuses, fails or is not set up: unlike a run's, a
//     refusal here is a question for the person, who is told what the judge
//     answered.
//   - judge-auto: the judge alone. What it does not allow, or having no
//     judge, is a no, as in a judged run: nobody is asked.
//   - allow: yes, without asking anyone.
//   - ask: the person.
//
// What the guard denies (the credential denylist, unbound egress, a literal
// key on a command line) is not a confirm and stays denied whatever this
// says. A write, an egress, a binding proposal and an upload go to the
// person, who is the control for what leaves the machine or changes what is
// trusted.
type approvalConfirmer struct {
	s *Session
}

func (a approvalConfirmer) Confirm(ctx context.Context, p guard.Prompt) (bool, error) {
	approval, err := a.ConfirmApproval(ctx, p)
	return approval.Allowed, err
}

// ConfirmApproval answers p once: an answer from the judge or the setting
// never becomes a standing one. A person's "always this session" still
// does, and then the call is no longer asked about.
//
// A question waiting for the person when who answers commands changes is
// handed back (SetCommands, rescoped) and answered again by the new choice,
// so choosing the judge alone, or no asking, also answers what is on the
// screen; the record keeps the waiting question's id, which the console's
// clocks read to leave the person's wait out of the working time.
func (a approvalConfirmer) ConfirmApproval(ctx context.Context, p guard.Prompt) (guard.Approval, error) {
	if !commandPrompt(p) {
		return a.s.ConfirmApproval(ctx, p)
	}
	waited := ""
	for {
		approval, err := a.answer(ctx, p)
		if err != nil || approval.Scope != rescoped {
			if approval.PromptID == "" {
				approval.PromptID = waited
			}
			return approval, err
		}
		waited = approval.PromptID
	}
}

// commandPrompt is a confirm approval.commands covers.
func commandPrompt(p guard.Prompt) bool {
	return p.Path == "" && p.Egress == nil && p.Binding == nil && p.Prim != uploadPrim
}

// rescoped is the scope of the answer a waiting question is given when who
// answers commands changed under it: no answer, but a sign to ask again.
const rescoped = "rescoped"

// answer answers p as Commands says now.
func (a approvalConfirmer) answer(ctx context.Context, p guard.Prompt) (guard.Approval, error) {
	mode, _ := a.s.Commands()
	switch mode {
	case config.ApproveAsk:
		return a.s.ConfirmApproval(ctx, p)
	case config.ApproveAllow:
		return guard.Approval{Allowed: true, Actor: "setting", Scope: "once"}, nil
	case config.ApproveJudgeAuto:
		return judgeConfirmer{s: a.s, security: true}.ConfirmApproval(ctx, p)
	}
	// "Don't ask" answers an ordinary confirm already (confirm.go), and no
	// judge is needed; without a judge, the person is asked.
	if on, _ := a.s.ApproveAll(); (on && p.Ordinary()) || !a.s.judgeAvailable() {
		return a.s.ConfirmApproval(ctx, p)
	}
	allowed, why := a.s.judgeCall(ctx, p, true)
	verdict := "declined, so you are asked"
	if allowed {
		verdict = "allowed"
	}
	if _, err := a.s.Record(log.KindNote, log.Note{Text: fmt.Sprintf("judged approval: %s %s: %s", p.Prim, verdict, why)}); err != nil {
		return guard.Approval{}, err
	}
	if allowed {
		return guard.Approval{Allowed: true, Actor: "judge", Scope: "once"}, nil
	}
	if err := ctx.Err(); err != nil {
		return guard.Approval{}, err
	}
	asked := p
	asked.Reasons = append(slices.Clone(p.Reasons), "the judge did not allow it: "+why)
	approval, err := a.s.ConfirmApproval(ctx, asked)
	if err == nil && approval.Scope != rescoped {
		a.s.noteAnswer(p, map[bool]string{true: "allowed by the person", false: "refused by the person"}[approval.Allowed])
	}
	return approval, err
}

// judgeAvailable reports whether the judge can be asked: its primitives
// are registered and the task has a judge whose key is stored (ADR 47).
func (s *Session) judgeAvailable() bool {
	_, ok := s.interp.Lookup("judge-choice")
	return ok && s.judgeKey() != nil
}

// judgeKey is the handle of the key of the task's judge, or nil when it
// has none, the key is not stored, or it is not one the judge's API takes.
func (s *Session) judgeKey() *scheme.Handle {
	spec, ok := s.judgeSpec()
	if !ok {
		return nil
	}
	h, _ := s.judgeHandle(spec)
	return h
}
