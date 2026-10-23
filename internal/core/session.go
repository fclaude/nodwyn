// Package core is the UI-agnostic façade over the harness (ADR 10). A
// Session owns the interpreter, the guard, the log and the loop; a
// front-end drives it with Send/Answer/Abort and watches Events. Nothing in
// here knows what a terminal is.
package core

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/fclaude/brote/scheme"
	"github.com/fclaude/nodwyn/internal/binding"
	"github.com/fclaude/nodwyn/internal/budget"
	"github.com/fclaude/nodwyn/internal/config"
	"github.com/fclaude/nodwyn/internal/event"
	"github.com/fclaude/nodwyn/internal/guard"
	"github.com/fclaude/nodwyn/internal/keychain"
	"github.com/fclaude/nodwyn/internal/llm"
	"github.com/fclaude/nodwyn/internal/log"
	"github.com/fclaude/nodwyn/internal/loop"
	"github.com/fclaude/nodwyn/internal/mcp"
	"github.com/fclaude/nodwyn/internal/prim"
)

// Session is one conversation with one log.
type Session struct {
	schedule     atomic.Pointer[log.SessionSchedule]
	scheduledRun atomic.Pointer[log.ScheduleRun]
	handover     atomic.Pointer[log.ContextHandover]
	initSource   []byte                 // The exact project startup snapshot whose trust was checked.
	initDefaults map[string]initDefault // What each global init.scm changed was bound to before it (reload.go).
	opts         Options
	profile      string // the default loop's profile, config.ProfileLean or config.ProfileFull (ResolveProfile)
	id           string
	logw         *log.Writer
	events       []log.Event
	evmu         sync.Mutex // guards events
	recMu        sync.Mutex // keeps the log's, the events' and the stream's order the same (Record)
	outputMu     sync.Mutex
	output       strings.Builder
	out          chan event.Event
	interp       *scheme.Interp
	loop         *loop.Loop

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
	// effort is the reasoning effort its requests ask for (ADR 41), one the
	// model takes; empty asks none.
	effort string
	// queued is the choice a person made while a turn ran, applied when the
	// turn ends (SetModel); modelNotice is why the last one was not, and
	// modelLost says the model the conversation was left on can no longer be
	// reached as chosen, so no turn is asked until one is (ModelState).
	queued      *ModelChoice
	modelNotice string
	modelLost   string
	// contextUnknown is why the window keeps its default size, when nothing
	// says how much context the model holds (sizeWindow, ModelState).
	contextUnknown string
	// modelMu serializes choosing a model with the end of the turn that
	// applies a queued choice. It is held while a switch sizes the window,
	// which may ask the server's model list, so nothing that a turn needs
	// takes it.
	modelMu sync.Mutex
	// judge is the [servers.NAME] the task's judge is on, "" for none (ADR
	// 47): config.toml's [judge] server, or the one a person chose for the
	// task. It changes only while the task is idle, under mu.
	judge string
	// delegates are the configured models the task may hand work to with its
	// delegate tool, chosen as it opened (ADR 27, amended 2026-10-09). The
	// tool list is fixed for the session, so they are fixed too.
	delegates []string
	// limitsOff: the task's turns have no turn limits (ADR 49), as
	// limitsActor chose; under mu, read as each turn starts. limitsMu
	// serializes choices, so the log's last record is the one in effect.
	limitsOff   bool
	limitsActor string
	limitsMu    sync.Mutex
	// The plan a turn must record before it may do anything irreversible
	// (SendPlanned; plan.go).
	planRequired atomic.Bool
	planned      atomic.Bool
	// reviewed: a person reads this turn's answer before anything else
	// happens (plan.go, (reviewed?)).
	reviewed atomic.Bool
	// The turn's task, in a chat with the person's messages before it, its
	// recorded plan with the steps marked so far, and what was answered
	// about each call this turn, for the judge when it answers a confirm
	// (judge.go).
	turnMu      sync.Mutex
	turnTask    string
	turnEarlier []string
	turnPlan    string
	turnMarks   map[int]log.PlanStep
	turnAnswers map[string][]string
	// The upload setting this session chose (uploads.go), when it has no
	// config file to keep it in.
	uploadMu      sync.Mutex
	uploadSetting string
	uploadSet     bool
	// The spend limit's soft limit as a checkpoint last raised it, and
	// whether a turn has said the limit cannot see a request's cost
	// (spend.go). Both under mu.
	costSoft   int64
	spendNoted bool
	// spendBefore is what the requests a resumed session does not hold in
	// memory cost (knownSpend); fixed when it resumes.
	spendBefore     float64
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
	scratch  string       // the session's scratch directory (scratch.go)
	keeper   *prim.Keeper // the state a passing test run kept, for a hard stop (keep.go)
	procs    *prim.Procs  // background processes; stopped when the session closes
	terms    *prim.Terms  // experimental terminals (ADR 48); nil unless [experimental] terminal is set
	// workspace is the folders outside the root the session may use, as
	// the workspace list named them at construction (ADR 22).
	workspace config.Workspace
	// worktree is the git worktree made for the session, which is its root;
	// nil when it works in the folder itself (ADR 34, worktree.go).
	worktree *log.Worktree
	// MCP servers this session connected to (mcp.go); checks made while the
	// session was being built wait for session.start.
	mcpMu     sync.Mutex
	mcp       []MCPServer
	remote    []*mcp.Client     // remote servers' clients, whose sessions Close ends
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
// each turn starts from what the earlier ones cost). A task whose limits
// are off keeps only that one (ADR 49).
func (s *Session) limits() (budget.Limits, error) {
	limits, err := s.opts.Config.Budget.Limits(s.opts.Config.Loop.MaxToolCalls)
	if err != nil {
		return budget.Limits{}, err
	}
	if run := s.scheduledRun.Load(); run != nil {
		limits.Cost = scheduledCost(run.Cost)
	}
	if s.limitsOff {
		limits = withoutTurnLimits(limits)
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
	if err := s.modelAvailable(); err != nil {
		return err
	}
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
	if err := s.modelAvailable(); err != nil {
		return err
	}
	return s.startTurn(func(ctx context.Context) error { return s.turn(ctx, text, required, !required) })
}

// turn runs a model turn on text and, when it succeeded, prepares the
// handover of a scheduled task. Only a conversation has something to hand
// over: an evaluation, a policy read or a friction read ends without it, so
// opening Settings never costs a model request.
func (s *Session) turn(ctx context.Context, text string, required, reviewed bool) error {
	if err := s.runTurn(ctx, text, required, reviewed); err != nil {
		return err
	}
	s.prepareHandover(ctx)
	return nil
}

// runTurn records the user's message and runs the Scheme turn on it, which
// plans when the turn must and then applies agent-turn (strategy.scm). What
// stays here is the invariant no Scheme can skip: a turn that must plan does
// nothing irreversible before it records a plan, and fails if it records
// none (plan.go).
func (s *Session) runTurn(ctx context.Context, text string, required, reviewed bool) error {
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
	// A chat's message may answer the model or correct it, so the judge
	// reads it after the ones before it; a run's turns each repeat its task.
	var earlier []string
	if !required {
		earlier = earlierRequests(s.EventsSoFar())
	}
	safe, err := s.Record(log.KindUserMsg, log.UserMsg{Text: text})
	if err != nil {
		return err
	}
	var msg log.UserMsg
	if err := safe.Decode(&msg); err != nil {
		return err
	}
	s.setTurnTask(msg.Text, earlier)
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
	return s.startScheduledTurn(work, nil)
}

func (s *Session) startScheduledTurn(work func(context.Context) error, run *log.ScheduleRun) error {
	s.mu.Lock()
	switch s.busyWith() {
	case holdClosed:
		s.mu.Unlock()
		return errors.New("core: session is closed")
	case holdTurn:
		s.mu.Unlock()
		return errors.New("core: a turn is already running")
	case holdAttachment:
		s.mu.Unlock()
		return errors.New("core: an attachment is in progress; send when it is done")
	case holdSwitch:
		s.mu.Unlock()
		return errors.New("core: the model is being switched; send when it is done")
	}
	limits, err := s.limits()
	if err != nil {
		s.mu.Unlock()
		return err
	}
	if run != nil {
		limits.Cost = scheduledCost(run.Cost)
	}
	ctx, turn, cancel := budget.Start(context.Background(), limits, s.confirmBudget)
	if limits.Cost.Hard > 0 && run == nil {
		turn.SetEarlierCost(s.knownSpend())
	}
	s.busy = true
	s.turnCancel = cancel
	s.turns.Add(1)
	s.mu.Unlock()
	// Every kind of work starts with no task, plan or marks of its own, and
	// remembers where in the log it began: an evaluation or a policy read
	// that fails says where it stopped from its own events, not from the
	// last chat turn's files and commands (turn() sets the task of a chat).
	s.setTurnTask("", nil)
	first := s.eventCount()

	go func() {
		var turnErr error
		defer s.turns.Done()
		defer func() {
			if limits.Cost.Hard > 0 {
				s.endSpend(turn)
			}
			end := turnOutcome(ctx, turnErr)
			if end.Outcome != "completed" && end.Outcome != "canceled" {
				end.Where = s.whereStopped(first)
				if kept := s.restoreKept(turnErr); kept != "" {
					end.Where += "; " + kept
				}
			}
			_, _ = s.Record(log.KindTurnEnd, end)
			s.flushOutput()
			cancel()
			s.endTurn()
			s.Emit(event.Event{Kind: event.Status, Payload: event.StatusInfo{State: "idle"}})
		}()
		if run != nil {
			run.SpendBefore = s.lifetimeSpend()
			if _, err := s.Record(log.KindScheduleRun, *run); err != nil {
				turnErr = err
				return
			}
		}
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

// endTurn ends the turn: a model chosen while it ran is applied first, while
// the session is still busy, so no turn can start on the model it replaces,
// and busy ends under the same lock that saw nothing queued (SetModel queues
// only while busy, and holds modelMu). The choice stays queued, as the state
// reports it, until it has been applied or has failed to be: applyQueued
// clears it together with the change it makes.
func (s *Session) endTurn() {
	s.modelMu.Lock()
	defer s.modelMu.Unlock()
	for {
		s.mu.Lock()
		queued := s.queued
		if queued == nil {
			s.busy = false
			s.turnCancel = nil
			s.turnTrust = false
			s.turnActor = ""
			s.mu.Unlock()
			return
		}
		s.mu.Unlock()
		s.applyQueued(*queued)
	}
}

// What keeps the session from starting a turn, an attachment or a change
// to it now (busyWith).
const (
	holdFree       = iota // nothing: it may start
	holdClosed            // the session is closing or closed
	holdTurn              // a turn is running
	holdAttachment        // an attachment is being added
	holdSwitch            // a model or judge change holds the idle session
)

// busyWith says what keeps a turn, an attachment or a change from starting
// now, so the places that start one (a turn, beginAttach, beginSwitch and
// holdIdle) share one test and a new kind of hold is added here and not in
// four places; each keeps its own wording of what to do instead. s.mu is
// held.
func (s *Session) busyWith() int {
	switch {
	case s.closed || s.closing:
		return holdClosed
	case s.busy:
		return holdTurn
	case s.attaching:
		return holdAttachment
	case s.switching:
		return holdSwitch
	}
	return holdFree
}

// holdSwitching holds the idle session for a change that cannot be written
// with s.mu held (Record emits, and Emit takes it): no turn or attachment
// starts until the function it returns is called, and Close waits for it.
// s.mu is held, and busyWith said holdFree.
func (s *Session) holdSwitching() (done func()) {
	s.switching = true
	s.turns.Add(1)
	return func() {
		s.mu.Lock()
		s.switching = false
		s.mu.Unlock()
		s.turns.Done()
	}
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

// Eval evaluates Scheme with user origin, unrecorded and without taking the
// session: for the tests and the exfiltration suite, which drive an idle
// session directly. A front-end uses StartEval.
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
		if s.terms != nil {
			s.terms.Close()
		}
		// Contained MCP servers stop before session.end, so their last
		// proxy reports are recorded.
		s.closeMCP()
		// Nothing runs in the worktree any more: it can go if that loses
		// nothing.
		s.closeWorktree()
		_, _ = s.logw.Append(log.KindSessionEnd, log.SessionEnd{Reason: "closed"})
		s.closeErr = s.logw.Close()
		// In sandbox mode (ADR 55) what the session's commands started in the
		// background goes on, with TMPDIR set to the scratch directory: a
		// server whose temporary files went there would fail once it was
		// gone, and the machine is thrown away with it anyway.
		if !s.opts.Sandbox {
			removeScratch(s.scratch)
		}
		close(s.out)
	})
	return s.closeErr
}

// evalScheme is the SchemeEval guard.WithSchemeEval uses to run a
// register-check predicate.
func (s *Session) evalScheme(ctx context.Context, proc scheme.Value, args []scheme.Value) (scheme.Value, error) {
	return s.interp.Apply(ctx, proc, args)
}
