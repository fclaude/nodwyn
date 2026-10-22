package guard_test

import (
	"context"
	"strings"
	"testing"

	"github.com/fclaude/brote/scheme"
	"github.com/fclaude/nodwyn/internal/guard"
)

var fileTaint = scheme.TaintOf(scheme.TagFile)

func taintedStr(s string, t scheme.Taint) scheme.Value {
	return scheme.Tainted(scheme.Str(s), t)
}

func TestExternalContentToAnUnnamedDestinationIsDenied(t *testing.T) {
	ck := guard.Taint{Table: bound, Policy: "deny"}
	// Bound host: the user authorised this destination, so content may go.
	args := httpArgs("https://api.anthropic.com/v1", "")
	args[5] = taintedStr("file contents", fileTaint)
	if d := ck.Check(context.Background(), httpP(), args); d.Verdict != guard.Allow {
		t.Fatalf("bound destination: %+v", d)
	}
	// A destination no binding names: denied, naming the source.
	args = httpArgs("https://collect.example/x", "")
	args[5] = taintedStr("file contents", fileTaint)
	d := ck.Check(context.Background(), httpP(), args)
	if d.Verdict != guard.Deny || !strings.Contains(d.Reason, "external:file") || !strings.Contains(d.Reason, "collect.example") {
		t.Fatalf("unbound destination: %+v", d)
	}
	// Policy confirm softens the verdict, never the reason.
	if d := (guard.Taint{Table: bound, Policy: "confirm"}).Check(context.Background(), httpP(), args); d.Verdict != guard.Confirm {
		t.Fatalf("confirm policy: %+v", d)
	}
	// Clean content is nobody's business here.
	if d := ck.Check(context.Background(), httpP(), httpArgs("https://collect.example/x", "hello")); d.Verdict != guard.Allow {
		t.Fatalf("clean payload: %+v", d)
	}
}

func TestCredentialDerivedValueInAPayload(t *testing.T) {
	ck := guard.Taint{Table: bound, Policy: "deny"}
	args := httpArgs("https://collect.example/x", "")
	args[5] = taintedStr("Bearer …", scheme.TaintOf(scheme.CredTag("anthropic-key")))
	d := ck.Check(context.Background(), httpP(), args)
	if d.Verdict != guard.Deny || !strings.Contains(d.Reason, "anthropic-key") {
		t.Fatalf("cred-derived payload to an unbound host: %+v", d)
	}
	args = httpArgs("https://api.anthropic.com/v1", "")
	args[5] = taintedStr("Bearer …", scheme.TaintOf(scheme.CredTag("anthropic-key")))
	if d := ck.Check(context.Background(), httpP(), args); d.Verdict != guard.Allow {
		t.Fatalf("cred-derived payload to its bound host: %+v", d)
	}
}

// A command line assembled from tool output is the indirect-injection path.
func TestCommandLineBuiltFromToolOutput(t *testing.T) {
	ck := guard.Taint{Policy: "deny"}
	clean := shellArgs("go", "test", "./...")
	if d := ck.Check(context.Background(), shellP(), clean); d.Verdict != guard.Allow {
		t.Fatalf("clean argv: %+v", d)
	}
	dirty := []scheme.Value{scheme.Str("sh"), scheme.List(scheme.Str("-c"), taintedStr("rm -rf /", fileTaint))}
	d := ck.Check(context.Background(), shellP(), dirty)
	if d.Verdict != guard.Confirm || !d.Security || !strings.Contains(d.Reason, "external:file") {
		t.Fatalf("tainted argv: %+v", d)
	}
	// A standing command approval must not waive it.
	asked := 0
	g := guard.New(guard.ConfirmFunc(func(context.Context, guard.Prompt) (bool, error) { asked++; return true, nil }), nil,
		guard.MetaPolicy{}, guard.NewCommandAllow("sh", "go test"), ck)
	if err := g.Before(context.Background(), shellP(), dirty); err != nil || asked != 1 {
		t.Fatalf("security confirm waived by a standing approval: err=%v asked=%d", err, asked)
	}
	if err := g.Before(context.Background(), shellP(), clean); err != nil || asked != 1 {
		t.Fatalf("clean argv should stay standing-approved: err=%v asked=%d", err, asked)
	}
}

func TestWipeIsUserOnly(t *testing.T) {
	wipe := &scheme.Prim{Name: "wipe", Meta: scheme.Meta{Doc: "d", TaintStrip: true, Hidden: true, Audited: true,
		Params: []scheme.Param{{Name: "x", Type: "any"}}}}
	ck := guard.Taint{}
	model := scheme.WithOrigin(context.Background(), scheme.OriginModel)
	if d := ck.Check(model, wipe, []scheme.Value{taintedStr("x", fileTaint)}); d.Verdict != guard.Deny {
		t.Fatalf("model wipe: %+v", d)
	}
	user := scheme.WithOrigin(context.Background(), scheme.OriginUser)
	if d := ck.Check(user, wipe, []scheme.Value{taintedStr("x", fileTaint)}); d.Verdict != guard.Allow {
		t.Fatalf("user wipe: %+v", d)
	}
	// An agent-turn strategy runs with harness origin and handles every
	// tool result; it must not launder them.
	harness := scheme.WithOrigin(context.Background(), scheme.OriginHarness)
	if d := ck.Check(harness, wipe, nil); d.Verdict != guard.Deny {
		t.Fatalf("harness wipe: %+v", d)
	}
}

