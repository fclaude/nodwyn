package guard

import (
	"context"
	"errors"
	"strings"
	"testing"

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
