package guard

import (
	"context"
	"strings"

	"github.com/fclaude/nodwyn/internal/binding"
	"github.com/fclaude/nodwyn/internal/detect/l1"
	"github.com/fclaude/nodwyn/internal/scheme"
)

// TextSecret is the L1 checker (Phase 4): it scans the text a primitive is
// about to send or run for credentials the model wrote literally rather
// than through a handle. It reads roles, not primitive names:
//
//   - payload text of an Egress primitive (http-request headers and body,
//     every argument of an MCP tool): a known class is allowed iff bound
//     for the destination host and method, otherwise denied naming the
//     class — the same authority the handle path uses (E3), applied to
//     literal text;
//   - argv of a command line: a known class is denied — a command can reach
//     the network and the harness cannot see where the bytes go;
//   - payload of an in-repository write (fs-write, fs-edit): a known class
//     is a Security confirm — not egress, but the user should see it, and
//     a standing write approval cannot waive it.
//
// The unknown-entropy residual never produces a verdict here: by the spec
// it "feeds the decision with binding, never decides alone", and that is
// L3's job (P8). Findings of that class are ignored for the verdict.
type TextSecret struct {
	Table   binding.Authority
	Scanner *l1.Scanner
}

// Name identifies the checker in the log.
func (TextSecret) Name() string { return "l1" }

// Check scans one application.
func (ts TextSecret) Check(_ context.Context, p *scheme.Prim, args []scheme.Value) Decision {
	if ts.Table == nil {
		ts.Table = binding.Table{}
	}
	payload, argv := secretText(p, args)
	s := ts.Scanner
	if s == nil {
		s = l1.New()
	}
	if f, ok := firstKnown(s.Scan(argv)); ok {
		return Decision{Verdict: Deny, Reason: "secret of class " + f.Class + " on the command line"}
	}
	f, ok := firstKnown(s.Scan(payload))
	if !ok {
		return Decision{Verdict: Allow}
	}
	if p.Meta.Egress {
		facts := egressFactsOf(p, args)
		bound, reason := ts.Table.Lookup(f.Class, facts.Host, facts.Method, "")
		if bound {
			return Decision{Verdict: Allow, Reason: "literal " + f.Class + " is bound for " + facts.Host}
		}
		return Decision{Verdict: Deny, Reason: "literal secret of class " + f.Class + " in the request is not bound for " + facts.Host + " (" + reason + ")"}
	}
	return Decision{Verdict: Confirm, Security: true, Reason: "the written content contains what looks like a " + f.Class}
}

// firstKnown returns the first finding that is a known class (never the
// unknown-entropy residual).
func firstKnown(fs []l1.Finding) (l1.Finding, bool) {
	for _, f := range fs {
		if f.Class != l1.ClassUnknownEntropy {
			return f, true
		}
	}
	return l1.Finding{}, false
}

// secretText collects the payload-role and argv-role text of an
// application: every string in it, however nested (header pairs, argv
// lists, and the alists of a JSON object, as an MCP argument).
func secretText(p *scheme.Prim, args []scheme.Value) (payload, argv string) {
	var pb, ab strings.Builder
	for i, pa := range p.Meta.Params {
		if i >= len(args) {
			break
		}
		var b *strings.Builder
		switch pa.Role {
		case "payload":
			b = &pb
		case "argv":
			b = &ab
		default:
			continue
		}
		collect(b, args[i])
	}
	return pb.String(), ab.String()
}

// collect writes the strings in v, following pairs through their cars and
// cdrs: an alist's values are in the cdrs. Pairs are immutable, so the walk
// ends; vectors are not entered (they cannot be sent as JSON either).
func collect(b *strings.Builder, v scheme.Value) {
	for v.Kind == scheme.KPair {
		collect(b, v.P.Car)
		v = v.P.Cdr
	}
	if v.Kind == scheme.KString {
		b.WriteString(v.S)
		b.WriteByte('\n')
	}
}
