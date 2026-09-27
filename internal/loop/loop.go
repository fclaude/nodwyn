// Package loop provides the agent loop.
//
// The loop itself — ask the model, run the tools it asked for, feed the
// results back, stop on a plain answer — is a Scheme procedure, agent-turn.
// Its default is strategy.scm, and a project may redefine it in
// .nibb/init.scm (ADR 26). This package keeps what a strategy must not be
// able to skip, and gives the strategy the primitives to work with:
// model-turn, run-tools, skip-tools, answer-call, add-message,
// narrow-context, fold-context, widen and session-system-prompt.
//
// Reading order:
//
//   - loop.go: a session's Loop, and how one turn starts and ends
//   - strategy.scm: the default agent-turn
//   - compact.scm: context compaction, as user code over the primitives
//   - agents.scm: sub-agents and agent tools, as user code over the primitives
//   - optimize.scm: the optimizer, a sub-agent that improves .nibb/init.scm
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
	"slices"
	"strings"
	"time"

	"github.com/fclaude/nibb/internal/budget"
	"github.com/fclaude/nibb/internal/event"
	"github.com/fclaude/nibb/internal/llm"
	"github.com/fclaude/nibb/internal/log"
	"github.com/fclaude/nibb/internal/plan"
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
	Window window.Policy // when and how older context is elided
	// Models are the models a strategy may ask by name: the [control] model
	// as ControlModel, which (model-turn) asks, and the configured registry
	// with (model-turn "NAME").
	Models         map[string]Endpoint
	RequestTimeout time.Duration // per model request
}

// ControlModel is the registry name of the [control] model.
const ControlModel = "control"

// Loop runs a session's turns. Build it with New before .nibb/init.scm
// loads and Freeze it after.
type Loop struct {
	host   Host
	interp *scheme.Interp
	cfg    Config

	tools    []llm.Tool // offered to the model; fixed by Freeze
	toolsLen int        // estimated tokens of tools, for the window
	system   string     // from ReadPrompts; fixed by Freeze (ADR 9)
	frozen   bool

	// turn is the turn in progress, nil between turns. The session runs one
	// turn at a time, so a field suffices.
	turn *turnState
}

// Endpoint is one model a strategy can ask by name.
type Endpoint struct {
	Provider  llm.Provider
	Model     string // model id as the provider names it
	NumCtx    int
	KeepAlive string
}

//go:embed strategy.scm
var defaultStrategy string

//go:embed prompts.scm
var defaultPrompts string

//go:embed compact.scm
var defaultCompaction string

//go:embed agents.scm
var defaultAgents string

//go:embed optimize.scm
var defaultOptimizer string

// New builds a loop and installs its primitives and the default strategy
// into interp. Call it before loading .nibb/init.scm, so the project may
// redefine agent-turn, and call Freeze afterwards. cfg.Models must hold the
// control model.
func New(host Host, interp *scheme.Interp, cfg Config) (*Loop, error) {
	if _, ok := cfg.Models[ControlModel]; !ok {
		return nil, errors.New("loop: no control model")
	}
	l := &Loop{host: host, interp: interp, cfg: cfg}
	if err := l.install(); err != nil {
		return nil, err
	}
	harness := scheme.WithOrigin(context.Background(), scheme.OriginHarness)
	if _, err := interp.EvalString(harness, defaultStrategy); err != nil {
		return nil, fmt.Errorf("loop: default strategy: %w", err)
	}
	if _, err := interp.EvalString(harness, defaultPrompts); err != nil {
		return nil, fmt.Errorf("loop: default prompts: %w", err)
	}
	if _, err := interp.EvalString(harness, defaultCompaction); err != nil {
		return nil, fmt.Errorf("loop: default compaction: %w", err)
	}
	if _, err := interp.EvalString(harness, defaultAgents); err != nil {
		return nil, fmt.Errorf("loop: default agents: %w", err)
	}
	if _, err := interp.EvalString(harness, defaultOptimizer); err != nil {
		return nil, fmt.Errorf("loop: default optimizer: %w", err)
	}
	return l, nil
}

// ReadPrompts evaluates the prompts as defined when it is called: the
// defaults in prompts.scm, or a project's redefinitions from .nibb/init.scm.
// It takes the system prompt for repo, which Freeze then fixes, and returns
// the planner's prompts. The planner's procedures are the ones bound now, so
// a later redefinition does not change a running session.
func (l *Loop) ReadPrompts(repo string) (plan.Prompts, error) {
	if l.frozen {
		return plan.Prompts{}, errors.New("loop: the system prompt is fixed once the session has started")
	}
	system, ps, err := l.readPrompts(repo)
	if err != nil {
		return plan.Prompts{}, err
	}
	l.system = system
	return ps, nil
}