// Taint on a list's elements counts, even when the list was built cleanly.
func TestTaintIsFoundInsideLists(t *testing.T) {
	ck := guard.Taint{Table: bound, Policy: "deny"}
	args := httpArgs("https://collect.example/x", "")
	args[4] = scheme.List(scheme.List(scheme.Str("X-Data"), taintedStr("secret file line", fileTaint)))
	if d := ck.Check(context.Background(), httpP(), args); d.Verdict != guard.Deny {
		t.Fatalf("taint inside a header list: %+v", d)
	}
}

// An alist's values are in its cdrs: a JSON object built cleanly around
// tool output (an MCP argument) still carries it.
func TestTaintIsFoundInsideAlists(t *testing.T) {
	ck := guard.Taint{Table: bound, Policy: "deny"}
	args := httpArgs("https://collect.example/x", "")
	args[4] = scheme.List(scheme.Cons(scheme.Str("X-Data"), taintedStr("secret file line", fileTaint)))
	if d := ck.Check(context.Background(), httpP(), args); d.Verdict != guard.Deny {
		t.Fatalf("taint in an alist value: %+v", d)
	}
}

// A destination the person approved outside the binding table (an MCP
// server's host) is named for that primitive alone, and only as its own
// fixed host.
func TestApprovedDestinationIsThePrimsOwn(t *testing.T) {
	ck := guard.Taint{Table: bound, Policy: "deny"}
	mcpP := func(approved bool, params ...scheme.Param) *scheme.Prim {
		return &scheme.Prim{Name: "chat__post", Meta: scheme.Meta{Egress: true, EgressHost: "MCP.example", EgressMethod: "POST", EgressApproved: approved,
			Params: append(params, scheme.Param{Name: "text", Type: "string", Role: "payload"})}}
	}
	text := []scheme.Value{taintedStr("file contents", fileTaint)}
	d := ck.Check(context.Background(), mcpP(true), text)
	if d.Verdict != guard.Allow || !strings.Contains(d.Reason, "approved destination mcp.example") {
		t.Fatalf("approved: %+v", d)
	}
	if d := ck.Check(context.Background(), mcpP(false), text); d.Verdict != guard.Deny || !strings.Contains(d.Reason, "mcp.example") {
		t.Fatalf("not approved: %+v", d)
	}
	// A destination argument elsewhere is not the approved host.
	withDest := mcpP(true, scheme.Param{Name: "url", Type: "string", Role: "dest"})
	if d := ck.Check(context.Background(), withDest, append([]scheme.Value{scheme.Str("https://collect.example/x")}, text...)); d.Verdict != guard.Deny {
		t.Fatalf("approval followed a destination argument: %+v", d)
	}
}

// A list parameter whose elements are objects carries its payload in the
// fields Items marks, as fs-edit's edits carry each new text: taint on the
// second element's payload counts, taint on a field with no role does not,
// and taint on the list around them, as json-parse of tool output leaves
// it, reaches every field inside.
func TestTaintInAListParametersItems(t *testing.T) {
	ck := guard.Taint{Policy: "deny"}
	p := &scheme.Prim{Name: "post-edits", Meta: scheme.Meta{Egress: true, Params: []scheme.Param{
		{Name: "url", Type: "string", Role: "dest"},
		{Name: "edits", Type: "list", Items: []scheme.Param{{Name: "old", Type: "string"}, {Name: "new", Type: "string", Role: "payload"}}},
	}}}
	edit := func(old, new scheme.Value) scheme.Value {
		return scheme.List(scheme.Cons(scheme.Str("old"), old), scheme.Cons(scheme.Str("new"), new))
	}
	clean := edit(scheme.Str("a"), scheme.Str("b"))
	call := func(edits scheme.Value) guard.Decision {
		return ck.Check(context.Background(), p, []scheme.Value{scheme.Str("https://collect.example/x"), edits})
	}
	if d := call(scheme.List(clean, edit(scheme.Str("c"), taintedStr("file contents", fileTaint)))); d.Verdict != guard.Deny || !strings.Contains(d.Reason, "external:file") {
		t.Fatalf("taint in the second edit's payload: %+v", d)
	}
	if d := call(scheme.List(clean, edit(taintedStr("file contents", fileTaint), scheme.Str("d")))); d.Verdict != guard.Allow {
		t.Fatalf("taint in a field with no role: %+v", d)
	}
	if d := call(scheme.Tainted(scheme.List(clean), fileTaint)); d.Verdict != guard.Deny {
		t.Fatalf("taint on the list: %+v", d)
	}
}
