package core

import (
	"context"
	"fmt"
	"slices"

	"github.com/fclaude/nodwyn/internal/config"
	"github.com/fclaude/nodwyn/internal/guard"
	"github.com/fclaude/nodwyn/internal/log"
	"github.com/fclaude/nodwyn/internal/prim"
)

// A session a person can answer asks them less (ADR 35). approval.writes
// makes writes the pin lets through a standing approval (guard.FolderWrites,
// installed by newGuard), and approval.commands decides who answers the
// other ordinary confirms; the person answers the rest.

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

// personConfirmer is the confirmer of a session a person can answer.
func (s *Session) personConfirmer() guard.Confirmer {
	switch s.approval().Commands {
	case config.ApproveJudge:
		return approvalConfirmer{s: s, judge: true}
	case config.ApproveAllow:
		return approvalConfirmer{s: s}
	}
	return s
}

// approvalConfirmer answers what approval.commands covers: an ordinary
// confirm that is not a write (approval.writes says whether those ask) and
// carries no credential (an egress, a binding proposal, an upload). With
// judge, the judge is asked first, as in a judged run (judge.go), and the
// person when it refuses, fails or is not set up: unlike a run's, a refusal
// here is a question for the person, who is told what the judge answered.
// Without it, those confirms are answered yes. Everything else, every
// security confirm included, goes to the person.
type approvalConfirmer struct {
	s     *Session
	judge bool
}

func (a approvalConfirmer) Confirm(ctx context.Context, p guard.Prompt) (bool, error) {
	approval, err := a.ConfirmApproval(ctx, p)
	return approval.Allowed, err
}

// ConfirmApproval answers p once: an answer from the judge or the setting
// never becomes a standing one. A person's "always this session" still
// does, and then the call is no longer asked about.
func (a approvalConfirmer) ConfirmApproval(ctx context.Context, p guard.Prompt) (guard.Approval, error) {
	if p.Security || p.Path != "" || p.Egress != nil || p.Binding != nil || p.Prim == uploadPrim {
		return a.s.ConfirmApproval(ctx, p)
	}
	if !a.judge {
		return guard.Approval{Allowed: true, Actor: "setting", Scope: "once"}, nil
	}
	// "Don't ask" answers it already (confirm.go), and no judge is needed;
	// without a judge, the person is asked.
	if on, _ := a.s.ApproveAll(); on || !a.s.judgeAvailable() {
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
	if err == nil {
		a.s.noteAnswer(p, map[bool]string{true: "allowed by the person", false: "refused by the person"}[approval.Allowed])
	}
	return approval, err
}

// judgeAvailable reports whether the judge can be asked: its primitives
// are registered (Jev enabled, a keychain) and a jev-key is stored.
func (s *Session) judgeAvailable() bool {
	_, ok := s.interp.Lookup("jev-choice")
	return ok && s.HandleForClass(prim.JevClass) != nil
}
