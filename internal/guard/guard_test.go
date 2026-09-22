package guard

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/fclaude/nibb/internal/binding"
	"github.com/fclaude/nibb/internal/scheme"
)

type fixed struct {
	name string
	d    Decision
}

func (f fixed) Name() string                                                 { return f.name }
func (f fixed) Check(context.Context, *scheme.Prim, []scheme.Value) Decision { return f.d }

var prim = &scheme.Prim{Name: "p", Meta: scheme.Meta{Doc: "d"}}

func TestStrictestVerdictWins(t *testing.T) {
	asked := 0
	yes := ConfirmFunc(func(context.Context, Prompt) (bool, error) { asked++; return true, nil })

	g := New(yes, nil, fixed{"a", Decision{Verdict: Allow}}, fixed{"b", Decision{Verdict: Confirm, Reason: "b says confirm"}}, fixed{"c", Decision{Verdict: Allow}})
	if err := g.Before(context.Background(), prim, nil); err != nil || asked != 1 {
		t.Fatalf("confirm path: err=%v asked=%d", err, asked)
	}

	g = New(yes, nil, fixed{"b", Decision{Verdict: Confirm, Reason: "x"}}, fixed{"d", Decision{Verdict: Deny, Reason: "d says deny"}}, fixed{"e", Decision{Verdict: Confirm, Reason: "never reached"}})
	err := g.Before(context.Background(), prim, nil)
	var d *scheme.Denied
	if !errors.As(err, &d) || d.Reason != "d says deny" || asked != 1 {
		t.Fatalf("deny must win without prompting: %v asked=%d", err, asked)
	}
}

func TestNilConfirmerDenies(t *testing.T) {
	g := New(nil, nil, fixed{"b", Decision{Verdict: Confirm, Reason: "needs a yes"}})
	err := g.Before(context.Background(), prim, nil)
	if err == nil || !strings.Contains(err.Error(), "not confirmed") {
		t.Fatalf("%v", err)
	}
}

func TestConfirmerCancellationPropagates(t *testing.T) {
	blocking := ConfirmFunc(func(ctx context.Context, _ Prompt) (bool, error) { <-ctx.Done(); return false, ctx.Err() })
	g := New(blocking, nil, fixed{"b", Decision{Verdict: Confirm, Reason: "r"}})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := g.Before(ctx, prim, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("want Canceled, got %v", err)
	}
}

func TestArgsSummaryTruncates(t *testing.T) {
	long := scheme.Str(strings.Repeat("x", 500))
	s := summarizeArgs([]scheme.Value{long, scheme.Int(1)})
	if len(s) > 140 || !strings.Contains(s, "...") {
		t.Fatalf("summary not truncated: %d", len(s))
	}
}

func TestStandingApprovalOutranksConfirmNotDeny(t *testing.T) {
	shell := &scheme.Prim{Name: "shell-exec", Meta: scheme.Meta{Doc: "d", Irreversible: true, Params: []scheme.Param{
		{Name: "cmd", Type: "string", Role: "argv"}, {Name: "args", Type: "list", Role: "argv"},
	}}}
	argv := func(prog string, rest ...string) []scheme.Value {
		items := make([]scheme.Value, len(rest))
		for i, r := range rest {
			items[i] = scheme.Str(r)
		}
		return []scheme.Value{scheme.Str(prog), scheme.List(items...)}
	}
	allow := NewCommandAllow("go test", "git status")
	asked := 0
	g := New(ConfirmFunc(func(context.Context, Prompt) (bool, error) { asked++; return false, nil }), nil, MetaPolicy{}, DefaultArgvTrigger("/repo"), allow)

	if err := g.Before(context.Background(), shell, argv("go", "test", "./...")); err != nil || asked != 0 {
		t.Fatalf("go test should pass on standing approval: %v asked=%d", err, asked)
	}
	if err := g.Before(context.Background(), shell, argv("/usr/local/go/bin/go", "test")); err != nil {
		t.Fatalf("basename match: %v", err)
	}
	if err := g.Before(context.Background(), shell, argv("go", "build")); err == nil || asked != 1 {
		t.Fatalf("go build is not approved: %v asked=%d", err, asked)
	}
	allow.Add("go build")
	if err := g.Before(context.Background(), shell, argv("go", "build")); err != nil {
		t.Fatalf("after Add: %v", err)
	}
	// A deny still wins: the Egress checker denies an unbound secret-bearing
	// call, and a standing approval must not override it.
	egress := &scheme.Prim{Name: "http", Meta: scheme.Meta{Doc: "d", Egress: true, Params: []scheme.Param{
		{Name: "cred", Type: "any", Role: "credential"}, {Name: "url", Type: "string", Role: "dest"},
	}}}
	eg := Egress{Table: binding.Table{}} // empty table: nothing is bound
	g = New(ConfirmFunc(func(context.Context, Prompt) (bool, error) { return false, nil }), nil, MetaPolicy{}, DefaultArgvTrigger("/repo"), allow, eg)
	allow.Add("http")
	if err := g.Before(context.Background(), egress, []scheme.Value{scheme.Value{Kind: scheme.KHandle, H: &scheme.Handle{Class: "openai-key", ID: "ab"}}, scheme.Str("https://api.openai.com/v1/chat")}); err == nil {
		t.Fatal("standing approval must not override an egress deny")
	}
	if got := strings.Join(allow.List(), ","); got != "git status,go build,go test,http" {
		t.Fatalf("list: %s", got)
	}
	for argvLine, want := range map[string]string{"go test ./x": "go test", "rg -n foo": "rg", "git status": "git status", "./bin/nibb version": "nibb version", "sh -c ls": "sh"} {
		if got := Prefix(strings.Fields(argvLine)); got != want {
			t.Errorf("Prefix(%q) = %q, want %q", argvLine, got, want)
		}
	}
}
