// Package core is the UI-agnostic façade over the harness (ADR 10). A
// Session owns the interpreter, the guard, the log and the loop; a
// front-end drives it with Send/Answer/Abort and watches Events. Nothing in
// here knows what a terminal is.
package core

import (
	"context"
	"errors"
	"github.com/fclaude/nodwyn/internal/config"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/fclaude/nodwyn/internal/binding"
	"github.com/fclaude/nodwyn/internal/budget"
	"github.com/fclaude/nodwyn/internal/event"
	"github.com/fclaude/nodwyn/internal/guard"
	"github.com/fclaude/nodwyn/internal/keychain"
	"github.com/fclaude/nodwyn/internal/llm"
	"github.com/fclaude/nodwyn/internal/log"
	"github.com/fclaude/nodwyn/internal/loop"
	"github.com/fclaude/nodwyn/internal/prim"
	"github.com/fclaude/nodwyn/internal/scheme"
)

// Session is one conversation with one log.
type Session struct {
	opts     Options
	id       string
	logw     *log.Writer
	events   []log.Event
	evmu     sync.Mutex
	outputMu sync.Mutex
	output   strings.Builder
	out      chan event.Event
	interp   *scheme.Interp
	loop     *loop.Loop

	mu         sync.Mutex
	busy       bool
	turnCancel context.CancelFunc
	// attaching: an attachment is being added, perhaps waiting for an
	// image's confirm (attach.go); no turn starts meanwhile.
	attaching    bool
	attachCancel context.CancelFunc
	// switching: a model switch is in progress (ADR 40); no turn or
	// attachment starts meanwhile, since the switch records what it is about
	// to do before it changes the model.
	switching bool
	closed    bool
	closing   bool
	turns     sync.WaitGroup // includes final event delivery after Busy becomes false
	closeOnce sync.Once
	closeErr  error

	// model is the model this conversation talks to now (ADR 40): config.toml's
	// [control] model, or the one a person chose for it. It changes only
	// between turns, under mu, and opts.Config.Control always keeps the
	// configured [control] model, so a conversation can be moved back to it.
	model config.Model

	// The plan a turn must record before it may do anything irreversible
	// (SendPlanned; plan.go).
	planRequired atomic.Bool
	planned      atomic.Bool
	// reviewed: a person reads this turn's answer before anything else
	// happens (plan.go, (reviewed?)).
	reviewed atomic.Bool
	// The turn's task and recorded plan, for the judge when it answers a
	// confirm (judge.go).
	turnMu   sync.Mutex
	turnTask string
	turnPlan string
	// The upload setting this session chose (uploads.go), when it has no
	// config file to keep it in.
	uploadMu      sync.Mutex
	uploadSetting string
	uploadSet     bool
	// The spend limit's soft limit as a checkpoint last raised it, and
	// whether a turn has said the limit cannot see a request's cost
	// (spend.go). Both under mu.
	costSoft        int64
	spendNoted      bool
	judgeSpendNoted bool
	prompts         map[string]pendingConfirmation
	promptMu        sync.Mutex

	allow  *guard.CommandAllow
	writes *guard.WriteAllow
	// turnTrust: writes are approved without a prompt until the turn ends
	// ("t" at a write prompt). Reset when the turn goes idle.
	turnTrust bool
	turnActor string
	// approveAll: ordinary writes and commands are approved without a
	// prompt until it is turned off or the session closes, as the person
	// who turned it on (approveActor) chose. Go only: no primitive reads or
	// changes it, since a repository's init.scm runs as user-origin Scheme
	// (confirm.go, SetApproveAll).
	approveAll   bool
	approveActor string

	// P3 egress.
	keychain *keychain.Store
	bindings *binding.Live
	scratch  string      // the session's scratch directory (scratch.go)
	procs    *prim.Procs // background processes; stopped when the session closes
	// workspace is the repository's .nodwyn/workspace.toml as loaded at
	// construction: its kind and mounted folders (ADR 22).
	workspace config.Workspace
	// MCP servers this session connected to (mcp.go); checks made while the
	// session was being built wait for session.start.
	mcpMu     sync.Mutex
	mcp       []MCPServer
	contained []containedServer // running contained servers, stopped by Close
	started   bool
	held      []heldRecord

	// P3 credential prompts: pending masked-secret prompts.
	secrets   map[string]pendingSecret
	secretsMu sync.Mutex
}

