// Package loop provides the agent loop.
//
// The loop itself — ask the model, run the tools it asked for, feed the
// results back, stop on a plain answer — is a Scheme procedure, agent-turn,
// which turn applies after planning when the turn must plan. Their defaults
// are strategy.scm, and a project may redefine either in .nodwyn/init.scm
// (ADR 26). This package keeps what a strategy must not be
// able to skip, and gives the strategy the primitives to work with:
// model-turn, run-tools, skip-tools, answer-call, add-message,
// narrow-context, fold-context, widen, session-system-prompt and
// session-requests.
//
// Reading order:
//
//   - loop.go: a session's Loop, and how one turn starts and ends
//   - strategy.scm: the default agent-turn
//   - compact.scm: context compaction, as user code over the primitives
//   - agents.scm: sub-agents and agent tools, as user code over the primitives
//   - optimize.scm: the optimizer, a sub-agent that improves .nodwyn/init.scm
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
	"strings"
	"sync"
	"time"

	"github.com/fclaude/nodwyn/internal/budget"
	"github.com/fclaude/nodwyn/internal/event"
	"github.com/fclaude/nodwyn/internal/llm"
	"github.com/fclaude/nodwyn/internal/log"
	"github.com/fclaude/nodwyn/internal/redact"
	"github.com/fclaude/nodwyn/internal/scheme"
	"github.com/fclaude/nodwyn/internal/window"
)

// Host is what the loop needs from its owner, the core session.
type Host interface {
	// Record appends an event to the session log.
	Record(kind log.Kind, payload any) (log.Event, error)
	// EventsSoFar returns the log so far, or of a resumed session its end
	// from where the window and the loop's readers need it (ADR 49 §10);
	// every window is rebuilt from it.
	EventsSoFar() []log.Event
	// Emit sends an ephemeral event to the front-end; it is not logged.
	Emit(ev event.Event)
}

// Config bounds the loop.
type Config struct {
	Window window.Policy // when and how older context is elided
	// Ceiling is the most context a control-model request can carry: its
	// context length less the output reserve, in the window's estimate. The
	// room between the high water and the ceiling is for a request that may
	// run past the high water ((model-turn name '((headroom . #t))), the
	// handover note's). Not above the high water when the context length is
	// unknown, or when the reply reserve takes all the rest.
	Ceiling int
	// Delegates are the configured models the task's model may hand work to
	// (the delegate tool, agents.scm); none registers no such tool.
	Delegates []string
	// MaxOutput caps the tokens a model generates per request, thinking
	// included (window.output_reserve), so prompt and output fit num_ctx.
	MaxOutput int
	// Models are the models a strategy may ask by name: the [control] model
	// as ControlModel, which (model-turn) asks, and the configured registry
	// with (model-turn "NAME"). The control model may be swapped between
	// turns (SetControl), when a person chooses another model for this
	// conversation (ADR 40).
	Models         map[string]Endpoint
	RequestTimeout time.Duration // per model request
	// Root is the workspace root, under which attached images are stored
	// (.nodwyn/attachments); a request reads them from there.
	Root string
}

// ControlModel is the registry name of the [control] model.
const ControlModel = "control"

// Loop runs a session's turns. Build it with New before .nodwyn/init.scm
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

	// controlMu guards what SetControl replaces between turns: the control
	// model's slot in cfg.Models, the window's water marks and the ceiling.
	controlMu sync.RWMutex

	// vision caches, per model name, whether the model takes images.
	visionMu sync.Mutex
	vision   map[string]bool
}

