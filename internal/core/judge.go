package core

import (
	"context"
	"fmt"
	"strings"

	"github.com/fclaude/nodwyn/internal/config"
	"github.com/fclaude/nodwyn/internal/guard"
	"github.com/fclaude/nodwyn/internal/log"
	"github.com/fclaude/nodwyn/internal/prim"
	"github.com/fclaude/nodwyn/internal/scheme"
)

// judgeConfirmer answers a session's confirms with the judge (Jev) instead of
// a person: a run started with --judge (ADR 18 §5, ADR 20 Seam C). Each
// ordinary confirm is put to the judge as a choice between allowing and
// refusing the call, and the call runs only if the judge's probability of
// allow reaches jev.approve_threshold. A security confirm is declined
// without asking: those exist for the case where a person is the control.
// Whatever goes wrong is a no.
//
// The decision is Go's, not Scheme's: what the judge is shown, what it is
// asked, how its answer is read and the threshold (from the user's config)
// are all here, so no Scheme, a repository's .nodwyn/init.scm included, can
// turn a refusal into an approval. Scheme may only refuse more: if
// (approve-refuse? call) is defined and returns true, or fails, a call the
// judge allowed is declined.
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

// The question the judge answers about a call, and the two answers it
// chooses between.
const (
	approveQuestion = "An autonomous coding agent is working on the task below without a person watching, and asks to make the call described. Should the call be allowed? Allow it only if the task and the plan call for it, its effects stay within what they need, and it does nothing a person would want to decide: deleting or overwriting unrelated work, publishing or pushing, spending money, contacting others, or weakening security."
	approveAllow    = "the task and the plan call for this call, and it is safe to make without a person"
	approveDeny     = "the call is not clearly called for, or could do harm a person should approve"
)

// judgeCall asks the judge about p and decides. The question goes through
// the jev-choice primitive from the primitive table, with the jev-key
// handle Go holds and harness origin, so it crosses the guard as the plan
// gate's calls do: the key's binding is checked, what is sent is redacted
// and jev.req is logged.
func (s *Session) judgeCall(ctx context.Context, p guard.Prompt) (bool, string) {
	choose, ok := s.interp.Lookup("jev-choice")
	h := s.HandleForClass(prim.JevClass)
	if !ok || h == nil {
		return false, "no judge is configured"
	}
	options := scheme.List(
		scheme.Cons(scheme.Str("allow"), scheme.Str(approveAllow)),
		scheme.Cons(scheme.Str("deny"), scheme.Str(approveDeny)),
	)
	harness := scheme.WithOrigin(ctx, scheme.OriginHarness)
	answer, err := s.interp.Apply(harness, scheme.Value{Kind: scheme.KPrim, Prim: choose},
		[]scheme.Value{scheme.HandleValue(h), scheme.Str(s.approveState(p)), scheme.Str(approveQuestion), options})
	if err != nil {
		return false, "the judge did not answer: " + err.Error()
	}
	allow := allowProbability(answer)
	threshold := s.opts.Config.Jev.ApproveThreshold
	if threshold == 0 {
		threshold = config.Default().Jev.ApproveThreshold
	}
	why := fmt.Sprintf("the judge's probability of allow is %.2f, %s %.2f", allow, map[bool]string{true: "at least", false: "below"}[allow >= threshold], threshold)
	if allow < threshold {
		return false, why
	}
	if refused, reason := s.approveRefused(harness, p); refused {
		return false, why + "; " + reason
	}
	return true, why
}

// approveState is what the judge is shown about p (ADR 19): the task, the
// turn's plan and the call's shape — the tool, why the guard asks and its
// target, the path a write names or the command line a program runs — and
// at jev.content = "full" also what the call would write or send.
func (s *Session) approveState(p guard.Prompt) string {
	task, plan := s.turnTaskAndPlan()
	if strings.TrimSpace(plan) == "" {
		plan = "(none recorded)"
	}
	target := ""
	switch {
	case p.Path != "":
		target = " on " + p.Path
	case len(p.Argv) > 0:
		target = ": " + strings.Join(p.Argv, " ")
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Task:\n%s\n\nPlan:\n%s\n\nCall: %s%s\n", task, plan, p.Prim, target)
	if len(p.Reasons) > 0 {
		fmt.Fprintf(&b, "Why it needs approval: %s\n", strings.Join(p.Reasons, "; "))
	}
	if jevOptions(s.opts.Config.Jev).Content == config.JevContentFull {
		what := p.Detail
		if what == "" {
			what = p.Args
		}
		fmt.Fprintf(&b, "\nWhat it would do:\n%s\n", what)
	}
	return b.String()
}

// allowProbability reads the judge's probability of allow from a
// jev-choice answer: its probability for "allow", or its confidence when it
// chose allow without probabilities, else 0.
func allowProbability(answer scheme.Value) float64 {
	if probs, ok := alistGet(answer, "probabilities"); ok {
		if p, ok := alistGet(probs, "allow"); ok && p.IsNumber() {
			return p.AsFloat()
		}
	}
	if c, ok := alistGet(answer, "choice"); ok && c.Kind == scheme.KString && c.S == "allow" {
		if conf, ok := alistGet(answer, "confidence"); ok && conf.IsNumber() {
			return conf.AsFloat()
		}
	}
	return 0
}

// approveRefused applies the optional (approve-refuse? call) hook to a call
// the judge allowed. It can only refuse: true, or an error, declines; it is
// never asked about a call the judge refused.
func (s *Session) approveRefused(ctx context.Context, p guard.Prompt) (bool, string) {
	hook, ok := s.interp.Global().Lookup("approve-refuse?")
	if !ok {
		return false, ""
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
	)
	v, err := s.interp.Apply(ctx, hook, []scheme.Value{call})
	switch {
	case err != nil:
		return true, "approve-refuse? failed: " + err.Error()
	case v.IsTrue():
		return true, "approve-refuse? refused it"
	}
	return false, ""
}

// alistGet finds key, a symbol or a string, in an alist.
func alistGet(v scheme.Value, key string) (scheme.Value, bool) {
	for v.Kind == scheme.KPair {
		if e := v.P.Car; e.Kind == scheme.KPair && (e.P.Car.Kind == scheme.KSymbol || e.P.Car.Kind == scheme.KString) && e.P.Car.S == key {
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
