// Package run chains existing core turns under a finite, supervised lifetime.
// It owns continuation policy, never tool authority or the turn loop.
package run

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/fclaude/nibb/internal/core"
	"github.com/fclaude/nibb/internal/event"
	"github.com/fclaude/nibb/internal/guard"
	"github.com/fclaude/nibb/internal/log"
)

// CompleteLine, alone on the last line of an answer, is how the model says
// the whole task is done.
const CompleteLine = "NIBB_RUN_COMPLETE"

// State is a run as recorded in its session log, from which it resumes.
type State struct {
	Task     string    `json:"task"`
	Deadline time.Time `json:"deadline"`
	MaxTurns int       `json:"max_turns"`
	Turns    int       `json:"turns"` // turns started so far
	// Status is running, completed, limited (deadline or turn count),
	// stopped or error; Reason says why it is not running.
	Status string `json:"status"`
	Reason string `json:"reason,omitempty"`
}

// Validate reports whether a run can start or resume: a task, turns left and
// a deadline in the future.
func (s State) Validate() error {
	if !utf8.ValidString(s.Task) || strings.TrimSpace(s.Task) == "" || len(s.Task) > 64<<10 {
		return errors.New("run task must be valid UTF-8, nonempty and at most 64 KiB")
	}
	if s.MaxTurns < 1 || s.MaxTurns > 10000 || s.Turns < 0 || s.Turns >= s.MaxTurns {
		return errors.New("run requires remaining turns within a maximum of 10000")
	}
	if s.Deadline.IsZero() || !s.Deadline.After(time.Now()) {
		return errors.New("run deadline has expired or is missing")
	}
	return nil
}

// completion is how one turn ended.
type completion struct {
	end log.TurnEnd
}

// Driver runs one run: it sends a planned turn, waits for it to end, stops
// if the model reported completion or a limit was reached, and otherwise asks
// whether to continue. The continuation prompt grants no tool permission.
type Driver struct {
	session  *core.Session
	mu       sync.Mutex
	state    State
	current  completion
	waiting  bool
	finished chan completion
	done     chan struct{}
	cancel   context.CancelFunc
	ctx      context.Context
}

// New prepares a driver before its session event stream is attached. Start must
// be called exactly once after Observe can receive the session's events.
func New(session *core.Session, state State) (*Driver, error) {
	if err := state.Validate(); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithDeadline(context.Background(), state.Deadline)
	state.Status = "running"
	state.Reason = ""
	return &Driver{session: session, state: state, ctx: ctx, cancel: cancel, finished: make(chan completion, 1), done: make(chan struct{})}, nil
}

// Info returns the run's current state.
func (d *Driver) Info() State { d.mu.Lock(); defer d.mu.Unlock(); return d.state }

// Active reports whether the run is still going.
func (d *Driver) Active() bool {
	if d == nil {
		return false
	}
	return d.Info().Status == "running"
}

// Stop ends the run, aborting a turn in progress. Wait for it to finish.
func (d *Driver) Stop() {
	if d != nil {
		d.cancel()
	}
}

// Wait blocks until the run has finished and recorded its final state.
func (d *Driver) Wait() {
	if d != nil {
		<-d.done
	}
}

// Observe never records or blocks the session's drain. Only one turn can be in
// flight; its final idle transfers one completion to the waiting driver.
func (d *Driver) Observe(ev event.Event) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.waiting {
		return
	}
	switch ev.Kind {
	case string(log.KindTurnEnd):
		d.current.end, _ = ev.Payload.(log.TurnEnd)
	case event.Status:
		if p, ok := ev.Payload.(event.StatusInfo); ok && p.State == "idle" {
			d.waiting = false
			d.finished <- d.current
		}
	}
}

// Start runs the driver in the background on behalf of actor.
func (d *Driver) Start(actor string) { go d.execute(actor) }

