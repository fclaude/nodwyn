// Package loop is the mechanism under the agent loop. The loop itself — ask
// the model, run the tools it asked for, feed the results back, stop on a
// plain answer — is a Scheme procedure, agent-turn (strategy.scm), which a
// project may redefine in .nibb/init.scm.
//
// What stays here is what a strategy must not be able to skip, exposed as
// primitives it calls (prims.go): building the window from the log and
// recording each request for replay, running only calls the model made and
// giving every one exactly one result, the budgets, and the progress guard.
// The SPEC's division, restored: Scheme owns orchestration, Go owns I/O and
// the invariants.
package loop

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/fclaude/nibb/internal/budget"
	"github.com/fclaude/nibb/internal/event"
	"github.com/fclaude/nibb/internal/llm"
	"github.com/fclaude/nibb/internal/log"
	"github.com/fclaude/nibb/internal/redact"
	"github.com/fclaude/nibb/internal/scheme"
	"github.com/fclaude/nibb/internal/window"
)

// Host is what the loop needs from its owner (the core session): a way to
// record log events, the events so far (the window is rebuilt from them),
// and a way to emit ephemeral events to the front-end.
type Host interface {
	Record(kind log.Kind, payload any) (log.Event, error)
	EventsSoFar() []log.Event
	Emit(ev event.Event)
}

// Config bounds the loop.
type Config struct {
	Model          string
	NumCtx         int
	KeepAlive      string
	Window         window.Policy
	MaxToolCalls   int
	RequestTimeout time.Duration
	System         string
}

// Loop runs turns.
type Loop struct {
	host     Host
	provider llm.Provider
	interp   *scheme.Interp
	cfg      Config
	tools    []llm.Tool
	toolsLen int
	frozen   bool

	// turn is the state of the turn in progress, nil between turns. The
	// session runs one turn at a time, so a field suffices.
	turn *turnState
}

// turnState is what the primitives share for one turn.
type turnState struct {
	budget   *budget.Turn // nil when no budget was started (legacy callers)
	calls    int          // tool calls, for the legacy MaxToolCalls limit
	progress turnProgress
	// pending holds the calls from the latest model response that have no
	// result yet. Only these can be run or skipped, and each once.
	pending []pendingCall
}

type pendingCall struct {
	call     llm.ToolCall
	answered bool
}

//go:embed strategy.scm
var defaultStrategy string

// New builds a loop and installs its primitives and the default strategy
// into interp. Call it before loading .nibb/init.scm, so the project may
// redefine agent-turn, and call Freeze afterwards.
func New(host Host, provider llm.Provider, interp *scheme.Interp, cfg Config) (*Loop, error) {
	l := &Loop{host: host, provider: provider, interp: interp, cfg: cfg}
	if err := l.install(); err != nil {
		return nil, err
	}
	if _, err := interp.EvalString(scheme.WithOrigin(context.Background(), scheme.OriginHarness), defaultStrategy); err != nil {
		return nil, fmt.Errorf("loop: default strategy: %w", err)
	}
	return l, nil
}

// Freeze fixes the tool list offered to the model. It runs once, after
// .nibb/init.scm has registered its tools; the list must not change
// mid-session (ADR 9).
func (l *Loop) Freeze() error {
	l.tools = nil
	for _, p := range l.interp.Tools() {
		l.tools = append(l.tools, llm.Tool{Name: p.Name, Description: p.Meta.Doc, Parameters: p.Schema()})
	}
	clean, _, err := redact.Payload(l.tools, nil)
	if err != nil {
		return fmt.Errorf("loop: redact tool schemas: %w", err)
	}
	l.tools = clean.([]llm.Tool)
	l.cfg.System = redact.Redact(l.cfg.System, nil).Text
	b, _ := json.Marshal(l.tools)
	l.toolsLen = window.Estimate(string(b))
	l.frozen = true
	return nil
}

// Tools returns the tool list offered to the model.
func (l *Loop) Tools() []llm.Tool { return l.tools }

// ErrHardStop is returned when a turn exceeds MaxToolCalls. The turn's work
// is in the log; a "continue" message starts a fresh budget.
var ErrHardStop = errors.New("hard stop: tool-call limit for this turn reached (loop.max_tool_calls / --max-tool-calls); say \"continue\" to go on")

// Turn runs the strategy for a user message the session has already
// recorded. The strategy runs with harness origin; the tools it runs on the
// model's behalf run with model origin.
func (l *Loop) Turn(ctx context.Context, request string) error {
	if !l.frozen {
		return errors.New("loop: Freeze was not called")
	}
	strategy, ok := l.interp.Global().Lookup("agent-turn")
	if !ok || !strategy.IsProcedure() {
		return errors.New("loop: agent-turn is not defined as a procedure")
	}
	l.turn = &turnState{budget: budget.From(ctx)}
	defer func() { l.turn = nil }()

	_, err := l.interp.Apply(scheme.WithOrigin(ctx, scheme.OriginHarness), strategy, []scheme.Value{scheme.Str(request)})
	// Every call the model made gets exactly one result, whatever the
	// strategy did with it: an unanswered call would make the next request
	// malformed.
	if skipErr := l.skipPending("the turn ended before this call ran"); skipErr != nil && err == nil {
		err = skipErr
	}
	if err != nil {
		return surfaced(ctx, err)
	}
	if l.turn.budget != nil {
		return l.turn.budget.Finish(ctx)
	}
	return nil
}

