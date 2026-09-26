package loop

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/fclaude/nibb/internal/event"
	"github.com/fclaude/nibb/internal/llm"
	"github.com/fclaude/nibb/internal/log"
	"github.com/fclaude/nibb/internal/redact"
	"github.com/fclaude/nibb/internal/scheme"
	"github.com/fclaude/nibb/internal/window"
)

// The loop's primitives are hidden from the model and refused to
// model-originated evaluation: a tool body calling model-turn would re-enter
// the loop from inside a tool call.
func (l *Loop) install() error {
	calls := scheme.Param{Name: "calls", Type: "list", Doc: "tool calls from the latest model-turn"}
	for _, p := range []*scheme.Prim{
		{
			Name: "model-turn",
			Meta: scheme.Meta{
				Doc:    "Ask the control-plane model once, over the window built from the log. Returns an alist with keys content, thinking, done (the stop reason) and tool-calls; each call is an alist with keys id, name and args (the model's JSON text).",
				Hidden: true,
				Origin: "builtin",
			},
			Handler: l.modelTurn,
		},
		{
			Name: "run-tools",
			Meta: scheme.Meta{
				Doc:    "Run tool calls the model made, each through the guard with model origin. Returns one alist per call with keys id, name, ok, value (the text the model reads), error and denied.",
				Params: []scheme.Param{calls},
				Hidden: true,
				Origin: "builtin",
			},
			Handler: l.runTools,
		},
		{
			Name: "skip-tools",
			Meta: scheme.Meta{
				Doc:    "Answer tool calls without running them; the model reads \"Not executed: reason\".",
				Params: []scheme.Param{calls, {Name: "reason", Type: "string", Doc: "why the calls did not run"}},
				Hidden: true,
				Origin: "builtin",
			},
			Handler: l.skipToolsPrim,
		},
	} {
		if err := l.interp.Register(p); err != nil {
			return err
		}
	}
	return nil
}

// active returns the turn in progress, refusing calls outside a turn or
// from model-originated evaluation.
func (l *Loop) active(c *scheme.Call) (*turnState, error) {
	if c.Origin == scheme.OriginModel {
		return nil, errors.New("not callable from a model-originated evaluation")
	}
	if l.turn == nil {
		return nil, errors.New("only available while a turn is running")
	}
	return l.turn, nil
}

// halt makes a stop uncatchable by a strategy (scheme.Halt).
func halt(err error) error { return &scheme.Halt{Err: err} }