func (d *Driver) execute(actor string) {
	defer close(d.done)
	defer d.cancel()
	state := d.Info()
	// The log's redacted state, not the submitted raw task, drives subsequent work.
	ev, err := d.session.Record(log.KindRunState, log.RunState{Task: state.Task, Deadline: state.Deadline, MaxTurns: state.MaxTurns, Turns: state.Turns, Status: "running", Actor: actor})
	if err != nil {
		d.finish("error", err.Error())
		return
	}
	var saved log.RunState
	if err := ev.Decode(&saved); err != nil {
		d.finish("error", err.Error())
		return
	}
	d.mu.Lock()
	d.state.Task = saved.Task
	d.mu.Unlock()
	for {
		state = d.Info()
		if d.ctx.Err() != nil {
			d.finish("stopped", d.ctx.Err().Error())
			return
		}
		if state.Turns >= state.MaxTurns {
			d.finish("limited", "maximum turn count reached")
			return
		}
		next := state.Turns + 1
		if _, err := d.session.Record(log.KindRunTurn, log.RunTurn{Number: next}); err != nil {
			d.finish("error", err.Error())
			return
		}
		d.mu.Lock()
		d.state.Turns = next
		d.current = completion{}
		d.waiting = true
		d.mu.Unlock()
		prompt := fmt.Sprintf("Run task:\n%s\n\nWork on one unfinished plan step, then report progress for review. If the entire task is complete, put %s alone on the final line. Do not claim completion while work remains. This is turn %d of at most %d.", state.Task, CompleteLine, next, state.MaxTurns)
		if err := d.session.SendPlanned(prompt); err != nil {
			d.mu.Lock()
			d.waiting = false
			d.mu.Unlock()
			d.finish("error", err.Error())
			return
		}
		var result completion
		select {
		case result = <-d.finished:
		case <-d.ctx.Done():
			d.session.Abort()
			result = <-d.finished
		}
		if d.ctx.Err() != nil {
			d.finish("stopped", d.ctx.Err().Error())
			return
		}
		// The turn's answer is the last reply the conversation still shows,
		// not the last one in the log: a sub-agent's reply, such as a
		// reviewer's verdict, is folded away and is not the model's word.
		answer := ""
		if reply, ok := d.session.FinalReply(); ok && len(reply.ToolCalls) == 0 && reply.DoneReason == "stop" {
			answer = reply.Content
		}
		if result.end.Outcome != "completed" && result.end.Outcome != "hard_budget" {
			d.finish("stopped", "turn ended: "+result.end.Outcome+": "+result.end.Error)
			return
		}
		if result.end.Outcome == "completed" && Complete(answer) {
			d.finish("completed", "model reported completion")
			return
		}
		if next >= state.MaxTurns {
			d.finish("limited", "maximum turn count reached")
			return
		}
		approval, err := d.session.ConfirmApproval(d.ctx, guard.Prompt{Prim: "run checkpoint", Reasons: []string{"Continue with another planned turn? This grants no tool permission."}, Detail: answer})
		_, recordErr := d.session.Record(log.KindRunCheckpoint, log.RunCheckpoint{Number: next, Allowed: approval.Allowed && err == nil, Actor: approval.Actor, PromptID: approval.PromptID})
		if recordErr != nil {
			d.finish("error", recordErr.Error())
			return
		}
		if err != nil || !approval.Allowed {
			d.finish("stopped", "run checkpoint stopped")
			return
		}
	}
}

// Complete reports whether answer ends with CompleteLine.
func Complete(answer string) bool {
	lines := strings.Split(strings.TrimRight(answer, "\r\n"), "\n")
	return len(lines) > 0 && strings.TrimSuffix(lines[len(lines)-1], "\r") == CompleteLine
}

// finish records the run's final state and releases Wait.
func (d *Driver) finish(status, reason string) {
	state := d.Info()
	_, err := d.session.Record(log.KindRunState, log.RunState{Task: state.Task, Deadline: state.Deadline, MaxTurns: state.MaxTurns, Turns: state.Turns, Status: status, Reason: reason})
	d.mu.Lock()
	d.state.Status = status
	d.state.Reason = reason
	d.mu.Unlock()
	if err != nil {
		d.mu.Lock()
		d.state.Status = "error"
		d.state.Reason = err.Error()
		d.mu.Unlock()
		d.session.Emit(event.Event{Kind: event.Error, Payload: event.ErrorInfo{Err: err.Error()}})
	}
	// The between-turn checkpoint is outside core.Busy; resolve frontend prompt
	// state explicitly even when there was no turn to produce an idle event.
	d.session.Emit(event.Event{Kind: event.Status, Payload: event.StatusInfo{State: "idle"}})
}

// Restore reconstructs a paused run. Interrupted turns require manual review;
// neither old logs without outcomes nor missing turn.end imply a safe boundary.
func Restore(events []log.Event) (State, error) {
	var state State
	found, boundary := false, true
	seenUser, uncertain := false, false
	for _, ev := range events {
		switch ev.Kind {
		case log.KindRunState:
			var p log.RunState
			if err := ev.Decode(&p); err != nil {
				return State{}, err
			}
			state = State{Task: p.Task, Deadline: p.Deadline, MaxTurns: p.MaxTurns, Turns: p.Turns, Status: p.Status, Reason: p.Reason}
			found = true
		case log.KindRunTurn:
			var p log.RunTurn
			if err := ev.Decode(&p); err != nil {
				return State{}, err
			}
			if !boundary {
				uncertain = true
			}
			state.Turns = p.Number
			boundary = false
			seenUser = false
		case log.KindUserMsg:
			if found && !boundary {
				if seenUser {
					uncertain = true
				}
				seenUser = true
			}
		case log.KindEvalReq:
			if found && !boundary {
				uncertain = true
			}
		case log.KindTurnEnd:
			var p log.TurnEnd
			if err := ev.Decode(&p); err != nil {
				return State{}, err
			}
			if found && !boundary {
				switch p.Outcome {
				case "completed", "hard_budget", "stopped", "canceled", "error":
				default:
					uncertain = true
				}
				if !seenUser {
					uncertain = true
				}
				boundary = true
			}
		}
	}
	if !found {
		return State{}, errors.New("session has no run")
	}
	if !boundary || uncertain {
		return state, errors.New("run has an interrupted turn without a recorded outcome; review it manually")
	}
	return state, nil
}