// Endpoint is one model a strategy can ask by name.
type Endpoint struct {
	Provider llm.Provider
	Model    string // model id as the provider names it
	Host     string // host:port of its URL: where a request to it goes
	// Server names the [servers] entry it is reached through ("" for an
	// inline model), which tells two servers at one address apart.
	Server    string
	NumCtx    int
	KeepAlive string
	MaxOutput int // this model's reply cap; 0 uses Config.MaxOutput
	// Sampling settings sent with each request; nil leaves them to the server.
	Temperature *float64
	Seed        *int64
	// Rates price a request the provider does not report the cost of.
	Rates llm.Rates
	// Effort is the reasoning effort its requests ask for; empty leaves it to
	// the provider (ADR 41).
	Effort string
	// Labels are the config entry's labels: what a strategy picks a model by.
	Labels []string
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

//go:embed plan.scm
var defaultPlanGate string

// The working habits (ADR 32), loaded after the defaults they wrap, in this
// order: shell.scm wraps run-calls and fold-result innermost, hygiene.scm
// also wraps model-turn, readdef.scm feeds hygiene.scm's reads, and
// checkloop.scm wraps nodwyn-turn.
var (
	//go:embed shell.scm
	habitShell string
	//go:embed hygiene.scm
	habitHygiene string
	//go:embed readdef.scm
	habitReadDef string
	//go:embed envhint.scm
	habitEnvHint string
	//go:embed checkloop.scm
	habitCheckLoop string
)

// defaultHabits names each habit for load errors.
var defaultHabits = []struct{ name, src string }{
	{"shell", habitShell}, {"hygiene", habitHygiene}, {"read-definition", habitReadDef},
	{"environment hint", habitEnvHint}, {"check loop", habitCheckLoop},
}

// New builds a loop and installs its primitives and the default strategy
// into interp. Call it before loading .nodwyn/init.scm, so the project may
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
	if _, err := interp.EvalString(harness, defaultPlanGate); err != nil {
		return nil, fmt.Errorf("loop: default plan gate: %w", err)
	}
	for _, h := range defaultHabits {
		if _, err := interp.EvalString(harness, h.src); err != nil {
			return nil, fmt.Errorf("loop: default habit %s: %w", h.name, err)
		}
	}
	return l, nil
}

// ReadPrompts evaluates, as defined when it is called, what every request
// starts with: the system prompt, and the headers under which the window
// adds the user's request and its plan (task-header, plan-header). They are
// the defaults in prompts.scm or a project's redefinitions from
// .nodwyn/init.scm, and Freeze then fixes them (ADR 9). Every other prompt,
// the planner's included, is ordinary Scheme, read when it is used.
func (l *Loop) ReadPrompts(repo string) error {
	if l.frozen {
		return errors.New("loop: the system prompt is fixed once the session has started")
	}
	fixed, err := l.readFixedPrompts(repo)
	if err != nil {
		return err
	}
	l.system = fixed.system
	l.cfg.Window.TaskHeader, l.cfg.Window.PlanHeader = fixed.taskHeader, fixed.planHeader
	return nil
}

// PromptChanges reads those prompts as now defined, after a reload, and
// names each that differs from what the session runs with: they are fixed
// for the session (ADR 9) and apply from the next one.
func (l *Loop) PromptChanges(repo string) ([]string, error) {
	fixed, err := l.readFixedPrompts(repo)
	if err != nil {
		return nil, err
	}
	var changed []string
	if redact.Redact(fixed.system, nil).Text != l.system {
		changed = append(changed, "system-prompt")
	}
	if fixed.taskHeader != l.cfg.Window.TaskHeader {
		changed = append(changed, "task-header")
	}
	if fixed.planHeader != l.cfg.Window.PlanHeader {
		changed = append(changed, "plan-header")
	}
	return changed, nil
}

type fixedPrompts struct{ system, taskHeader, planHeader string }

// readFixedPrompts applies system-prompt to repo and reads the headers.
func (l *Loop) readFixedPrompts(repo string) (fixedPrompts, error) {
	var out fixedPrompts
	proc, err := l.evalPrompt("system-prompt")
	if err != nil {
		return out, err
	}
	if !proc.IsProcedure() {
		return out, errors.New("loop: prompt system-prompt must be a procedure")
	}
	if out.system, err = l.callPrompt("system-prompt", proc, scheme.Str(repo)); err != nil {
		return out, err
	}
	for name, dst := range map[string]*string{"task-header": &out.taskHeader, "plan-header": &out.planHeader} {
		v, err := l.evalPrompt(name)
		if err != nil {
			return out, err
		}
		if *dst, err = promptText(name, v); err != nil {
			return out, err
		}
	}
	return out, nil
}

