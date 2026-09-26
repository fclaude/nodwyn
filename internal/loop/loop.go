// Package loop provides the agent loop.
//
// The loop itself — ask the model, run the tools it asked for, feed the
// results back, stop on a plain answer — is a Scheme procedure, agent-turn.
// Its default is strategy.scm, and a project may redefine it in
// .nibb/init.scm (ADR 26). This package keeps what a strategy must not be
// able to skip, and gives the strategy the primitives to work with:
// model-turn, run-tools, skip-tools and add-message.
//
// Reading order:
//
//   - loop.go: a session's Loop, and how one turn starts and ends
//   - strategy.scm: the default agent-turn
//   - prims.go: the primitives and the Scheme values they exchange
//   - model.go: one request to the model
//   - tools.go: one tool call, run or skipped
//   - turn.go: the per-turn bookkeeping that keeps the transcript well-formed
//   - progress.go: when a turn has stopped making progress
package loop

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/fclaude/nibb/internal/budget"
	"github.com/fclaude/nibb/internal/event"
	"github.com/fclaude/nibb/internal/llm"
	"github.com/fclaude/nibb/internal/log"
	"github.com/fclaude/nibb/internal/redact"
	"github.com/fclaude/nibb/internal/scheme"
	"github.com/fclaude/nibb/internal/window"
)

// Host is what the loop needs from its owner, the core session.
type Host interface {
	// Record appends an event to the session log.
	Record(kind log.Kind, payload any) (log.Event, error)
	// EventsSoFar returns the log so far; every window is rebuilt from it.
	EventsSoFar() []log.Event
	// Emit sends an ephemeral event to the front-end; it is not logged.
	Emit(ev event.Event)
}

// Config bounds the loop.
type Config struct {
	Model     string        // control-plane model name
	NumCtx    int           // context size requested from the model server
	KeepAlive string        // how long the server keeps the model loaded
	Window    window.Policy // when and how older context is elided
	// MaxToolCalls stops a turn that started no budget (budget.From is nil).
	MaxToolCalls   int
	RequestTimeout time.Duration // per model request
	System         string        // the system prompt, fixed for the session (ADR 9)
}

// Loop runs a session's turns. Build it with New before .nibb/init.scm
// loads and Freeze it after.
type Loop struct {
	host     Host
	provider llm.Provider
	interp   *scheme.Interp
	cfg      Config

	tools    []llm.Tool // offered to the model; fixed by Freeze
	toolsLen int        // estimated tokens of tools, for the window
	frozen   bool

	// turn is the turn in progress, nil between turns. The session runs one
	// turn at a time, so a field suffices.
	turn *turnState
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

// Turn runs one turn: agent-turn applied to request, the user message the
// session has already recorded. The strategy runs with harness origin; the
// tools it runs on the model's behalf run with model origin.
//
// Whatever the strategy did, every tool call the model made has a result
// when Turn returns, so the next request is well-formed. The error is why
// the turn stopped early: a budget or progress stop, a cancellation, a
// failed model request, or the strategy's own error.
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

// record appends to the log. A log that cannot be written ends the turn: the
// log is the source of truth, and a strategy that caught the failure would
// carry on unrecorded.
func (l *Loop) record(kind log.Kind, payload any) error {
	if _, err := l.host.Record(kind, payload); err != nil {
		return halt(err)
	}
	return nil
}

// WindowContext is the immutable, redacted recipe needed to replay requests.
// Tool schema marshaling already succeeded in Freeze.
func (l *Loop) WindowContext() *log.WindowContext {
	tools, _ := json.Marshal(l.tools)
	return &log.WindowContext{Version: window.RecipeVersion, System: l.cfg.System, Tools: tools, HighWater: l.cfg.Window.HighWater, LowWater: l.cfg.Window.LowWater, MaxResultTokens: l.cfg.Window.MaxResultTokens, NumCtx: l.cfg.NumCtx, KeepAlive: l.cfg.KeepAlive}
}