func (l *Loop) modelTurn(c *scheme.Call) (scheme.Value, error) {
	t, err := l.active(c)
	if err != nil {
		return scheme.Void, err
	}
	ctx := c.Ctx
	if ctx.Err() != nil {
		return scheme.Void, context.Cause(ctx)
	}
	if err := l.skipPending("the strategy asked the model again before running this call"); err != nil {
		return scheme.Void, err
	}
	if t.budget != nil {
		if err := t.budget.Check(ctx); err != nil {
			return scheme.Void, halt(err)
		}
	}
	w := window.Build(l.host.EventsSoFar(), l.cfg.System, l.toolsLen, l.cfg.Window)
	request := llm.Request{Model: l.cfg.Model, Messages: w.Messages, Tools: l.tools, NumCtx: l.cfg.NumCtx, KeepAlive: l.cfg.KeepAlive}
	digest, err := window.RequestHash(request)
	if err != nil {
		return scheme.Void, err
	}
	if _, err := l.host.Record(log.KindLLMReq, log.LLMReq{
		RequestHash: digest,
		Model:       l.cfg.Model, Messages: len(w.Messages), Tools: len(l.tools), EstTokens: w.EstTokens, Elided: w.Elided,
	}); err != nil {
		return scheme.Void, err
	}
	l.host.Emit(event.Event{Kind: event.Status, Payload: event.StatusInfo{State: "busy", Detail: "waiting for model"}})

	reqCtx, cancel := context.WithTimeout(ctx, l.cfg.RequestTimeout)
	resp, err := l.provider.Chat(reqCtx, request, nil)
	cancel()
	if err != nil {
		_, _ = l.host.Record(log.KindNote, log.Note{Text: "model request failed: " + err.Error()})
		return scheme.Void, err
	}
	respRec := log.LLMResp{
		Content: resp.Message.Content, Thinking: resp.Message.Thinking, DoneReason: resp.DoneReason,
		Usage: log.Usage{
			PromptTokens: resp.Usage.PromptTokens, OutputTokens: resp.Usage.OutputTokens,
			PromptEvalMS: resp.Usage.PromptEvalMS, EvalMS: resp.Usage.EvalMS, TotalMS: resp.Usage.TotalMS, LoadMS: resp.Usage.LoadMS,
		},
	}
	for _, tc := range resp.Message.ToolCalls {
		respRec.ToolCalls = append(respRec.ToolCalls, log.ToolCall{ID: tc.ID, Name: tc.Name, Args: tc.Args})
	}
	// Buffer the response: tokens can split a credential at any byte.
	if resp.Message.Thinking != "" {
		l.host.Emit(event.Event{Kind: event.StreamThinking, Payload: event.Text{Text: redact.Redact(resp.Message.Thinking, nil).Text}})
	}
	if resp.Message.Content != "" {
		l.host.Emit(event.Event{Kind: event.StreamText, Payload: event.Text{Text: redact.Redact(resp.Message.Content, nil).Text}})
	}
	if _, err := l.host.Record(log.KindLLMResp, respRec); err != nil {
		return scheme.Void, err
	}
	if t.budget != nil {
		t.budget.AddTokens(resp.Usage.PromptTokens, resp.Usage.OutputTokens)
	}
	t.pending = t.pending[:0]
	calls := make([]scheme.Value, len(resp.Message.ToolCalls))
	for i, tc := range resp.Message.ToolCalls {
		t.pending = append(t.pending, pendingCall{call: tc})
		calls[i] = callValue(tc)
	}
	return scheme.List(
		scheme.Cons(scheme.Sym("content"), scheme.Str(resp.Message.Content)),
		scheme.Cons(scheme.Sym("thinking"), scheme.Str(resp.Message.Thinking)),
		scheme.Cons(scheme.Sym("done"), scheme.Str(resp.DoneReason)),
		scheme.Cons(scheme.Sym("tool-calls"), scheme.List(calls...)),
	), nil
}

func (l *Loop) runTools(c *scheme.Call) (scheme.Value, error) {
	t, err := l.active(c)
	if err != nil {
		return scheme.Void, err
	}
	items, err := c.List(0)
	if err != nil {
		return scheme.Void, err
	}
	calls, err := t.claim(items)
	if err != nil {
		return scheme.Void, err
	}
	ctx := c.Ctx
	results := make([]scheme.Value, 0, len(calls))
	for i, tc := range calls {
		var limitErr error
		if t.budget != nil {
			limitErr = t.budget.ToolCall(ctx)
		} else {
			t.calls++
			if t.calls > l.cfg.MaxToolCalls {
				limitErr = ErrHardStop
			}
		}
		if limitErr != nil {
			if t.budget == nil {
				if _, err := l.host.Record(log.KindNote, log.Note{Text: limitErr.Error()}); err != nil {
					return scheme.Void, err
				}
			}
			if err := l.skipTools(calls[i:], limitErr.Error()); err != nil {
				return scheme.Void, err
			}
			return scheme.Void, halt(limitErr)
		}
		res, value, outcome, err := l.runTool(ctx, tc)
		if err != nil {
			if recordErr := l.skipTools(calls[i+1:], err.Error()); recordErr != nil {
				return scheme.Void, recordErr
			}
			return scheme.Void, err
		}
		results = append(results, resultValue(res, value, outcome))
		if reason := t.progress.observe(tc, outcome); reason != "" {
			stop := fmt.Errorf("progress stop: %s; inspect the results before continuing", reason)
			if _, err := l.host.Record(log.KindNote, log.Note{Text: stop.Error()}); err != nil {
				return scheme.Void, err
			}
			if err := l.skipTools(calls[i+1:], stop.Error()); err != nil {
				return scheme.Void, err
			}
			return scheme.Void, halt(stop)
		}
	}
	return scheme.List(results...), nil
}

