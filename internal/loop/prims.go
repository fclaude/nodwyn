package loop

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/fclaude/nodwyn/internal/budget"
	"github.com/fclaude/nodwyn/internal/event"
	"github.com/fclaude/nodwyn/internal/llm"
	"github.com/fclaude/nodwyn/internal/log"
	"github.com/fclaude/nodwyn/internal/scheme"
	"github.com/fclaude/nodwyn/internal/window"
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
				Doc:      "Ask a model once, over the window built from the log: the control model, or with a name the configured model [models.NAME]; an options alist may follow the name, where (tools . #f) offers the model no tools and (headroom . #t) lets the request run past the window's high water, up to its ceiling, before older context is trimmed. Returns an alist with keys content, thinking, done (the stop reason), tool-calls, prompt-tokens and output-tokens (the server's counts; 0 when it gives none), cached-tokens (from the provider's prompt cache) and cost (USD, reported or estimated from configured prices), each #f when unknown; each call is an alist with keys id, name and args (the model's JSON text).",
				Variadic: true, // (model-turn), (model-turn name) or (model-turn name options)
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
			Name: "fold-result",
			Meta: scheme.Meta{
				Doc:    "Show a tool result as text in later windows, such as an outline or a line saying what it was, once its details stopped mattering. The result stays in the log, for replay, audit and recall.",
				Params: []scheme.Param{{Name: "result", Type: "any", Doc: "the tool.result event's seq, or the id of the call it answers"}, {Name: "text", Type: "string", Doc: "what later windows show instead"}},
				Hidden: true,
				Origin: "builtin",
			},
			Handler: l.foldResult,
		},
		{
			Name: "log-note",
			Meta: scheme.Meta{
				Doc:    "Add a note to the session log, for the record and the transcript; the model never sees it.",
				Params: []scheme.Param{{Name: "text", Type: "string", Doc: "the note"}},
				Hidden: true,
				Origin: "builtin",
			},
			Handler: l.logNote,
		},
		{
			Name: "record-compaction",
			Meta: scheme.Meta{
				Doc:    "Record a compaction as context.compact, from an alist: trigger (auto, command or requested), compacted (#t or #f), before (the context's size before it, from context-tokens), threshold (for auto) and focus. The size after and the note's reply are read here: the context as it stands, and the latest model reply.",
				Params: []scheme.Param{{Name: "facts", Type: "alist", Doc: "what the compaction knows"}},
				Hidden: true,
				Origin: "builtin",
			},
			Handler: l.recordCompaction,
		},
		{
			Name: "record-plan",
			Meta: scheme.Meta{
				Doc:    "Record the plan gate's outcome as gate.plan, from an alist: decision (plan, proceed, degraded, required or carried), plan, score, revisions, reason, and marks, the steps of the plan already marked, each an alist of step, state and note, as run-plan and unfinished-plan give them (a run's later turn, and the turn after one that did not finish, carry a plan with them). The window shows a recorded plan, and the marks it carries, to the model for the rest of the turn.",
				Params: []scheme.Param{{Name: "outcome", Type: "alist", Doc: "the gate's decision and plan"}},
				Hidden: true,
				Origin: "builtin",
			},
			Handler: l.recordPlan,
		},
		{
			Name: "plan-steps",
			Meta: scheme.Meta{
				Doc:    "The steps of the plan recorded in this turn, in order, as strings: its numbered or bulleted lines, or the whole plan when it is prose. '() when no plan was recorded. With a plan's text, the steps of that text, read the same way: plan-write checks a plan's steps before it records it.",
				Params: []scheme.Param{{Name: "plan", Type: "string", Doc: "a plan's text, not yet recorded", Optional: true}},
				Hidden: true,
				Origin: "builtin",
			},
			Handler: l.planSteps,
		},
		{
			Name: "run-plan",
			Meta: scheme.Meta{
				Doc:    "The plan of the run this turn belongs to, as its earlier turns left it: an alist of plan (the text), steps, marks (the latest mark per step, in step order, each an alist of step, state and note) and next (the first unmarked step, or #f when every step is marked). #f when this turn is not a run's, or no turn of the run has recorded a plan yet. A run started afresh has none; a resumed one keeps its plan.",
				Hidden: true,
				Origin: "builtin",
			},
			Handler: l.runPlanPrim,
		},
		{
			Name: "unfinished-plan",
			Meta: scheme.Meta{
				Doc:    "The plan a turn that did not finish left to this one, read from the log, as run-plan gives a run's: an alist of plan, steps, marks and next. A turn did not finish when an error, a limit or the person's Stop ended it (turn.end outcome error, stopped, hard_budget, spend_limit or canceled), or when nothing ended it; its plan, with every mark made since, goes on through such turns. #f when the latest turn that ended finished (outcome completed, or none recorded), or no turn since recorded a plan.",
				Hidden: true,
				Origin: "builtin",
			},
			Handler: l.unfinishedPlanPrim,
		},
		{
			Name: "record-step",
			Meta: scheme.Meta{
				Doc:    "Record that step n of the turn's plan is done or skipped, with a note saying what shows it (plan.step); the console shows the plan's progress. Returns ((done . k) (of . n) (next . m)): steps marked so far, the plan's steps, and the first unmarked step, or #f.",
				Params: []scheme.Param{{Name: "step", Type: "integer", Doc: "the step's number, from 1"}, {Name: "state", Type: "string", Doc: "done or skipped"}, {Name: "note", Type: "string", Doc: "what shows it"}},
				Hidden: true,
				Origin: "builtin",
			},
			Handler: l.recordStep,
		},
		{
			Name: "narrow-context",
			Meta: scheme.Meta{
				Doc:      "Start a fresh context: until fold-context, widen or the user's next message, models see system as their system prompt and only what comes after this. Hand a model a task with add-message after it. Contexts nest; calls pending when it opens wait until it folds. An optional agent name labels the context in the log; an options alist may follow it, where (lasting . #t) makes the narrowing outlast the turn, as a compaction's does: if it is still open at the user's next message, the conversation starts here from then on, under the session's system prompt.",
				Params:   []scheme.Param{{Name: "system", Type: "string", Doc: "the system prompt of the new context"}},
				Variadic: true, // (narrow-context system), (narrow-context system agent) or (narrow-context system agent options)
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
			Name: "session-requests",
			Meta: scheme.Meta{
				Doc:    "The user's messages in this session so far, oldest first, as recorded (redacted); the running turn's request is the last. What a strategy reads to know what was asked before this turn, such as the request a compaction between turns hands over.",
				Hidden: true,
				Origin: "builtin",
			},
			Handler: l.sessionRequests,
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
			Name: "model-info",
			Meta: scheme.Meta{
				Doc:      "What is known about a model before asking it: the control model, or with a name the configured model [models.NAME]. Returns an alist with keys name, model (the provider's id), provider, num-ctx (its context length; 0 when unset), max-output (the reply cap its requests carry), and temperature and seed (#f when the server decides).",
				Variadic: true, // (model-info) or (model-info name)
				Hidden:   true,
				Origin:   "builtin",
			},
			Handler: l.modelInfo,
		},
		{
			Name: "model-names",
			Meta: scheme.Meta{
				Doc:    "The names model-turn and model-info accept: \"control\" and each configured [models.NAME], sorted.",
				Hidden: true,
				Origin: "builtin",
			},
			Handler: l.modelNames,
		},
		{
			Name: "window-limits",
			Meta: scheme.Meta{
				Doc:    "The window's sizes, in tokens as the window estimates them: an alist with keys high-water (past it older context is trimmed), low-water (what trimming goes down to), ceiling (the most a request with headroom may carry; the high water when nothing is known above it), max-result-tokens (a longer tool result is shortened) and output-reserve (the default reply cap).",
				Hidden: true,
				Origin: "builtin",
			},
			Handler: l.windowLimits,
		},
		{
			Name: "context-tokens",
			Meta: scheme.Meta{
				Doc:      "The size of the context the next model-turn would send, in tokens as the window estimates them (the unit of window-limits), before any trimming. With a tool result (its seq, or the id of the call it answers), the size of the part from that result on: what a server with a prompt cache reads again when the result is folded; #f when the window does not show it.",
				Variadic: true, // (context-tokens) or (context-tokens result)
				Hidden:   true,
				Origin:   "builtin",
			},
			Handler: l.contextTokens,
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

// modelTurn is (model-turn [name [options]]): answer the calls the strategy left
// unanswered, check the budget, ask the model, and hand its reply to the
// strategy. The reply's calls become the ones run-tools may run, whichever
// model made them.
func (l *Loop) modelTurn(c *scheme.Call) (scheme.Value, error) {
	t, err := l.active(c)
	if err != nil {
		return scheme.Void, err
	}
	name, offer, headroom := ControlModel, toolOffer{}, false
	if len(c.Args) > 2 {
		return scheme.Void, errors.New("expected at most a model name and an options alist")
	}
	if len(c.Args) > 0 {
		if name, err = c.String(0); err != nil {
			return scheme.Void, err
		}
	}
	if len(c.Args) == 2 {
		opts, ok := c.Args[1].Slice()
		if !ok {
			return scheme.Void, errors.New("options must be an alist, such as '((tools . #f))")
		}
		for _, o := range opts {
			if o.Kind != scheme.KPair || o.P.Car.Kind != scheme.KSymbol {
				return scheme.Void, fmt.Errorf("unknown model-turn option %s; the options are tools and headroom", scheme.Write(o))
			}
			switch o.P.Car.S {
			case "tools":
				if offer, err = l.parseToolOffer(o.P.Cdr); err != nil {
					return scheme.Void, err
				}
			case "headroom":
				if o.P.Cdr.Kind != scheme.KBool {
					return scheme.Void, fmt.Errorf("the headroom option is #t or #f; got %s", scheme.Write(o.P.Cdr))
				}
				headroom = scheme.Equal(o.P.Cdr, scheme.True)
			default:
				return scheme.Void, fmt.Errorf("unknown model-turn option %s; the options are tools and headroom", scheme.Write(o))
			}
		}
	}
	if err := l.skipPending("the strategy asked the model again before running this call"); err != nil {
		return scheme.Void, err
	}
	if t.budget != nil {
		if err := t.budget.Check(c.Ctx); err != nil {
			return scheme.Void, halt(err)
		}
	}
	resp, err := l.ask(c.Ctx, name, offer, headroom)
	if err != nil {
		return scheme.Void, err
	}
	var rates llm.Rates
	if ep, err := l.endpoint(name); err == nil {
		rates = ep.Rates
	}
	cost, _ := llm.Cost(resp.Usage, rates)
	if t.budget != nil {
		t.budget.AddTokens(budget.CountedPrompt(resp.Usage.PromptTokens, resp.Usage.CachedTokens), resp.Usage.OutputTokens)
		t.budget.AddSpend(cost, resp.Usage.CachedTokens)
	}
	t.expect(resp.Message.ToolCalls)
	return replyValue(resp, cost), nil
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

// logNote is (log-note text).
func (l *Loop) logNote(c *scheme.Call) (scheme.Value, error) {
	if _, err := l.active(c); err != nil {
		return scheme.Void, err
	}
	text, err := c.String(0)
	if err != nil {
		return scheme.Void, err
	}
	if strings.TrimSpace(text) == "" {
		return scheme.Void, errors.New("text is required")
	}
	return scheme.Void, l.record(log.KindNote, log.Note{Text: text})
}

// foldResult is (fold-result result text): later windows show a tool result
// as text. result is the tool.result's seq, or the id of the call it
// answers (its latest result); either must be of this session.
func (l *Loop) foldResult(c *scheme.Call) (scheme.Value, error) {
	if _, err := l.active(c); err != nil {
		return scheme.Void, err
	}
	text, err := c.String(1)
	if err != nil {
		return scheme.Void, err
	}
	if strings.TrimSpace(text) == "" {
		return scheme.Void, errors.New("text is required: it is what later windows show")
	}
	seq, err := resultSeq(l.host.EventsSoFar(), c.Args[0])
	if err != nil {
		return scheme.Void, err
	}
	return scheme.Void, l.record(log.KindResultFold, log.ResultFold{Seq: seq, Text: text})
}

// resultSeq is the seq of the tool.result arg names: its seq, or the id of
// the call it answers (the latest such result).
func resultSeq(events []log.Event, arg scheme.Value) (uint64, error) {
	switch arg.Kind {
	case scheme.KInt:
		for _, ev := range events {
			if ev.Seq == uint64(arg.I) {
				if ev.Kind != log.KindToolResult {
					return 0, fmt.Errorf("seq %d is a %s, not a tool.result", arg.I, ev.Kind)
				}
				return ev.Seq, nil
			}
		}
		return 0, fmt.Errorf("no event at seq %d", arg.I)
	case scheme.KString:
		for i := len(events) - 1; i >= 0; i-- {
			if events[i].Kind != log.KindToolResult {
				continue
			}
			var p log.ToolResult
			if events[i].Decode(&p) == nil && p.ID == arg.S {
				return events[i].Seq, nil
			}
		}
		return 0, fmt.Errorf("no result for call %q", arg.S)
	}
	return 0, fmt.Errorf("result is a seq or a call id, got %s", scheme.Write(arg))
}

// recordPlan is (record-plan outcome): the plan gate's outcome, in the log
// where the window finds the plan and a front-end shows the decision.
func (l *Loop) recordPlan(c *scheme.Call) (scheme.Value, error) {
	if _, err := l.active(c); err != nil {
		return scheme.Void, err
	}
	outcome := c.Args[0]
	gp := log.GatePlan{Origin: "gate", Noul: field(outcome, "decision"), Plan: field(outcome, "plan"), Reason: field(outcome, "reason")}
	if gp.Noul == "" {
		return scheme.Void, errors.New("a decision is required: plan, proceed, degraded or required")
	}
	if v, ok := lookup(outcome, "score"); ok && v.IsNumber() {
		gp.Score = v.AsFloat()
	}
	if v, ok := lookup(outcome, "revisions"); ok && v.Kind == scheme.KInt {
		gp.Revisions = int(v.I)
	}
	if v, ok := lookup(outcome, "marks"); ok {
		marks, err := planMarks(v, len(gp.Steps()))
		if err != nil {
			return scheme.Void, err
		}
		if len(marks) > 0 && strings.TrimSpace(gp.Plan) == "" {
			return scheme.Void, errors.New("marks need the plan they mark")
		}
		gp.Marks = marks
	}
	if err := l.record(log.KindGatePlan, gp); err != nil {
		return scheme.Void, err
	}
	if strings.TrimSpace(gp.Plan) != "" {
		t := l.turn
		t.steps, t.marks = gp.Steps(), map[int]string{}
		for _, m := range gp.Marks {
			t.marks[m.Step] = m.State
		}
	}
	if strings.TrimSpace(gp.Plan) != "" {
		l.host.Emit(event.Event{Kind: event.Status, Payload: event.StatusInfo{State: "plan", Detail: fmt.Sprintf("plan score %.2f (%d revises)", gp.Score, gp.Revisions)}})
	}
	return scheme.Void, nil
}

// recordCompaction is (record-compaction facts): one compaction, for the
// record and the front-ends. What only the strategy knows (what set it off,
// the size before, the threshold, the focus) comes from facts; the size
// after and the note are measured here, so call it once the handover is in
// place.
func (l *Loop) recordCompaction(c *scheme.Call) (scheme.Value, error) {
	if _, err := l.active(c); err != nil {
		return scheme.Void, err
	}
	facts := c.Args[0]
	rec := log.ContextCompact{Trigger: field(facts, "trigger"), Focus: field(facts, "focus")}
	switch rec.Trigger {
	case "auto", "command", "requested":
	default:
		return scheme.Void, errors.New("trigger must be auto, command or requested")
	}
	if v, ok := lookup(facts, "compacted"); ok {
		rec.Compacted = v.IsTrue()
	}
	if v, ok := lookup(facts, "before"); ok && v.Kind == scheme.KInt {
		rec.Before = int(v.I)
	}
	if v, ok := lookup(facts, "threshold"); ok && v.Kind == scheme.KInt {
		rec.Threshold = int(v.I)
	}
	events := l.host.EventsSoFar()
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].Kind != log.KindLLMResp {
			continue
		}
		var resp log.LLMResp
		if events[i].Decode(&resp) == nil {
			rec.NoteSeq, rec.NoteTokens, rec.Truncated = events[i].Seq, resp.Usage.OutputTokens, resp.DoneReason == "length"
		}
		break
	}
	if rec.Compacted && l.frozen {
		pol, _ := l.policy()
		pol.HighWater = math.MaxInt
		rec.After = window.Build(events, l.system, l.toolsLen, pol).EstTokens
	}
	return scheme.Void, l.record(log.KindContextCompact, rec)
}

// planSteps is (plan-steps [plan]): the steps of the plan recorded this
// turn, or of plan's text, read as a recorded plan is, so that a step
// plan-write checks is the step plan-step will mark.
func (l *Loop) planSteps(c *scheme.Call) (scheme.Value, error) {
	t, err := l.active(c)
	if err != nil {
		return scheme.Void, err
	}
	steps := t.steps
	if len(c.Args) > 0 {
		plan, err := c.String(0)
		if err != nil {
			return scheme.Void, err
		}
		steps = log.GatePlan{Plan: plan}.Steps()
	}
	out := make([]scheme.Value, len(steps))
	for i, s := range steps {
		out[i] = scheme.Str(s)
	}
	return scheme.List(out...), nil
}

// recordStep is (record-step n state note): step n of the turn's plan is
// done or skipped, as the model says (the strategy answers its plan-step
// call with this). It is recorded as plan.step for the console.
func (l *Loop) recordStep(c *scheme.Call) (scheme.Value, error) {
	t, err := l.active(c)
	if err != nil {
		return scheme.Void, err
	}
	n, err := c.Int(0)
	if err != nil {
		return scheme.Void, err
	}
	state, err := c.String(1)
	if err != nil {
		return scheme.Void, err
	}
	note, err := c.String(2)
	if err != nil {
		return scheme.Void, err
	}
	switch {
	case len(t.steps) == 0:
		return scheme.Void, errors.New("no plan was recorded in this turn")
	case n < 1 || int(n) > len(t.steps):
		return scheme.Void, fmt.Errorf("the plan has %d steps, numbered from 1; there is no step %d", len(t.steps), n)
	case state != "done" && state != "skipped":
		return scheme.Void, fmt.Errorf("a step is done or skipped, not %q", state)
	}
	if err := l.record(log.KindPlanStep, log.PlanStep{Step: int(n), Of: len(t.steps), State: state, Note: note}); err != nil {
		return scheme.Void, err
	}
	t.marks[int(n)] = state
	next := scheme.False
	for i := 1; i <= len(t.steps); i++ {
		if t.marks[i] == "" {
			next = scheme.Int(int64(i))
			break
		}
	}
	return scheme.List(
		scheme.Cons(scheme.Sym("done"), scheme.Int(int64(len(t.marks)))),
		scheme.Cons(scheme.Sym("of"), scheme.Int(int64(len(t.steps)))),
		scheme.Cons(scheme.Sym("next"), next),
	), nil
}

// planMarks reads the marks record-plan carries with a plan of n steps:
// a list of alists of step, state and note, at most one per step.
func planMarks(v scheme.Value, n int) ([]log.PlanStep, error) {
	items, ok := v.Slice()
	if !ok {
		return nil, errors.New("marks is a list of alists of step, state and note")
	}
	var marks []log.PlanStep
	seen := map[int]bool{}
	for _, it := range items {
		step, ok := lookup(it, "step")
		if !ok || step.Kind != scheme.KInt {
			return nil, errors.New("a mark needs its step's number")
		}
		m := log.PlanStep{Step: int(step.I), Of: n, State: field(it, "state"), Note: field(it, "note")}
		switch {
		case m.Step < 1 || m.Step > n:
			return nil, fmt.Errorf("the plan has %d steps, numbered from 1; there is no step %d", n, m.Step)
		case m.State != "done" && m.State != "skipped":
			return nil, fmt.Errorf("a step is done or skipped, not %q", m.State)
		case seen[m.Step]:
			return nil, fmt.Errorf("step %d is marked twice", m.Step)
		}
		seen[m.Step] = true
		marks = append(marks, m)
	}
	return marks, nil
}

// runPlanPrim is (run-plan), read from the log like every window, so a
// resumed run finds the plan it had.
func (l *Loop) runPlanPrim(c *scheme.Call) (scheme.Value, error) {
	if c.Origin == scheme.OriginModel {
		return scheme.Void, errors.New("not callable from a model-originated evaluation")
	}
	plan, marks, ok := runPlan(l.host.EventsSoFar())
	if !ok {
		return scheme.False, nil
	}
	return planAlist(plan, marks), nil
}

// unfinishedPlanPrim is (unfinished-plan), read from the log like
// (run-plan), so a resumed session finds the plan its last turn left.
func (l *Loop) unfinishedPlanPrim(c *scheme.Call) (scheme.Value, error) {
	if c.Origin == scheme.OriginModel {
		return scheme.Void, errors.New("not callable from a model-originated evaluation")
	}
	plan, marks, ok := unfinishedPlan(l.host.EventsSoFar())
	if !ok {
		return scheme.False, nil
	}
	return planAlist(plan, marks), nil
}

// planAlist is a plan carried from earlier turns as the strategy reads it:
// plan, steps, marks and next, the first unmarked step or #f.
func planAlist(plan string, marks []log.PlanStep) scheme.Value {
	steps := log.GatePlan{Plan: plan}.Steps()
	stepValues := make([]scheme.Value, len(steps))
	for i, s := range steps {
		stepValues[i] = scheme.Str(s)
	}
	marked := map[int]bool{}
	markValues := make([]scheme.Value, len(marks))
	for i, m := range marks {
		marked[m.Step] = true
		markValues[i] = scheme.List(
			scheme.Cons(scheme.Sym("step"), scheme.Int(int64(m.Step))),
			scheme.Cons(scheme.Sym("state"), scheme.Str(m.State)),
			scheme.Cons(scheme.Sym("note"), scheme.Str(m.Note)),
		)
	}
	next := scheme.False
	for i := 1; i <= len(steps); i++ {
		if !marked[i] {
			next = scheme.Int(int64(i))
			break
		}
	}
	return scheme.List(
		scheme.Cons(scheme.Sym("plan"), scheme.Str(plan)),
		scheme.Cons(scheme.Sym("steps"), scheme.List(stepValues...)),
		scheme.Cons(scheme.Sym("marks"), scheme.List(markValues...)),
		scheme.Cons(scheme.Sym("next"), next),
	)
}

// runPlan is the plan of the run the latest turn belongs to, as the run's
// turns left it: the latest plan a turn of the run recorded (a turn whose
// user.msg follows a run.turn), and the latest mark per step since, the
// marks it carried included, in step order. ok is false when the latest
// turn is not a run's, or no turn of the run recorded a plan. A run that
// starts afresh (run.state running with no turns yet) starts with none; a
// resume keeps it, and a chat turn between leaves it alone.
func runPlan(events []log.Event) (plan string, marks []log.PlanStep, ok bool) {
	var byStep map[int]log.PlanStep
	reserved, inRun := false, false
	for _, ev := range events {
		switch ev.Kind {
		case log.KindRunState:
			var p log.RunState
			if ev.Decode(&p) == nil && p.Status == "running" && p.Turns == 0 {
				plan, byStep = "", nil
			}
		case log.KindRunTurn:
			reserved = true
		case log.KindUserMsg:
			inRun, reserved = reserved, false
		case log.KindGatePlan:
			var p log.GatePlan
			if inRun && ev.Decode(&p) == nil && strings.TrimSpace(p.Plan) != "" {
				plan, byStep = p.Plan, map[int]log.PlanStep{}
				for _, m := range p.Marks {
					byStep[m.Step] = m
				}
			}
		case log.KindPlanStep:
			var p log.PlanStep
			if inRun && plan != "" && ev.Decode(&p) == nil {
				byStep[p.Step] = p
			}
		}
	}
	if !inRun || plan == "" {
		return "", nil, false
	}
	return plan, inStepOrder(byStep), true
}

// unfinishedPlan is the plan the turns that did not finish left to the
// latest one: the latest plan recorded since the last turn that finished,
// and the latest mark per step since, the marks it carried included, in
// step order. A turn finished when its turn.end says completed, or says
// nothing (an older log); one that an error, a limit or the person's Stop
// ended, or that nothing ended, leaves its plan to the next. ok is false
// when no turn since the last one that finished recorded a plan.
func unfinishedPlan(events []log.Event) (plan string, marks []log.PlanStep, ok bool) {
	var byStep map[int]log.PlanStep
	for _, ev := range events {
		switch ev.Kind {
		case log.KindTurnEnd:
			var p log.TurnEnd
			if ev.Decode(&p) != nil {
				continue
			}
			switch p.Outcome {
			case "error", "stopped", "hard_budget", "spend_limit", "canceled":
			default:
				plan, byStep = "", nil
			}
		case log.KindGatePlan:
			var p log.GatePlan
			if ev.Decode(&p) == nil && strings.TrimSpace(p.Plan) != "" {
				plan, byStep = p.Plan, map[int]log.PlanStep{}
				for _, m := range p.Marks {
					byStep[m.Step] = m
				}
			}
		case log.KindPlanStep:
			var p log.PlanStep
			if plan != "" && ev.Decode(&p) == nil {
				byStep[p.Step] = p
			}
		}
	}
	if plan == "" {
		return "", nil, false
	}
	return plan, inStepOrder(byStep), true
}

// inStepOrder is the marks, one per step, in step order.
func inStepOrder(byStep map[int]log.PlanStep) []log.PlanStep {
	var marks []log.PlanStep
	for _, m := range byStep {
		marks = append(marks, m)
	}
	sort.Slice(marks, func(i, j int) bool { return marks[i].Step < marks[j].Step })
	return marks
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
	if len(c.Args) > 3 {
		return scheme.Void, errors.New("expected a system prompt and, optionally, an agent name and an options alist")
	}
	if len(c.Args) > 1 {
		if rec.Agent, err = c.String(1); err != nil {
			return scheme.Void, err
		}
	}
	if len(c.Args) == 3 {
		if rec.Lasting, err = narrowOptions(c.Args[2]); err != nil {
			return scheme.Void, err
		}
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

// narrowOptions reads narrow-context's options alist: whether the
// narrowing lasts past the turn. Only #t or #f is accepted, so a slip such
// as '((lasting #t)) is an error rather than a silent no.
func narrowOptions(v scheme.Value) (lasting bool, err error) {
	opts, ok := v.Slice()
	if !ok {
		return false, errors.New("options must be an alist, such as '((lasting . #t))")
	}
	for _, o := range opts {
		if o.Kind != scheme.KPair || o.P.Car.Kind != scheme.KSymbol || o.P.Car.S != "lasting" {
			return false, fmt.Errorf("unknown narrow-context option %s; the only one is lasting", scheme.Write(o))
		}
		switch {
		case scheme.Equal(o.P.Cdr, scheme.True):
			lasting = true
		case scheme.Equal(o.P.Cdr, scheme.False):
			lasting = false
		default:
			return false, fmt.Errorf("the lasting option is #t or #f; got %s", scheme.Write(o.P.Cdr))
		}
	}
	return lasting, nil
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

// sessionRequests is (session-requests), read from the log like every
// window, so it holds across a resume.
func (l *Loop) sessionRequests(c *scheme.Call) (scheme.Value, error) {
	if c.Origin == scheme.OriginModel {
		return scheme.Void, errors.New("not callable from a model-originated evaluation")
	}
	var out []scheme.Value
	for _, ev := range l.host.EventsSoFar() {
		if ev.Kind != log.KindUserMsg {
			continue
		}
		var p log.UserMsg
		if err := ev.Decode(&p); err != nil {
			return scheme.Void, err
		}
		out = append(out, scheme.Str(p.Text))
	}
	return scheme.List(out...), nil
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

// replyValue is what (model-turn) returns. cached-tokens and cost are #f
// when not known.
func replyValue(resp *llm.Response, cost *float64) scheme.Value {
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
		scheme.Cons(scheme.Sym("cached-tokens"), optionalInt(resp.Usage.CachedTokens)),
		scheme.Cons(scheme.Sym("cost"), optionalFloat(cost)),
	)
}

func optionalInt(n *int) scheme.Value {
	if n == nil {
		return scheme.False
	}
	return scheme.Int(int64(*n))
}

func optionalFloat(f *float64) scheme.Value {
	if f == nil {
		return scheme.False
	}
	return scheme.Float(*f)
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

// modelInfo is (model-info [name]).
func (l *Loop) modelInfo(c *scheme.Call) (scheme.Value, error) {
	name := ControlModel
	if len(c.Args) > 1 {
		return scheme.Void, errors.New("expected at most a model name")
	}
	if len(c.Args) == 1 {
		var err error
		if name, err = c.String(0); err != nil {
			return scheme.Void, err
		}
	}
	ep, err := l.endpoint(name)
	if err != nil {
		return scheme.Void, err
	}
	provider := ""
	if ep.Provider != nil {
		provider = ep.Provider.Name()
	}
	maxOutput := l.cfg.MaxOutput
	if ep.MaxOutput > 0 {
		maxOutput = ep.MaxOutput
	}
	temperature, seed := scheme.False, scheme.False
	if ep.Temperature != nil {
		temperature = scheme.Float(*ep.Temperature)
	}
	if ep.Seed != nil {
		seed = scheme.Int(*ep.Seed)
	}
	return scheme.List(
		scheme.Cons(scheme.Sym("name"), scheme.Str(name)),
		scheme.Cons(scheme.Sym("model"), scheme.Str(ep.Model)),
		scheme.Cons(scheme.Sym("provider"), scheme.Str(provider)),
		scheme.Cons(scheme.Sym("num-ctx"), scheme.Int(int64(ep.NumCtx))),
		scheme.Cons(scheme.Sym("max-output"), scheme.Int(int64(maxOutput))),
		scheme.Cons(scheme.Sym("temperature"), temperature),
		scheme.Cons(scheme.Sym("seed"), seed),
	), nil
}

// modelNames is (model-names).
func (l *Loop) modelNames(*scheme.Call) (scheme.Value, error) {
	// The registry's map is the one a switch writes the control model into.
	l.controlMu.RLock()
	names := make([]string, 0, len(l.cfg.Models))
	for n := range l.cfg.Models {
		names = append(names, n)
	}
	l.controlMu.RUnlock()
	sort.Strings(names)
	vs := make([]scheme.Value, len(names))
	for i, n := range names {
		vs[i] = scheme.Str(n)
	}
	return scheme.List(vs...), nil
}

// windowLimits is (window-limits).
func (l *Loop) windowLimits(*scheme.Call) (scheme.Value, error) {
	w, ceiling := l.policy()
	return scheme.List(
		scheme.Cons(scheme.Sym("high-water"), scheme.Int(int64(w.HighWater))),
		scheme.Cons(scheme.Sym("low-water"), scheme.Int(int64(w.LowWater))),
		scheme.Cons(scheme.Sym("ceiling"), scheme.Int(int64(max(ceiling, w.HighWater)))),
		scheme.Cons(scheme.Sym("max-result-tokens"), scheme.Int(int64(w.MaxResultTokens))),
		scheme.Cons(scheme.Sym("output-reserve"), scheme.Int(int64(l.cfg.MaxOutput))),
	), nil
}

// contextTokens is (context-tokens [result]): the window the next
// model-turn would build, before it trims anything. Compaction compares it
// with a threshold derived from the high water, so both count the same way,
// and it measures the tool results just added, which the last reply's
// prompt-tokens cannot. With a result, only the part from that result on:
// what folding it makes a prefix cache read again (hygiene.scm); #f when
// the window does not show it.
func (l *Loop) contextTokens(c *scheme.Call) (scheme.Value, error) {
	if c.Origin == scheme.OriginModel {
		return scheme.Void, errors.New("not callable from a model-originated evaluation")
	}
	if !l.frozen {
		return scheme.Void, errors.New("the window is not fixed until the session has started")
	}
	if len(c.Args) > 1 {
		return scheme.Void, errors.New("expected at most a tool result")
	}
	events := l.host.EventsSoFar()
	pol, _ := l.policy()
	pol.HighWater = math.MaxInt
	built := window.Build(events, l.system, l.toolsLen, pol)
	if len(c.Args) == 0 {
		return scheme.Int(int64(built.EstTokens)), nil
	}
	seq, err := resultSeq(events, c.Args[0])
	if err != nil {
		return scheme.Void, err
	}
	n, ok := built.TokensFrom(seq)
	if !ok {
		return scheme.False, nil
	}
	return scheme.Int(int64(n)), nil
}
