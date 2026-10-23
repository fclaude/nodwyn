package loop

import (
	"context"

	"github.com/fclaude/brote/scheme"
	"github.com/fclaude/nodwyn/internal/budget"
)

// TitleProcedure is the Scheme procedure the host asks for a task's title:
// (session-title request) returns a short title for the session's first
// message, or "" for none (agents.scm).
const TitleProcedure = "session-title"

// Title asks the model for a short title for request, through
// (session-title request), which asks it in a context of its own and folds
// it away, so the conversation never sees the question. The caller owns the
// interpreter for its whole turn, as for Handover, and the request counts
// against the turn's budget.
func (l *Loop) Title(ctx context.Context, request string) (string, error) {
	l.turn = &turnState{budget: budget.From(ctx)}
	defer func() { l.turn = nil }()
	proc, err := l.evalPrompt(TitleProcedure)
	if err != nil {
		return "", err
	}
	v, err := l.interp.Apply(scheme.WithOrigin(ctx, scheme.OriginHarness), proc, []scheme.Value{scheme.Str(request)})
	if unwind := l.unwind(); err == nil {
		err = unwind
	}
	if skipped := l.skipPending("the title was asked without tools"); err == nil {
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