func (l *Loop) skipToolsPrim(c *scheme.Call) (scheme.Value, error) {
	t, err := l.active(c)
	if err != nil {
		return scheme.Void, err
	}
	reason, err := c.String(1)
	if err != nil {
		return scheme.Void, err
	}
	if reason == "" {
		return scheme.Void, errors.New("a reason is required: it is what the model reads")
	}
	items, err := c.List(0)
	if err != nil {
		return scheme.Void, err
	}
	calls, err := t.claim(items)
	if err != nil {
		return scheme.Void, err
	}
	return scheme.Void, l.skipTools(calls, reason)
}

// skipPending answers every call from the latest response that the
// strategy neither ran nor skipped.
func (l *Loop) skipPending(reason string) error {
	if l.turn == nil {
		return nil
	}
	var calls []llm.ToolCall
	for i := range l.turn.pending {
		if !l.turn.pending[i].answered {
			l.turn.pending[i].answered = true
			calls = append(calls, l.turn.pending[i].call)
		}
	}
	return l.skipTools(calls, reason)
}

// claim resolves call values back to the calls the model made. A call not
// in the latest response, one already answered, or one whose args were
// changed is refused: the log's "origin: model" must mean the model asked
// for exactly this, and no call runs twice.
func (t *turnState) claim(items []scheme.Value) ([]llm.ToolCall, error) {
	// All or nothing: a refused call must not leave the ones before it
	// marked answered but never run.
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

// lookup finds a symbol key in an alist.
func lookup(alist scheme.Value, key string) (scheme.Value, bool) {
	items, _ := alist.Slice()
	for _, it := range items {
		if it.Kind == scheme.KPair && it.P.Car.Kind == scheme.KSymbol && it.P.Car.S == key {
			return it.P.Cdr, true
		}
	}
	return scheme.Void, false
}

func field(alist scheme.Value, key string) string {
	v, _ := lookup(alist, key)
	if v.Kind != scheme.KString {
		return ""
	}
	return v.S
}

// callValue is how a strategy sees a call. args is the model's JSON text,
// unparsed, so what runs is byte-for-byte what the model asked for.
func callValue(tc llm.ToolCall) scheme.Value {
	return scheme.List(
		scheme.Cons(scheme.Sym("id"), scheme.Str(tc.ID)),
		scheme.Cons(scheme.Sym("name"), scheme.Str(tc.Name)),
		scheme.Cons(scheme.Sym("args"), scheme.Str(string(tc.Args))),
	)
}

// resultValue is how a strategy sees a result. value keeps the provenance
// of what the tool produced, so a strategy that forwards it somewhere is
// judged by L2 exactly as the tool's own output would be.
func resultValue(res log.ToolResult, value scheme.Value, outcome toolOutcome) scheme.Value {
	return scheme.Tainted(scheme.List(
		scheme.Cons(scheme.Sym("id"), scheme.Str(res.ID)),
		scheme.Cons(scheme.Sym("name"), scheme.Str(res.Name)),
		scheme.Cons(scheme.Sym("ok"), scheme.Bool(res.OK)),
		scheme.Cons(scheme.Sym("value"), value),
		scheme.Cons(scheme.Sym("error"), scheme.Str(res.Error)),
		scheme.Cons(scheme.Sym("denied"), scheme.Bool(outcome.denied)),
	), value.Taint)
}
