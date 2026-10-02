package guard_test

import (
	"context"
	"strings"
	"testing"

	"github.com/fclaude/nodwyn/internal/binding"
	"github.com/fclaude/nodwyn/internal/detect/l1"
	"github.com/fclaude/nodwyn/internal/guard"
	"github.com/fclaude/nodwyn/internal/scheme"
)

// A contained MCP server's tool sends to its container, which may pass
// the arguments on to the hosts of its network list: a literal key, or a
// value derived from a credential, is bound only if its class is bound
// for every one of them; with no network, for none. Content from outside
// may go, as to any server the person approved.
func TestContainedDestinationNeedsEveryHost(t *testing.T) {
	table := binding.Table{Bind: []binding.Binding{
		{Class: "anthropic-key", Dest: "api.notes.example", Positions: []string{"env:NOTES_TOKEN"}, Methods: []string{"*"}},
		{Class: "anthropic-key", Dest: "files.notes.example", Positions: []string{"header:x-api-key"}, Methods: []string{"POST"}},
	}}
	contained := func(onward ...string) *scheme.Prim {
		return &scheme.Prim{Name: "notes__write", Meta: scheme.Meta{Egress: true, EgressHost: "mcp:notes", EgressApproved: true, EgressContained: true, EgressOnward: onward,
			Params: []scheme.Param{{Name: "text", Type: "string", Role: "payload"}}}}
	}
	secrets := guard.TextSecret{Table: table, Scanner: l1.New()}
	l2 := guard.Taint{Table: table, Policy: "deny"}
	literal := []scheme.Value{scheme.Str("token " + anthropicKey)}
	derived := []scheme.Value{taintedStr("Bearer …", scheme.TaintOf(scheme.CredTag("anthropic-key")))}
	ctx := context.Background()

	for _, p := range []*scheme.Prim{contained(), contained("api.notes.example", "uploads.notes.example")} {
		if d := secrets.Check(ctx, p, literal); d.Verdict != guard.Deny || !strings.Contains(d.Reason, "mcp:notes") {
			t.Fatalf("L1, onward %v: %+v", p.Meta.EgressOnward, d)
		}
		if d := l2.Check(ctx, p, derived); d.Verdict != guard.Deny {
			t.Fatalf("L2, onward %v: %+v", p.Meta.EgressOnward, d)
		}
	}
	every := contained("api.notes.example", "FILES.notes.example")
	if d := secrets.Check(ctx, every, literal); d.Verdict != guard.Allow {
		t.Fatalf("L1, bound everywhere: %+v", d)
	}
	if d := l2.Check(ctx, every, derived); d.Verdict != guard.Allow {
		t.Fatalf("L2, bound everywhere: %+v", d)
	}
	// A binding naming the container itself binds nothing: the hosts do.
	byName := guard.TextSecret{Table: binding.Table{Bind: []binding.Binding{{Class: "anthropic-key", Dest: "mcp:notes", Positions: []string{"body"}, Methods: []string{"POST"}}}}, Scanner: l1.New()}
	if d := byName.Check(ctx, contained(), literal); d.Verdict != guard.Deny {
		t.Fatalf("bound by the container's name: %+v", d)
	}
	external := []scheme.Value{taintedStr("file contents", fileTaint)}
	if d := l2.Check(ctx, contained(), external); d.Verdict != guard.Allow || !strings.Contains(d.Reason, "approved destination mcp:notes") {
		t.Fatalf("external content: %+v", d)
	}
	// E3 has nothing to check: the call carries no credential.
	if d := (guard.Egress{Table: table}).Check(ctx, every, literal); d.Verdict != guard.Allow {
		t.Fatalf("E3: %+v", d)
	}
}
