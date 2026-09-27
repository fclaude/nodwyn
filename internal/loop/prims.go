package loop

import (
	"errors"
	"fmt"
	"strings"

	"github.com/fclaude/nibb/internal/llm"
	"github.com/fclaude/nibb/internal/log"
	"github.com/fclaude/nibb/internal/scheme"
)

// install registers the primitives a strategy is written with. They are
// hidden from the model, and refused to model-originated code: a tool body
// calling model-turn would re-enter the loop from inside a tool call.
func (l *Loop) install() error {
	calls := scheme.Param{Name: "calls", Type: "list", Doc: "tool calls from the latest model-turn"}
	for _, p := range []*scheme.Prim{
		{
			Name: "model-turn",
			Meta: scheme.Meta{
				Doc:      "Ask a model once, over the window built from the log: the control model, or with a name the configured model [models.NAME]. Returns an alist with keys content, thinking, done (the stop reason), tool-calls, prompt-tokens and output-tokens (the server's counts; 0 when it gives none); each call is an alist with keys id, name and args (the model's JSON text).",
				Variadic: true, // (model-turn) or (model-turn name)
				Hidden:   true,
				Origin:   "builtin",
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
		{
			Name: "add-message",
			Meta: scheme.Meta{
				Doc:    "Add text to the conversation; the model reads it as a user message at the next model-turn. Refused while calls from the latest model-turn are unanswered.",
				Params: []scheme.Param{{Name: "text", Type: "string", Doc: "what the model should read"}},
				Hidden: true,
				Origin: "builtin",
			},
			Handler: l.addMessage,
		},
		{
			Name: "narrow-context",
			Meta: scheme.Meta{
				Doc:      "Start a fresh context: until fold-context, widen or the user's next message, models see system as their system prompt and only what comes after this. Hand a model a task with add-message after it. Contexts nest; calls pending when it opens wait until it folds. An optional agent name labels the context in the log.",
				Params:   []scheme.Param{{Name: "system", Type: "string", Doc: "the system prompt of the new context"}},
				Variadic: true, // (narrow-context system) or (narrow-context system agent)
				Hidden:   true,
				Origin:   "builtin",
			},
			Handler: l.narrowContext,
		},
		{
			Name: "fold-context",
			Meta: scheme.Meta{
				Doc:    "End the innermost narrowed context and fold it away: later windows skip everything since its narrow-context and see the context around it again. Calls that were pending when it opened are the latest model-turn's again. Refused while calls from the latest model-turn are unanswered.",
				Hidden: true,
				Origin: "builtin",
			},
			Handler: l.foldContext,
		},
		{
			Name: "answer-call",
			Meta: scheme.Meta{
				Doc:    "Answer one of the model's calls with text the strategy produced, as a successful result, without running the tool: how a tool the strategy handles itself, such as a sub-agent, answers. Counts against the turn's tool-call budget.",
				Params: []scheme.Param{{Name: "call", Type: "list", Doc: "one call from the latest model-turn"}, {Name: "text", Type: "string", Doc: "the result the model reads"}},
				Hidden: true,
				Origin: "builtin",
			},
			Handler: l.answerCall,
		},
		{
			Name: "session-system-prompt",
			Meta: scheme.Meta{
				Doc:    "The session's system prompt, fixed when it started: what a narrowed context passes to narrow-context to keep the model's standing instructions.",
				Hidden: true,
				Origin: "builtin",
			},
			Handler: l.sessionSystemPrompt,
		},
		{
			Name: "widen",
			Meta: scheme.Meta{
				Doc:    "End every narrowed context: models see the whole conversation again, including what happened while it was narrowed. Refused while a context holds calls waiting for an answer; fold it instead.",
				Hidden: true,
				Origin: "builtin",
			},
			Handler: l.widen,
		},
	} {
		if err := l.interp.Register(p); err != nil {
			return err
		}
	}
	return nil
}

// active returns the turn in progress, refusing calls outside a turn or
// from model-originated code.
func (l *Loop) active(c *scheme.Call) (*turnState, error) {
	if c.Origin == scheme.OriginModel {
		return nil, errors.New("not callable from a model-originated evaluation")
	}
	if l.turn == nil {
		return nil, errors.New("only available while a turn is running")
	}
	return l.turn, nil
}

// modelTurn is (model-turn [name]): answer the calls the strategy left
// unanswered, check the budget, ask the model, and hand its reply to the
// strategy. The reply's calls become the ones run-tools may run, whichever
// model made them.
func (l *Loop) modelTurn(c *scheme.Call) (scheme.Value, error) {
	t, err := l.active(c)
	if err != nil {
		return scheme.Void, err
	}
	name := ControlModel
	switch len(c.Args) {
	case 0:
	case 1:
		if name, err = c.String(0); err != nil {
			return scheme.Void, err
		}
	default:
		return scheme.Void, errors.New("expected at most one argument, a model name")
	}
	if err := l.skipPending("the strategy asked the model again before running this call"); err != nil {
		return scheme.Void, err
	}
	if t.budget != nil {
		if err := t.budget.Check(c.Ctx); err != nil {
			return scheme.Void, halt(err)
		}
	}
	resp, err := l.ask(c.Ctx, name)
	if err != nil {
		return scheme.Void, err
	}
	if t.budget != nil {
		t.budget.AddTokens(resp.Usage.PromptTokens, resp.Usage.OutputTokens)
	}
	t.expect(resp.Message.ToolCalls)
	return replyValue(resp), nil
}

// runTools is (run-tools calls): run each call in order, stopping the turn
// if a limit is reached or the turn stops making progress. The calls after
// a stop are answered as not executed.
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
	results := make([]scheme.Value, 0, len(calls))
	for i, tc := range calls {
		if err := l.reserveCall(c.Ctx, t); err != nil {
			return scheme.Void, l.stop(err, calls[i:])
		}
		res, value, outcome, err := l.runTool(c.Ctx, tc)
		if err != nil {
			// Cancelled, or the log failed: the rest cannot run either.
			if skipErr := l.skipTools(calls[i+1:], err.Error()); skipErr != nil {
				return scheme.Void, skipErr
			}
			return scheme.Void, err
		}
		results = append(results, resultValue(res, value, outcome))
		if reason := t.progress.observe(tc, outcome); reason != "" {
			stuck := fmt.Errorf("progress stop: %s; inspect the results before continuing", reason)
			if err := l.record(log.KindNote, log.Note{Text: stuck.Error()}); err != nil {
				return scheme.Void, err
			}
			return scheme.Void, l.stop(stuck, calls[i+1:])
		}
	}
	return scheme.List(results...), nil
}

// skipToolsPrim is (skip-tools calls reason).
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

// addMessage is (add-message text): the strategy speaking to the model,
// for when there is no tool call to answer. It is refused while calls are
// unanswered, because a tool call must be followed by its result before
// anything else.
func (l *Loop) addMessage(c *scheme.Call) (scheme.Value, error) {
	t, err := l.active(c)
	if err != nil {
		return scheme.Void, err
	}
	text, err := c.String(0)
	if err != nil {
		return scheme.Void, err
	}
	if strings.TrimSpace(text) == "" {
		return scheme.Void, errors.New("text is required")
	}
	if n := t.unanswered(); n > 0 {
		return scheme.Void, fmt.Errorf("%d calls from the latest model-turn are unanswered; run or skip them first", n)
	}
	return scheme.Void, l.record(log.KindHarnessMsg, log.HarnessMsg{Text: text})
}

// narrowContext is (narrow-context system). The narrowing is an event in
// the log, so every later window, and a replay, rebuilds the same context.
func (l *Loop) narrowContext(c *scheme.Call) (scheme.Value, error) {
	t, err := l.active(c)
	if err != nil {
		return scheme.Void, err
	}
	system, err := c.String(0)
	if err != nil {
		return scheme.Void, err
	}
	if strings.TrimSpace(system) == "" {
		return scheme.Void, errors.New("a system prompt is required")
	}
	rec := log.ContextNarrow{System: system}
	switch len(c.Args) {
	case 1:
	case 2:
		if rec.Agent, err = c.String(1); err != nil {
			return scheme.Void, err
		}
	default:
		return scheme.Void, errors.New("expected a system prompt and, optionally, an agent name")
	}
	for _, p := range t.pending {
		if !p.answered {
			rec.Answers = append(rec.Answers, p.call.ID)
		}
	}
	if err := l.record(log.KindContextNarrow, rec); err != nil {
		return scheme.Void, err
	}
	// The latest model-turn's calls wait for this context to fold; its own
	// model-turns start with none pending.
	t.contexts = append(t.contexts, t.pending)
	t.pending = nil
	return scheme.Void, nil
}

// foldContext is (fold-context).
func (l *Loop) foldContext(c *scheme.Call) (scheme.Value, error) {
	t, err := l.active(c)
	if err != nil {
		return scheme.Void, err
	}
	if len(t.contexts) == 0 {
		return scheme.Void, errors.New("no narrowed context is open in this turn")
	}
	if n := t.unanswered(); n > 0 {
		return scheme.Void, fmt.Errorf("%d calls from the latest model-turn are unanswered; run or skip them first", n)
	}
	return scheme.Void, l.fold(t)
}

// fold records the fold and gives the enclosing context its pending calls
// back.
func (l *Loop) fold(t *turnState) error {
	if err := l.record(log.KindContextFold, log.ContextFold{}); err != nil {
		return err
	}
	n := len(t.contexts)
	t.pending, t.contexts = t.contexts[n-1], t.contexts[:n-1]
	return nil
}

// answerCall is (answer-call call text).
func (l *Loop) answerCall(c *scheme.Call) (scheme.Value, error) {
	t, err := l.active(c)
	if err != nil {
		return scheme.Void, err
	}
	if len(c.Args) != 2 {
		return scheme.Void, errors.New("expected a call and the text that answers it")
	}
	text, err := c.String(1)
	if err != nil {
		return scheme.Void, err
	}
	calls, err := t.claim([]scheme.Value{c.Args[0]})
	if err != nil {
		return scheme.Void, err
	}
	if err := l.reserveCall(c.Ctx, t); err != nil {
		return scheme.Void, l.stop(err, calls)
	}
	return scheme.Void, l.record(log.KindToolResult, log.ToolResult{ID: calls[0].ID, Name: calls[0].Name, OK: true, Value: text})
}

// sessionSystemPrompt is (session-system-prompt). It needs no turn: the
// prompt is fixed once the session has started.
func (l *Loop) sessionSystemPrompt(c *scheme.Call) (scheme.Value, error) {
	if c.Origin == scheme.OriginModel {
		return scheme.Void, errors.New("not callable from a model-originated evaluation")
	}
	if !l.frozen {
		return scheme.Void, errors.New("the system prompt is not fixed until the session has started")
	}
	return scheme.Str(l.system), nil
}

// widen is (widen).
func (l *Loop) widen(c *scheme.Call) (scheme.Value, error) {
	t, err := l.active(c)
	if err != nil {
		return scheme.Void, err
	}
	if n := t.unanswered(); n > 0 {
		return scheme.Void, fmt.Errorf("%d calls from the latest model-turn are unanswered; run or skip them first", n)
	}
	if t.parked() {
		return scheme.Void, errors.New("a narrowed context holds calls waiting for an answer; fold it with fold-context instead")
	}
	if err := l.record(log.KindContextWiden, log.ContextWiden{}); err != nil {
		return scheme.Void, err
	}
	t.contexts = nil
	return scheme.Void, nil
}

// stop ends the turn: the calls that have not run are answered with the
// reason, and the reason is raised as a Halt so the strategy cannot catch
// it.
func (l *Loop) stop(reason error, rest []llm.ToolCall) error {
	if err := l.skipTools(rest, reason.Error()); err != nil {
		return err
	}
	return halt(reason)
}

// halt makes err uncatchable by a strategy's guard (scheme.Halt).
func halt(err error) error { return &scheme.Halt{Err: err} }

// The values a strategy sees are alists with symbol keys, like the other
// records the harness makes (primitive-info, error objects).

// replyValue is what (model-turn) returns.
func replyValue(resp *llm.Response) scheme.Value {
	calls := make([]scheme.Value, len(resp.Message.ToolCalls))
	for i, tc := range resp.Message.ToolCalls {
		calls[i] = callValue(tc)
	}
	return scheme.List(
		scheme.Cons(scheme.Sym("content"), scheme.Str(resp.Message.Content)),
		scheme.Cons(scheme.Sym("thinking"), scheme.Str(resp.Message.Thinking)),
		scheme.Cons(scheme.Sym("done"), scheme.Str(resp.DoneReason)),
		scheme.Cons(scheme.Sym("tool-calls"), scheme.List(calls...)),
		scheme.Cons(scheme.Sym("prompt-tokens"), scheme.Int(int64(resp.Usage.PromptTokens))),
		scheme.Cons(scheme.Sym("output-tokens"), scheme.Int(int64(resp.Usage.OutputTokens))),
	)
}

// callValue is one call. args is the model's JSON text, unparsed, so a
// strategy can compare it with what claim will run.
func callValue(tc llm.ToolCall) scheme.Value {
	return scheme.List(
		scheme.Cons(scheme.Sym("id"), scheme.Str(tc.ID)),
		scheme.Cons(scheme.Sym("name"), scheme.Str(tc.Name)),
		scheme.Cons(scheme.Sym("args"), scheme.Str(string(tc.Args))),
	)
}

// resultValue is one result. value keeps the provenance of what the tool
// produced, so a strategy that forwards it somewhere is judged by L2
// exactly as the tool's own output would be.
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

// field is a string under key, or "".
func field(alist scheme.Value, key string) string {
	v, _ := lookup(alist, key)
	if v.Kind != scheme.KString {
		return ""
	}
	return v.S
}
