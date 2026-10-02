package core

import (
	"context"
	"fmt"

	"github.com/fclaude/nodwyn/internal/guard"
	"github.com/fclaude/nodwyn/internal/log"
	"github.com/fclaude/nodwyn/internal/scheme"
)

// judgeConfirmer answers a session's confirms with the judge (Jev) instead of
// a person: a run started with --judge (ADR 18 §5, ADR 20 Seam C). Each
// ordinary confirm is put to approve.scm's (approve-call call), which asks the
// judge whether the call should run and allows it only above a threshold.
// A security confirm is declined without asking: those exist for the case
// where a person is the control. Whatever goes wrong is a no.
type judgeConfirmer struct{ s *Session }

func (j judgeConfirmer) Confirm(ctx context.Context, p guard.Prompt) (bool, error) {
	a, err := j.ConfirmApproval(ctx, p)
	return a.Allowed, err
}

// ConfirmApproval answers p, attributed to "judge" (or, for a security
// confirm, "unattended"), once: a judged answer never becomes a standing one.
func (j judgeConfirmer) ConfirmApproval(ctx context.Context, p guard.Prompt) (guard.Approval, error) {
	if p.Security {
		_, err := j.s.Record(log.KindNote, log.Note{Text: "judged approval: " + p.Prim + " declined without asking the judge (a security confirm)"})
		return guard.Approval{Actor: "unattended", Scope: "once"}, err
	}
	allowed, why := j.s.judgeCall(ctx, p)
	verdict := "declined"
	if allowed {
		verdict = "allowed"
	}
	if _, err := j.s.Record(log.KindNote, log.Note{Text: fmt.Sprintf("judged approval: %s %s: %s", p.Prim, verdict, why)}); err != nil {
		return guard.Approval{}, err
	}
	return guard.Approval{Allowed: allowed, Actor: "judge", Scope: "once"}, nil
}

// judgeCall applies (approve-call call) with harness origin and reads its
// verdict. The call goes through the guard like the plan gate's: the
// jev-key's binding is checked and what is sent is redacted.
func (s *Session) judgeCall(ctx context.Context, p guard.Prompt) (bool, string) {
	proc, ok := s.interp.Global().Lookup("approve-call")
	if !ok {
		return false, "approve-call is not defined"
	}
	reasons := make([]scheme.Value, len(p.Reasons))
	for i, r := range p.Reasons {
		reasons[i] = scheme.Str(r)
	}
	argv := make([]scheme.Value, len(p.Argv))
	for i, a := range p.Argv {
		argv[i] = scheme.Str(a)
	}
	task, plan := s.turnTaskAndPlan()
	call := scheme.List(
		scheme.Cons(scheme.Sym("prim"), scheme.Str(p.Prim)),
		scheme.Cons(scheme.Sym("args"), scheme.Str(p.Args)),
		scheme.Cons(scheme.Sym("reasons"), scheme.List(reasons...)),
		scheme.Cons(scheme.Sym("path"), scheme.Str(p.Path)),
		scheme.Cons(scheme.Sym("argv"), scheme.List(argv...)),
		scheme.Cons(scheme.Sym("detail"), scheme.Str(p.Detail)),
		scheme.Cons(scheme.Sym("task"), scheme.Str(task)),
		scheme.Cons(scheme.Sym("plan"), scheme.Str(plan)),
		scheme.Cons(scheme.Sym("content"), scheme.Str(jevOptions(s.opts.Config.Jev).Content)),
	)
	v, err := s.interp.Apply(scheme.WithOrigin(ctx, scheme.OriginHarness), proc, []scheme.Value{call})
	if err != nil {
		return false, "approve-call failed: " + err.Error()
	}
	why := "approve-call gave no reason"
	if r, ok := alistGet(v, "reason"); ok && r.Kind == scheme.KString {
		why = r.S
	}
	allow, ok := alistGet(v, "allow")
	return ok && allow.IsTrue(), why
}

// alistGet finds key, a symbol, in an alist.
func alistGet(v scheme.Value, key string) (scheme.Value, bool) {
	for v.Kind == scheme.KPair {
		if e := v.P.Car; e.Kind == scheme.KPair && e.P.Car.Kind == scheme.KSymbol && e.P.Car.S == key {
			return e.P.Cdr, true
		}
		v = v.P.Cdr
	}
	return scheme.Value{}, false
}

func (s *Session) setTurnTask(task string) {
	s.turnMu.Lock()
	s.turnTask, s.turnPlan = task, ""
	s.turnMu.Unlock()
}

func (s *Session) setTurnPlan(plan string) {
	s.turnMu.Lock()
	s.turnPlan = plan
	s.turnMu.Unlock()
}

func (s *Session) turnTaskAndPlan() (string, string) {
	s.turnMu.Lock()
	defer s.turnMu.Unlock()
	return s.turnTask, s.turnPlan
}
