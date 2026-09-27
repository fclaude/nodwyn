package loop

import (
	"context"
	"fmt"
	"slices"

	"github.com/fclaude/nibb/internal/budget"
	"github.com/fclaude/nibb/internal/llm"
	"github.com/fclaude/nibb/internal/scheme"
)

// turnState is what the primitives share during one turn.
type turnState struct {
	budget   *budget.Turn // nil when the session started no budget
	progress turnProgress
	// pending holds the calls from the latest model response. Only an
	// unanswered one can be run or skipped, and answering it is final.
	pending []pendingCall
	// contexts are the contexts narrowed during this turn, innermost last.
	// Each keeps the calls that were pending when it opened; they wait,
	// unanswered, until it folds (a sub-agent answering the model's call).
	contexts [][]pendingCall
}

// parked reports whether an open context holds calls still waiting for an
// answer: the transcript is well-formed only once they are answered after
// their context folds.
func (t *turnState) parked() bool {
	for _, calls := range t.contexts {
		for _, p := range calls {
			if !p.answered {
				return true
			}
		}
	}
	return false
}

type pendingCall struct {
	call     llm.ToolCall
	answered bool
}

// expect makes calls, from a new model response, the pending ones.
func (t *turnState) expect(calls []llm.ToolCall) {
	t.pending = t.pending[:0]
	for _, tc := range calls {
		t.pending = append(t.pending, pendingCall{call: tc})
	}
}

// unanswered counts the pending calls nobody has run or skipped yet.
func (t *turnState) unanswered() int {
	n := 0
	for _, p := range t.pending {
		if !p.answered {
			n++
		}
	}
	return n
}

// claim resolves call values from a strategy back to the calls the model
// made, and marks them answered. A call not in the latest response, one
// already answered, or one whose args were changed is refused: the log's
// "origin: model" must mean the model asked for exactly this, and no call
// runs twice. Claiming is all or nothing, so a refused call cannot leave
// the ones before it marked answered but never run.
func (t *turnState) claim(items []scheme.Value) ([]llm.ToolCall, error) {
	claimed := make([]*pendingCall, 0, len(items))
	for _, item := range items {
		id, name := field(item, "id"), field(item, "name")
		var match *pendingCall
		for i := range t.pending {
			p := &t.pending[i]
			if !p.answered && !slices.Contains(claimed, p) && p.call.ID == id && p.call.Name == name {
				match = p
				break
			}
		}
		if match == nil {
			return nil, fmt.Errorf("call %s (%s) is not an unanswered call from the latest model-turn", id, name)
		}
		if args, ok := lookup(item, "args"); ok && (args.Kind != scheme.KString || args.S != string(match.call.Args)) {
			return nil, fmt.Errorf("call %s (%s): args differ from what the model sent; a strategy may run or skip a call, not rewrite it", id, name)
		}
		claimed = append(claimed, match)
	}
	out := make([]llm.ToolCall, len(claimed))
	for i, p := range claimed {
		p.answered = true
		out[i] = p.call
	}
	return out, nil
}

// skipPending answers, as not executed, every call from the latest
// response that the strategy neither ran nor skipped. An unanswered call
// would make the next model request malformed.
func (l *Loop) skipPending(reason string) error {
	if l.turn == nil {
		return nil
	}
	var calls []llm.ToolCall
	for i := range l.turn.pending {
		if p := &l.turn.pending[i]; !p.answered {
			p.answered = true
			calls = append(calls, p.call)
		}
	}
	return l.skipTools(calls, reason)
}

// reserveCall counts the next tool call against the turn's budget and
// returns the stop if it would exceed it; the budget records its own stop.
// Every session turn has a budget, whose hard limit also carries the legacy
// loop.max_tool_calls.
func (l *Loop) reserveCall(ctx context.Context, t *turnState) error {
	if t.budget == nil {
		return nil
	}
	return t.budget.ToolCall(ctx)
}
