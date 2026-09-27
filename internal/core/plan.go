package core

import (
	"strings"

	"github.com/fclaude/nibb/internal/log"
	"github.com/fclaude/nibb/internal/scheme"
)

// The plan gate is Scheme (internal/loop/plan.scm): the judge's questions,
// the drafts and revisions, and what to record are the strategy's. What the
// core keeps is one invariant for turns that must plan (SendPlanned, as a
// run's turns are): nothing irreversible runs before a plan is recorded, and
// a turn that records none fails.

// registerPlanRequired adds (plan-required?), which tells a strategy that
// this turn must plan before it acts.
func (s *Session) registerPlanRequired() error {
	return s.interp.Register(&scheme.Prim{
		Name: "plan-required?",
		Meta: scheme.Meta{
			Doc:    "True when this turn must record a plan (record-plan) before anything irreversible may run: a supervised run's turns must.",
			Hidden: true,
			Origin: "builtin",
		},
		Handler: func(*scheme.Call) (scheme.Value, error) {
			return scheme.Bool(s.planRequired.Load()), nil
		},
	})
}

// notePlan marks the turn planned when a non-empty plan is recorded.
func (s *Session) notePlan(kind log.Kind, payload any) {
	if kind != log.KindGatePlan {
		return
	}
	if gp, ok := payload.(log.GatePlan); ok && strings.TrimSpace(gp.Plan) != "" {
		s.planned.Store(true)
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