// RunTurnPrompt is the message for a supervised run's turn:
// (run-turn-prompt task complete-line turn max-turns), read when it is used.
// completeLine is what the driver looks for, so the prompt names the marker
// the driver will recognise.
func (l *Loop) RunTurnPrompt(task, completeLine string, turn, maxTurns int) (string, error) {
	proc, err := l.evalPrompt("run-turn-prompt")
	if err != nil {
		return "", err
	}
	return l.callPrompt("run-turn-prompt", proc, scheme.Str(task), scheme.Str(completeLine), scheme.Int(int64(turn)), scheme.Int(int64(maxTurns)))
}

func (l *Loop) evalPrompt(name string) (scheme.Value, error) {
	v, err := l.interp.EvalString(scheme.WithOrigin(context.Background(), scheme.OriginHarness), name)
	if err != nil {
		return scheme.Void, fmt.Errorf("loop: prompt %s: %w", name, err)
	}
	return v, nil
}

func (l *Loop) callPrompt(name string, proc scheme.Value, args ...scheme.Value) (string, error) {
	v, err := l.interp.Apply(scheme.WithOrigin(context.Background(), scheme.OriginHarness), proc, args)
	if err != nil {
		return "", fmt.Errorf("loop: prompt %s: %w", name, err)
	}
	return promptText(name, v)
}

func promptText(name string, v scheme.Value) (string, error) {
	if v.Kind != scheme.KString || strings.TrimSpace(v.S) == "" {
		return "", fmt.Errorf("loop: prompt %s must be a non-empty string, got %s", name, scheme.Write(v))
	}
	return v.S, nil
}

