package core

import (
	"strings"

	"github.com/fclaude/nodwyn/internal/log"
	"github.com/fclaude/nodwyn/internal/scheme"
)

// The plan gate is Scheme (internal/loop/plan.scm): the judge's questions,
// the drafts and revisions, and what to record are the strategy's. What the
// core keeps is one invariant for turns that must plan (SendPlanned, as a
// run's turns are): nothing irreversible runs before a plan is recorded, and
// a turn that records none fails.

// registerPlanRequired adds (plan-required?), which tells a strategy that
// this turn must plan before it acts, and (plan-recorded?), whether it has.
func (s *Session) registerPlanRequired() error {
	for _, p := range []*scheme.Prim{
		{
			Name: "plan-required?",
			Meta: scheme.Meta{
				Doc:    "True when this turn must record a plan (record-plan) before anything irreversible may run: a supervised run's turns must.",
				Hidden: true,
				Origin: "builtin",
			},
			Handler: func(*scheme.Call) (scheme.Value, error) {
				return scheme.Bool(s.planRequired.Load()), nil
			},
		},
		{
			Name: "unattended?",
			Meta: scheme.Meta{
				Doc:    "True when nobody approves this session's calls: it answers ordinary confirms itself (--yolo, a run's auto_approve), or nobody is there at all (an unattended run). The default prompts and loop work differently then.",
				Hidden: true,
				Origin: "builtin",
			},
			Handler: func(*scheme.Call) (scheme.Value, error) {
				return scheme.Bool(s.opts.unattended()), nil
			},
		},
		{
			Name: "sandbox?",
			Meta: scheme.Meta{
				Doc:    "True in sandbox mode (nodwyn run --sandbox container, ADR 54): the machine is the session's disposable sandbox, every path but nodwyn's own directories and credential files is the tools', and nobody approves anything. The lean prompt says so, and the default loop does not tell a turn that no file changed, since a command may change any file on the machine.",
				Hidden: true,
				Origin: "builtin",
			},
			Handler: func(*scheme.Call) (scheme.Value, error) {
				return scheme.Bool(s.opts.Sandbox), nil
			},
		},
		{
			Name: "reviewed?",
			Meta: scheme.Meta{
				Doc:    "True when a person reads this turn's answer before anything else happens: a chat's turn, and a supervised run's turn that a run checkpoint follows. False in an unattended run, in a run's last turn, and outside a turn. The default loop then lets a turn that changes nothing end, if the model says the request asks for no change yet.",
				Hidden: true,
				Origin: "builtin",
			},
			Handler: func(*scheme.Call) (scheme.Value, error) {
				return scheme.Bool(s.reviewed.Load()), nil
			},
		},
		{
			Name: "plan-recorded?",
			Meta: scheme.Meta{
				Doc:    "True once this turn has recorded a non-empty plan.",
				Hidden: true,
				Origin: "builtin",
			},
			Handler: func(*scheme.Call) (scheme.Value, error) {
				return scheme.Bool(s.planned.Load()), nil
			},
		},
	} {
		if err := s.interp.Register(p); err != nil {
			return err
		}
	}
	return nil
}

// notePlan marks the turn planned when a non-empty plan is recorded, and
// keeps the plan and the marks made on it for the judge.
func (s *Session) notePlan(kind log.Kind, payload any) {
	switch p := payload.(type) {
	case log.GatePlan:
		if kind == log.KindGatePlan && strings.TrimSpace(p.Plan) != "" {
			s.planned.Store(true)
			s.setTurnPlan(p.Plan, p.Marks)
		}
	case log.PlanStep:
		if kind == log.KindPlanStep {
			s.markTurnPlan(p)
		}
	}
}

// unplanned refuses an irreversible primitive in a turn that must plan and
// has not recorded a plan yet.
func (s *Session) unplanned(p *scheme.Prim) error {
	if !p.Meta.Irreversible || !s.planRequired.Load() || s.planned.Load() {
		return nil
	}
	return &scheme.Denied{Prim: p.Name, Reason: "this turn must record a plan (record-plan) before it changes anything"}
}