// ID is the session id (also the log file's base name).
func (s *Session) ID() string { return s.id }

// Repo is the pinned repository root.
func (s *Session) Repo() string { return s.opts.Repo }

// Tools lists the tools offered to the model.
func (s *Session) Tools() []llm.Tool { return s.loop.Tools() }

// Limits are the budget this session's next turn starts from: the
// configuration it was opened with, whatever Settings has changed since.
func (s *Session) Limits() budget.Limits {
	s.mu.Lock()
	defer s.mu.Unlock()
	limits, _ := s.limits()
	return limits
}

// limits computes a turn's budget; s.mu is held. The spend limit is the
// task's: a soft limit raised at a checkpoint holds for later turns (and
// each turn starts from what the earlier ones cost).
func (s *Session) limits() (budget.Limits, error) {
	limits, err := s.opts.Config.Budget.Limits(s.opts.Config.Loop.MaxToolCalls)
	if err != nil {
		return budget.Limits{}, err
	}
	if limits.Cost.Hard > 0 && s.costSoft > limits.Cost.Soft && s.costSoft <= limits.Cost.Hard {
		limits.Cost.Soft = s.costSoft
	}
	return limits, nil
}

// Events is the stream a front-end reads. It is closed by Close.
func (s *Session) Events() <-chan event.Event { return s.out }

// Send starts a turn for the user's text. It returns immediately; the turn
// runs in the background and reports through Events, ending with a status
// event whose State is "idle".
func (s *Session) Send(text string) error { return s.send(text, false) }

// SendPlanned requires a recorded, nonempty plan before executing host work.
// Long-run drivers use this without changing the ordinary one-turn loop.
func (s *Session) SendPlanned(text string) error { return s.send(text, true) }

// SendRunTurn starts a supervised run's turn with the message
// (run-turn-prompt task complete-line turn max-turns), which is Scheme like
// every other prompt; the turn must plan, as with SendPlanned.
func (s *Session) SendRunTurn(task, completeLine string, turn, maxTurns int) error {
	return s.startTurn(func(ctx context.Context) error {
		// Evaluated here, inside the turn, where the interpreter is the
		// turn's alone.
		text, err := s.loop.RunTurnPrompt(task, completeLine, turn, maxTurns)
		if err != nil {
			return err
		}
		// A run checkpoint follows every turn but the last, and a person
		// answers it unless the run is unattended.
		return s.turn(ctx, text, true, !s.opts.Unattended && turn < maxTurns)
	})
}

// send starts a turn on text. A chat's reply goes to the person who sent
// it; a planned turn outside a run (SendPlanned) is taken as unreviewed.
func (s *Session) send(text string, required bool) error {
	return s.startTurn(func(ctx context.Context) error { return s.turn(ctx, text, required, !required) })
}

// turn records the user's message and runs the Scheme turn on it, which
// plans when the turn must and then applies agent-turn (strategy.scm). What
// stays here is the invariant no Scheme can skip: a turn that must plan does
// nothing irreversible before it records a plan, and fails if it records
// none (plan.go).
func (s *Session) turn(ctx context.Context, text string, required, reviewed bool) error {
	s.planRequired.Store(required)
	s.planned.Store(false)
	s.reviewed.Store(reviewed)
	// The flags are the turn's: an evaluation after it, even after a
	// required turn that failed unplanned, is not held to its plan.
	defer func() {
		s.planRequired.Store(false)
		s.planned.Store(false)
		s.reviewed.Store(false)
	}()
	safe, err := s.Record(log.KindUserMsg, log.UserMsg{Text: text})
	if err != nil {
		return err
	}
	var msg log.UserMsg
	if err := safe.Decode(&msg); err != nil {
		return err
	}
	s.setTurnTask(msg.Text)
	touchScratch(s.scratch)
	if err := s.loop.Turn(ctx, msg.Text); err != nil {
		return err
	}
	if required && !s.planned.Load() {
		return errors.New("required plan: the turn recorded no plan")
	}
	return nil
}

