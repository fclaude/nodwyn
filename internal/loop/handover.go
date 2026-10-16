package loop

import (
	"context"
	"github.com/fclaude/nodwyn/internal/budget"
	"github.com/fclaude/nodwyn/internal/scheme"
)

// Handover reuses compact-note without applying compact-context's narrowing.
// The caller owns the interpreter for its whole turn and retains the budget.
func (l *Loop) Handover(ctx context.Context, focus string) (string, error) {
	l.turn = &turnState{budget: budget.From(ctx)}
	defer func() { l.turn = nil }()
	proc, err := l.evalPrompt("compact-note")
	if err != nil {
		return "", err
	}
	v, err := l.interp.Apply(scheme.WithOrigin(ctx, scheme.OriginHarness), proc, []scheme.Value{scheme.Str(focus)})
	if unwind := l.unwind(); err == nil {
		err = unwind
	}
	if skipped := l.skipPending("the handover ended without running tools"); err == nil {
		err = skipped
	}
	if err != nil {
		return "", err
	}
	if v.Kind == scheme.KString {
		return v.S, nil
	}
	return "", nil
}