// Freeze fixes the tool list offered to the model. It runs once, after
// .nodwyn/init.scm has registered its tools; the list must not change
// mid-session (ADR 9), so the interpreter takes no further registrations.
func (l *Loop) Freeze() error {
	l.interp.Freeze()
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

// TurnProcedure is the Scheme procedure the host applies to every user
// message (strategy.scm). Its name is specific to nodwyn, so a project's own
// definitions in init.scm, a counter called turn say, cannot shadow it.
const TurnProcedure = "nodwyn-turn"

// TurnStartProcedure is applied to the request just before TurnProcedure, so
// the state the default habits keep for one turn is reset on the host's side
// and survives a project that redefines nodwyn-turn (strategy.scm). A
// project that removes it loses only those resets.
const TurnStartProcedure = "turn-start"

// Turn runs one turn: the Scheme nodwyn-turn applied to request, the user message
// the session has already recorded. The strategy runs with harness origin; the
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
	strategy, ok := l.interp.Global().Lookup(TurnProcedure)
	if !ok || !strategy.IsProcedure() {
		return fmt.Errorf("loop: %s is not defined as a procedure; nodwyn calls (%s request) for every message", TurnProcedure, TurnProcedure)
	}
	l.turn = &turnState{budget: budget.From(ctx)}
	defer func() { l.turn = nil }()

	harness := scheme.WithOrigin(ctx, scheme.OriginHarness)
	args := []scheme.Value{scheme.Str(request)}
	var err error
	if start, ok := l.interp.Global().Lookup(TurnStartProcedure); ok && start.IsProcedure() {
		_, err = l.interp.Apply(harness, start, args)
	}
	if err == nil {
		_, err = l.interp.Apply(harness, strategy, args)
	}
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

// SetControl makes ep the model (model-turn) asks, from the next request on,
// with the window sized for it (marks' water marks and result cap, and
// ceiling): a person chose another model for this conversation (ADR 40).
// record is given the recipe the requests that follow are built by, before
// anything changes, and nothing does if it fails. The caller must not have a
// turn running: a turn under way reads the control model, and a switch must
// not change the request it is making.
func (l *Loop) SetControl(ep Endpoint, marks window.Policy, ceiling int, record func(*log.WindowContext) error) error {
	l.controlMu.Lock()
	defer l.controlMu.Unlock()
	pol := l.cfg.Window
	pol.HighWater, pol.LowWater, pol.MaxResultTokens = marks.HighWater, marks.LowWater, marks.MaxResultTokens
	if err := record(l.recipe(ep, pol)); err != nil {
		return err
	}
	// Only the marks change: the prompts' headers stay the session's.
	l.cfg.Models[ControlModel], l.cfg.Ceiling = ep, ceiling
	l.cfg.Window.HighWater, l.cfg.Window.LowWater, l.cfg.Window.MaxResultTokens = pol.HighWater, pol.LowWater, pol.MaxResultTokens
	// Whether a model takes images is asked of the model, not of the name
	// it answers to: the next model is asked again.
	l.visionMu.Lock()
	delete(l.vision, ControlModel)
	l.visionMu.Unlock()
	return nil
}

// SetEffort changes the reasoning effort the control model's requests ask
// for, from the next request on, and nothing else (ADR 41): a person chose
// another effort of the model the conversation already talks to. record is
// given the recipe first, as SetControl's is, and nothing changes if it fails.
// As for SetControl, no turn may be running.
func (l *Loop) SetEffort(effort string, record func(*log.WindowContext) error) error {
	l.controlMu.Lock()
	defer l.controlMu.Unlock()
	ep := l.cfg.Models[ControlModel]
	if err := record(l.recipe(ep, l.cfg.Window)); err != nil {
		return err
	}
	ep.Effort = effort
	l.cfg.Models[ControlModel] = ep
	return nil
}

// policy is the window's policy and ceiling now, which a switch may change
// between turns.
func (l *Loop) policy() (window.Policy, int) {
	l.controlMu.RLock()
	defer l.controlMu.RUnlock()
	return l.cfg.Window, l.cfg.Ceiling
}

// ResultCap is the window's cap on one tool result now, in its tokens: a
// switch to another model may change it.
func (l *Loop) ResultCap() int {
	pol, _ := l.policy()
	return pol.MaxResultTokens
}

// WindowTokens is the window's estimate of the next model-turn's request
// were the window's water marks and result cap marks': what choosing another
// model compares with the context that model reads (ADR 40).
func (l *Loop) WindowTokens(marks window.Policy) int {
	pol, _ := l.policy()
	pol.HighWater, pol.LowWater, pol.MaxResultTokens = marks.HighWater, marks.LowWater, marks.MaxResultTokens
	return window.Build(l.host.EventsSoFar(), l.system, l.toolsLen, pol).EstTokens
}

// WindowContext is the immutable, redacted recipe needed to replay requests.
// Tool schema marshaling already succeeded in Freeze.
func (l *Loop) WindowContext() *log.WindowContext {
	l.controlMu.RLock()
	defer l.controlMu.RUnlock()
	return l.recipe(l.cfg.Models[ControlModel], l.cfg.Window)
}

// recipe is the recipe of requests to control built with the window pol.
func (l *Loop) recipe(control Endpoint, pol window.Policy) *log.WindowContext {
	tools, _ := json.Marshal(l.tools)
	return &log.WindowContext{Version: window.RecipeVersion, System: l.system, Tools: tools, HighWater: pol.HighWater, LowWater: pol.LowWater, MaxResultTokens: pol.MaxResultTokens, NumCtx: control.NumCtx, KeepAlive: control.KeepAlive,
		TaskHeader: pol.TaskHeader, PlanHeader: pol.PlanHeader, MaxOutput: l.cfg.MaxOutput}
}