// startTurn serializes model turns and user evaluations under one cancellation
// and budget lifetime. Neither kind of work can mutate the interpreter while
// the other is running.
func (s *Session) startTurn(work func(context.Context) error) error {
	s.mu.Lock()
	if s.closed || s.closing {
		s.mu.Unlock()
		return errors.New("core: session is closed")
	}
	if s.busy {
		s.mu.Unlock()
		return errors.New("core: a turn is already running")
	}
	if s.attaching {
		s.mu.Unlock()
		return errors.New("core: an attachment is in progress; send when it is done")
	}
	if s.switching {
		s.mu.Unlock()
		return errors.New("core: the model is being switched; send when it is done")
	}
	limits, err := s.limits()
	if err != nil {
		s.mu.Unlock()
		return err
	}
	ctx, turn, cancel := budget.Start(context.Background(), limits, s.confirmBudget)
	if limits.Cost.Hard > 0 {
		turn.SetEarlierCost(s.knownSpend())
	}
	s.busy = true
	s.turnCancel = cancel
	s.turns.Add(1)
	s.mu.Unlock()

	go func() {
		var turnErr error
		defer s.turns.Done()
		defer func() {
			if limits.Cost.Hard > 0 {
				s.endSpend(turn)
			}
			_, _ = s.Record(log.KindTurnEnd, turnOutcome(ctx, turnErr))
			s.flushOutput()
			cancel()
			s.mu.Lock()
			s.busy = false
			s.turnCancel = nil
			s.turnTrust = false
			s.turnActor = ""
			s.mu.Unlock()
			s.Emit(event.Event{Kind: event.Status, Payload: event.StatusInfo{State: "idle"}})
		}()
		if err := work(ctx); err != nil {
			turnErr = err
			if ctx.Err() != nil {
				err = context.Cause(ctx)
				turnErr = err
				_, _ = s.Record(log.KindNote, log.Note{Text: err.Error()})
			}
			s.Emit(event.Event{Kind: event.Error, Payload: event.ErrorInfo{Err: err.Error()}})
		}
	}()
	return nil
}

// Busy reports whether a turn is running.
func (s *Session) Busy() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.busy
}

// Abort cancels the running turn, if any: the model request is dropped, a
// running tool is killed, a pending confirm is answered "no". An attachment
// waiting for its confirm is not attached.
func (s *Session) Abort() {
	s.mu.Lock()
	cancel, attach := s.turnCancel, s.attachCancel
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if attach != nil {
		attach()
	}
}

// Eval evaluates Scheme with user origin (a front-end command). Output is
// delivered as Output events; the result is returned.
func (s *Session) Eval(ctx context.Context, src string) (scheme.Value, error) {
	defer s.flushOutput()
	return s.interp.EvalString(scheme.WithOrigin(ctx, scheme.OriginUser), src)
}

// Close ends the session: aborts a running turn, writes session.end, closes
// the log and the events channel.
func (s *Session) Close() error {
	s.closeOnce.Do(func() {
		s.mu.Lock()
		s.closing = true // no new Send may race with the wait
		s.mu.Unlock()
		s.Abort()
		// The frontend must keep draining until the channel closes. Busy alone
		// cannot prove completion: the turn still owes its final idle event.
		s.turns.Wait()
		s.mu.Lock()
		s.closed = true
		s.mu.Unlock()
		if s.procs != nil {
			s.procs.Close()
		}
		// Contained MCP servers stop before session.end, so their last
		// proxy reports are recorded.
		s.closeMCP()
		_, _ = s.logw.Append(log.KindSessionEnd, log.SessionEnd{Reason: "closed"})
		s.closeErr = s.logw.Close()
		removeScratch(s.scratch)
		close(s.out)
	})
	return s.closeErr
}

// evalScheme is the SchemeEval guard.WithSchemeEval uses to run a
// register-check predicate.
func (s *Session) evalScheme(ctx context.Context, proc scheme.Value, args []scheme.Value) (scheme.Value, error) {
	return s.interp.Apply(ctx, proc, args)
}
