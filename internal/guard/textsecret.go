package guard

import (
	"context"
	"strings"

	"github.com/fclaude/brote/scheme"
	"github.com/fclaude/nodwyn/internal/binding"
	"github.com/fclaude/nodwyn/internal/detect/l1"
)

// TextSecret is the L1 checker (Phase 4): it scans the text a primitive is
// about to send or run for credentials the model wrote literally rather
// than through a handle. It reads roles, not primitive names:
//
//   - payload text of an Egress primitive (http-request headers and body,
//     every argument of an MCP tool): a known class is allowed iff bound
//     for the destination host and method (for a contained MCP server,
//     for every host it can reach), otherwise denied naming the class —
//     the same authority the handle path uses (E3), applied to literal
//     text. Every credential in the text must be bound, not only the
//     first: a key bound for the host does not take a private key along;
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
	s := ts.scanner()
	if f, ok := firstKnown(s.Scan(argv)); ok {
		return Decision{Verdict: Deny, Reason: "secret of class " + f.Class + " on the command line"}
	}
	if p.Meta.Egress {
		facts := egressFactsOf(p, args)
		if p.Meta.EgressContained {
			// A contained server can send on to every host on its
			// network list: a literal key goes only if its class is bound
			// for all of them, and nowhere when it has no network.
			fs := s.Scan(payload)
			first, ok := firstKnown(fs)
			if !ok {
				return Decision{Verdict: Allow}
			}
			if f, reason, found := firstUnbound(fs, func(class string) (bool, string) { return boundFor(ts.Table, p, facts, class) }); found {
				return Decision{Verdict: Deny, Reason: "literal secret of class " + f.Class + " in the request is not bound for " + destOf(facts) + " (" + reason + ")"}
			}
			return Decision{Verdict: Allow, Reason: "literal " + first.Class + " is bound for " + destOf(facts)}
		}
		return ts.Outgoing(payload, facts.Host, facts.Method)
	}
	f, ok := firstKnown(s.Scan(payload))
	if !ok {
		return Decision{Verdict: Allow}
	}
	return Decision{Verdict: Confirm, Security: true, Reason: "the written content contains what looks like a " + f.Class}
}

// Outgoing judges text about to be sent to host by method, as Check judges
// an egress primitive's payload: a known class is allowed iff it is bound
// for that host and method, otherwise denied naming the class. A primitive
// that sends what never was a Scheme value — a document's bytes, which
// the guard does not see — asks it about that text before it sends.
func (ts TextSecret) Outgoing(text, host, method string) Decision {
	fs := ts.scanner().Scan(text)
	first, ok := firstKnown(fs)
	if !ok {
		return Decision{Verdict: Allow}
	}
	table := ts.Table
	if table == nil {
		table = binding.Table{}
	}
	f, reason, found := firstUnbound(fs, func(class string) (bool, string) { return table.Lookup(class, host, method, "") })
	if found {
		return Decision{Verdict: Deny, Reason: "literal secret of class " + f.Class + " in the request is not bound for " + host + " (" + reason + ")"}
	}
	return Decision{Verdict: Allow, Reason: "literal " + first.Class + " is bound for " + host}
}

func (ts TextSecret) scanner() *l1.Scanner {
	if ts.Scanner == nil {
		return l1.New()
	}
	return ts.Scanner
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

// firstUnbound returns the first known finding whose class bound refuses, and
// why. Findings come in order of appearance, so judging only the first would
// let a credential bound for the host carry along whatever follows it.
func firstUnbound(fs []l1.Finding, bound func(class string) (bool, string)) (l1.Finding, string, bool) {
	for _, f := range fs {
		if f.Class == l1.ClassUnknownEntropy {
			continue
		}
		if ok, reason := bound(f.Class); !ok {
			return f, reason, true
		}
	}
	return l1.Finding{}, "", false
}

// secretText collects the payload-role and argv-role text of an
// application: every string in it, however nested (header pairs, argv
// lists, and the alists of a JSON object, as an MCP argument), and the
// fields with those roles in a list parameter's elements (each new text of
// fs-edit's edits, not the old text it replaces).
func secretText(p *scheme.Prim, args []scheme.Value) (payload, argv string) {
	var pb, ab strings.Builder
	p.EachRole(args, func(role string, v scheme.Value) {
		switch role {
		case "payload":
			collect(&pb, v)
		case "argv":
			collect(&ab, v)
		}
	})
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
