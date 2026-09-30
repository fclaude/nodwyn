package guard

import (
	"context"
	"strings"

	"github.com/fclaude/nodwyn/internal/binding"
	"github.com/fclaude/nodwyn/internal/scheme"
)

// Taint is the L2 checker (SPEC E3, Phase 6). It reads the provenance tags
// the interpreter propagates and decides three things:
//
//   - a taint-stripping primitive (wipe, box) called from a model-originated
//     evaluation is denied: L2 exists to stop the model, and the escape
//     hatch belongs to the user (SPEC Risk 1);
//   - a value carrying a credential class into an egress *payload* — rather
//     than into the credential slot, where the binding check handles it — is
//     allowed only if that class is bound for the destination;
//   - content that came from outside (a file, a command, a response) heading
//     for an egress destination with no binding at all is denied by policy,
//     unless the person approved that destination for this primitive
//     another way (EgressApproved: an MCP server's host); heading into a
//     command line, it is a security confirm naming the sources, because
//     building a command out of tool output is the classic
//     indirect-injection path.
//
// When L2 decides, L3 is not paid for (SPEC's ladder principle): this
// checker runs before the learned layer and a decision here is final.
type Taint struct {
	Table  binding.Authority
	Policy string // "deny" (default) or "confirm" for unbound external egress
}

// Name identifies the checker in the log.
func (Taint) Name() string { return "l2" }

// Check inspects one application.
func (t Taint) Check(ctx context.Context, p *scheme.Prim, args []scheme.Value) Decision {
	if t.Table == nil {
		t.Table = binding.Table{}
	}
	if p.Meta.TaintStrip {
		// Harness origin is refused too: an agent-turn strategy runs with it
		// and handles every tool result, so it must not launder them.
		if o := scheme.OriginFrom(ctx); o != scheme.OriginUser {
			return Decision{Verdict: Deny, Reason: p.Name + " is user-only: " + string(o) + "-originated code may not strip provenance"}
		}
		return Decision{Verdict: Allow, Reason: "provenance stripped by the user"}
	}

	payload, argv := taintedRoles(p, args)

	// A credential class in a payload is judged like the handle itself.
	if classes := payload.Creds(); len(classes) > 0 && p.Meta.Egress {
		facts := egressFactsOf(p, args)
		for _, class := range classes {
			if bound, _ := t.Table.Lookup(class, facts.Host, facts.Method, ""); !bound {
				return Decision{Verdict: Deny, Reason: "a value derived from credential class " + class + " is not bound for " + destOf(facts)}
			}
		}
	}

	if ext := payload.Externals(); len(ext) > 0 && p.Meta.Egress {
		facts := egressFactsOf(p, args)
		from := "content from " + strings.Join(ext, ", ")
		switch {
		case t.Table.HasDestination(facts.Host):
			return Decision{Verdict: Allow, Reason: from + " to the bound destination " + facts.Host}
		case approvedDest(p, facts.Host):
			return Decision{Verdict: Allow, Reason: from + " to the approved destination " + facts.Host}
		}
		verdict := Deny
		if t.Policy == "confirm" {
			verdict = Confirm
		}
		return Decision{Verdict: verdict, Reason: from + " is heading to " + destOf(facts) + ", which no binding authorises"}
	}

	if ext := argv.Externals(); len(ext) > 0 {
		return Decision{
			Verdict: Confirm, Security: true,
			Reason: "the command line is built from " + strings.Join(ext, ", ") + " (tool output), not from the model's own words",
		}
	}
	return Decision{Verdict: Allow}
}

// approvedDest reports whether host is the primitive's own fixed
// destination and the person approved it outside the binding table.
func approvedDest(p *scheme.Prim, host string) bool {
	return p.Meta.EgressApproved && host != "" && host == strings.ToLower(p.Meta.EgressHost)
}

func destOf(f egressFacts) string {
	if f.Host == "" {
		return "an unnamed destination"
	}
	return f.Host
}

// taintedRoles returns the union of the taint carried by payload-role and
// argv-role arguments.
func taintedRoles(p *scheme.Prim, args []scheme.Value) (payload, argv scheme.Taint) {
	for i, pa := range p.Meta.Params {
		if i >= len(args) {
			break
		}
		switch pa.Role {
		case "payload":
			payload = scheme.Union(payload, deepTaint(args[i]))
		case "argv":
			argv = scheme.Union(argv, deepTaint(args[i]))
		}
	}
	return payload, argv
}

// deepTaint is the taint of a value and, for a list, of everything in it:
// a list of strings built from tool output carries the taint on the
// strings even when the list itself was assembled cleanly. Pairs are
// followed through their cdrs too, so an alist's values (a JSON object, as
// an MCP argument) count. Pairs are immutable, so the walk ends.
func deepTaint(v scheme.Value) scheme.Taint {
	t := v.Taint
	for v.Kind == scheme.KPair {
		t = scheme.Union(t, deepTaint(v.P.Car))
		v = v.P.Cdr
		t = scheme.Union(t, v.Taint)
	}
	return t
}