// surfaced turns an error that came up through Scheme into the one the
// session reports: the cancellation cause, the stop itself, or the Go error
// a primitive raised — never its Scheme wrapping, which only adds the
// primitive's name.
func surfaced(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return context.Cause(ctx)
	}
	var halt *scheme.Halt
	if errors.As(err, &halt) {
		return halt.Err
	}
	var se *scheme.Error
	if errors.As(err, &se) && se.Cause != nil {
		return se.Cause
	}
	return err
}

// runTool evaluates one tool call as a Scheme application (ADR 3) with
// model origin, and records the result the model will see. Only context
// cancellation or recording failure is returned as an error; operation
// failures are tool results, with a typed outcome for turn policy. value
// is what a strategy receives: the recorded text, still carrying the
// provenance of the value the tool produced (L2).
func (l *Loop) runTool(ctx context.Context, tc llm.ToolCall) (log.ToolResult, scheme.Value, toolOutcome, error) {
	var outcome toolOutcome
	res := log.ToolResult{ID: tc.ID, Name: tc.Name}
	if _, err := l.host.Record(log.KindToolCall, log.ToolCallEvent{
		ToolCall: log.ToolCall{ID: tc.ID, Name: tc.Name, Args: tc.Args}, Origin: string(scheme.OriginModel),
	}); err != nil {
		return res, scheme.Void, outcome, err
	}
	l.host.Emit(event.Event{Kind: event.Status, Payload: event.StatusInfo{State: "busy", Detail: "running " + tc.Name}})
	start := time.Now()
	produced, err := l.apply(ctx, tc)
	res.Millis = time.Since(start).Milliseconds()
	if err != nil {
		if ctx.Err() != nil {
			cause := context.Cause(ctx)
			res.Error = "Interrupted during execution; outcome may be partial: " + cause.Error()
			if _, recordErr := l.host.Record(log.KindToolResult, res); recordErr != nil {
				return res, scheme.Void, outcome, recordErr
			}
			return res, scheme.Void, outcome, cause
		}
		outcome.failed = true
		var denied *scheme.Denied
		outcome.denied = errors.As(err, &denied)
		res.OK = false
		res.Error = err.Error()
	} else {
		res.OK = true
		res.Value = render(produced)
	}
	_, err = l.host.Record(log.KindToolResult, res)
	return res, scheme.Tainted(scheme.Str(res.Value), produced.Taint), outcome, err
}

func (l *Loop) apply(ctx context.Context, tc llm.ToolCall) (scheme.Value, error) {
	p, ok := l.interp.Lookup(tc.Name)
	if !ok || !p.Tool() {
		return scheme.Void, fmt.Errorf("unknown tool %q; available: %s", tc.Name, l.toolNames())
	}
	args, err := p.ArgsFromJSON(tc.Args)
	if err != nil {
		return scheme.Void, err
	}
	return l.interp.Apply(scheme.WithOrigin(ctx, scheme.OriginModel), scheme.Value{Kind: scheme.KPrim, Prim: p}, args)
}

// render turns a tool's value into what the model reads: strings as-is,
// void as "ok", everything else as JSON (falling back to the written form).
func render(v scheme.Value) string {
	switch v.Kind {
	case scheme.KString:
		return v.S
	case scheme.KVoid:
		return "ok"
	}
	if b, err := scheme.ToJSON(v); err == nil {
		return string(b)
	}
	return scheme.Write(v)
}

func (l *Loop) toolNames() string {
	names := make([]string, len(l.tools))
	for i, t := range l.tools {
		names[i] = t.Name
	}
	return strings.Join(names, ", ")
}

// skipTools completes the transcript for calls that a policy stop prevented.
// No Scheme evaluation or side effect occurs for these calls.
func (l *Loop) skipTools(calls []llm.ToolCall, reason string) error {
	for _, call := range calls {
		if _, err := l.host.Record(log.KindToolResult, log.ToolResult{ID: call.ID, Name: call.Name, OK: false, Error: "Not executed: " + reason}); err != nil {
			return err
		}
	}
	return nil
}

// WindowContext is the immutable, redacted recipe needed to replay requests.
// Tool schema marshaling already succeeded in New.
func (l *Loop) WindowContext() *log.WindowContext {
	tools, _ := json.Marshal(l.tools)
	return &log.WindowContext{Version: window.RecipeVersion, System: l.cfg.System, Tools: tools, HighWater: l.cfg.Window.HighWater, LowWater: l.cfg.Window.LowWater, MaxResultTokens: l.cfg.Window.MaxResultTokens, NumCtx: l.cfg.NumCtx, KeepAlive: l.cfg.KeepAlive}
}
