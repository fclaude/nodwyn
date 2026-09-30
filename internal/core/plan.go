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
				Doc:    "True when nobody approves this session's calls: it answers ordinary confirms itself (--yolo, a run's auto_approve). The default prompts and loop work differently then.",
				Hidden: true,
				Origin: "builtin",
			},
			Handler: func(*scheme.Call) (scheme.Value, error) {
				return scheme.Bool(s.opts.AutoApprove), nil
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