// PromptChanges reads the prompts as now defined, after a reload, and names
// those that differ from what the session runs with: they are fixed for the
// session (ADR 9) and apply from the next one. The planner's prompts are
// compared by what they produce for a sample request.
func (l *Loop) PromptChanges(repo string, running plan.Prompts) ([]string, error) {
	system, ps, err := l.readPrompts(repo)
	if err != nil {
		return nil, err
	}
	var changed []string
	if redact.Redact(system, nil).Text != l.system {
		changed = append(changed, "system-prompt")
	}
	same := func(a, b func() (string, error)) bool {
		x, errX := a()
		y, errY := b()
		return errX == nil && errY == nil && x == y
	}
	for _, c := range []struct {
		name string
		same bool
	}{
		{"plan-system-prompt", ps.System == running.System},
		{"plan-draft-prompt", same(func() (string, error) { return ps.Draft("R") }, func() (string, error) { return running.Draft("R") })},
		{"plan-revise-prompt", same(func() (string, error) { return ps.Revise("R", "P", 0.5) }, func() (string, error) { return running.Revise("R", "P", 0.5) })},
		{"plan-needed-question", ps.Needed == running.Needed},
		{"plan-score-rubric", ps.ScoreRubric == running.ScoreRubric},
		{"plan-score-levels", slices.Equal(ps.ScoreLevels, running.ScoreLevels)},
	} {
		if !c.same {
			changed = append(changed, c.name)
		}
	}
	return changed, nil
}

// readPrompts evaluates the prompts as currently defined.
func (l *Loop) readPrompts(repo string) (string, plan.Prompts, error) {
	ctx := scheme.WithOrigin(context.Background(), scheme.OriginHarness)
	lookup := func(name string) (scheme.Value, error) {
		v, err := l.interp.EvalString(ctx, name)
		if err != nil {
			return scheme.Void, fmt.Errorf("loop: prompt %s: %w", name, err)
		}
		return v, nil
	}
	text := func(name string, v scheme.Value) (string, error) {
		if v.Kind != scheme.KString || strings.TrimSpace(v.S) == "" {
			return "", fmt.Errorf("loop: prompt %s must be a non-empty string, got %s", name, scheme.Write(v))
		}
		return v.S, nil
	}
	call := func(name string, proc scheme.Value, args ...scheme.Value) (string, error) {
		v, err := l.interp.Apply(ctx, proc, args)
		if err != nil {
			return "", fmt.Errorf("loop: prompt %s: %w", name, err)
		}
		return text(name, v)
	}
	procs := map[string]scheme.Value{}
	for _, name := range []string{"system-prompt", "plan-system-prompt", "plan-draft-prompt", "plan-revise-prompt"} {
		v, err := lookup(name)
		if err != nil {
			return "", plan.Prompts{}, err
		}
		if !v.IsProcedure() {
			return "", plan.Prompts{}, fmt.Errorf("loop: prompt %s must be a procedure", name)
		}
		procs[name] = v
	}
	system, err := call("system-prompt", procs["system-prompt"], scheme.Str(repo))
	if err != nil {
		return "", plan.Prompts{}, err
	}
	var ps plan.Prompts
	if ps.System, err = call("plan-system-prompt", procs["plan-system-prompt"], scheme.Str(repo)); err != nil {
		return "", plan.Prompts{}, err
	}
	for name, dst := range map[string]*string{"plan-needed-question": &ps.Needed, "plan-score-rubric": &ps.ScoreRubric} {
		v, err := lookup(name)
		if err != nil {
			return "", plan.Prompts{}, err
		}
		if *dst, err = text(name, v); err != nil {
			return "", plan.Prompts{}, err
		}
	}
	levels, err := lookup("plan-score-levels")
	if err != nil {
		return "", plan.Prompts{}, err
	}
	items, ok := levels.Slice()
	if !ok || len(items) < 2 || len(items) > 10 {
		return "", plan.Prompts{}, fmt.Errorf("loop: prompt plan-score-levels must be a list of 2 to 10 strings")
	}
	for _, item := range items {
		level, err := text("plan-score-levels", item)
		if err != nil {
			return "", plan.Prompts{}, err
		}
		ps.ScoreLevels = append(ps.ScoreLevels, level)
	}
	ps.Draft = func(request string) (string, error) {
		return call("plan-draft-prompt", procs["plan-draft-prompt"], scheme.Str(request))
	}
	ps.Revise = func(request, plan string, score float64) (string, error) {
		return call("plan-revise-prompt", procs["plan-revise-prompt"], scheme.Str(request), scheme.Str(plan), scheme.Float(score))
	}
	return system, ps, nil
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
	l.system = redact.Redact(l.system, nil).Text
	b, _ := json.Marshal(l.tools)
	l.toolsLen = window.Estimate(string(b))
	l.frozen = true
	return nil
}

// Tools returns the tool list offered to the model.
func (l *Loop) Tools() []llm.Tool { return l.tools }

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
	if unwindErr := l.unwind(); unwindErr != nil && err == nil {
		err = unwindErr
	}
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

// unwind folds the contexts a turn left open while any of them holds calls
// waiting for an answer, innermost first, answering what each left pending:
// a call must be followed by its result, so a sub-agent's context cannot
// stay between them. Contexts without waiting calls, such as a compaction's,
// stay open until the next user message.
func (l *Loop) unwind() error {
	t := l.turn
	for t.parked() {
		if err := l.skipPending("the turn ended before this call ran"); err != nil {
			return err
		}
		if err := l.fold(t); err != nil {
			return err
		}
		if err := l.skipPending("the turn ended before the sub-agent answering this call finished"); err != nil {
			return err
		}
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
	control := l.cfg.Models[ControlModel]
	return &log.WindowContext{Version: window.RecipeVersion, System: l.system, Tools: tools, HighWater: l.cfg.Window.HighWater, LowWater: l.cfg.Window.LowWater, MaxResultTokens: l.cfg.Window.MaxResultTokens, NumCtx: control.NumCtx, KeepAlive: control.KeepAlive}
}
