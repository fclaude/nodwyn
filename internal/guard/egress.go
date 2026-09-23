package guard

import (
	"context"

	"github.com/fclaude/nibb/internal/binding"
	"github.com/fclaude/nibb/internal/log"
	"github.com/fclaude/nibb/internal/scheme"
)

// Egress is the E3 binding check: the egress authority (SPEC). It runs on
// every secret-bearing primitive application and decides, against the
// binding table, whether the credential may travel to the destination.
//
// The decision is deny or confirm when the call is unbound — the choice
// between them is the policy default (ADR 6, config.policy.unbound). A call
// that is bound, or that carries no credential, is allowed here; the guard's
// strictest-wins merge still lets a Deny from any other checker stand.
//
// The guard records the lookup as an egress.check event so a binding failure
// is auditable independent of the guard.decision.
type Egress struct {
	Table    binding.Authority
	Policy   string // "deny" (default) or "confirm" for unbound secret egress
	Recorder Logger // receives the egress.check event; nil discards (tests)
}

// Name identifies the checker in the log.
func (Egress) Name() string { return "egress" }

// Check returns the binding verdict for one application.
func (e Egress) Check(_ context.Context, p *scheme.Prim, args []scheme.Value) Decision {
	if e.Table == nil {
		e.Table = binding.Table{}
	}
	f := egressFactsOf(p, args)
	if !f.HasCredential {
		// Not a secret-bearing egress: this checker has no opinion.
		return Decision{Verdict: Allow}
	}
	bound, reason := e.Table.Lookup(f.Class, f.Host, f.Method, f.Position)
	rec := log.EgressCheck{Prim: p.Name, Class: f.Class, Host: f.Host, Method: f.Method, Bound: bound, Reason: reason}
	e.record(rec)
	if !bound {
		verdict := Deny
		if e.Policy == "confirm" {
			verdict = Confirm
		}
		d := Decision{Verdict: verdict, Reason: reason}
		if verdict == Confirm {
			if row, err := binding.Exact(f.Class, f.Host, f.Method, f.Position); err == nil {
				d.Binding = &row
			}
		}
		return d
	}
	return Decision{Verdict: Allow, Reason: "bound"}
}

func (e Egress) record(rec log.EgressCheck) {
	if e.Recorder != nil {
		_, _ = e.Recorder.Record(log.KindEgressCheck, rec)
	}
}
