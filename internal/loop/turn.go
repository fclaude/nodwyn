package loop

import (
	"context"
	"fmt"
	"slices"

	"github.com/fclaude/nibb/internal/budget"
	"github.com/fclaude/nibb/internal/llm"
	"github.com/fclaude/nibb/internal/log"
	"github.com/fclaude/nibb/internal/scheme"
)

// turnState is what the primitives share during one turn.
type turnState struct {
	budget   *budget.Turn // nil when the session started no budget
	calls    int          // tool calls so far, for Config.MaxToolCalls
	progress turnProgress
	// pending holds the calls from the latest model response. Only an
	// unanswered one can be run or skipped, and answering it is final.
	pending []pendingCall
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

// reserveCall counts the next tool call against the turn's limits and
// returns the stop if it would exceed them. A budget records its own stop;
// the plain MaxToolCalls limit is recorded here, as a note.
func (l *Loop) reserveCall(ctx context.Context, t *turnState) error {
	if t.budget != nil {
		return t.budget.ToolCall(ctx)
	}
	t.calls++
	if t.calls <= l.cfg.MaxToolCalls {
		return nil
	}
	if err := l.record(log.KindNote, log.Note{Text: ErrHardStop.Error()}); err != nil {
		return err
	}
	return ErrHardStop
}
