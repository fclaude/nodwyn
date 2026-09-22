// Package core is the UI-agnostic façade over the harness (ADR 10). A
// Session owns the interpreter, the guard, the log and the loop; a
// front-end drives it with Send/Answer/Abort and watches Events. Nothing in
// here knows what a terminal is.
package core

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/fclaude/nibb/internal/binding"
	"github.com/fclaude/nibb/internal/budget"
	"github.com/fclaude/nibb/internal/event"
	"github.com/fclaude/nibb/internal/guard"
	"github.com/fclaude/nibb/internal/keychain"
	"github.com/fclaude/nibb/internal/llm"
	"github.com/fclaude/nibb/internal/log"
	"github.com/fclaude/nibb/internal/loop"
	"github.com/fclaude/nibb/internal/plan"
	"github.com/fclaude/nibb/internal/scheme"
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
	closed     bool

	prompts  map[string]pendingConfirmation
	promptN  int
	promptMu sync.Mutex

	allow  *guard.CommandAllow
	writes *guard.WriteAllow
	// turnTrust: writes are approved without a prompt until the turn ends
	// ("t" at a write prompt). Reset when the turn goes idle.
	turnTrust bool

	// P3 egress.
	keychain *keychain.Store
	bindings binding.Table

	// P3 credential prompts: pending masked-secret prompts.
	secrets   map[string]chan []byte
	secretsMu sync.Mutex
}

// ID is the session id (also the log file's base name).
func (s *Session) ID() string { return s.id }

// Repo is the pinned repository root.
func (s *Session) Repo() string { return s.opts.Repo }

// Tools lists the tools offered to the model.
func (s *Session) Tools() []llm.Tool { return s.loop.Tools() }

// Events is the stream a front-end reads. It is closed by Close.
func (s *Session) Events() <-chan event.Event { return s.out }

// Send starts a turn for the user's text. It returns immediately; the turn
// runs in the background and reports through Events, ending with a status
// event whose State is "idle".
func (s *Session) Send(text string) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return errors.New("core: session is closed")
	}
	if s.busy {
		s.mu.Unlock()
		return errors.New("core: a turn is already running")
	}
	limits, err := s.opts.Config.Budget.Limits(s.opts.Config.Loop.MaxToolCalls)
	if err != nil {
		s.mu.Unlock()
		return err
	}
	ctx, _, cancel := budget.Start(context.Background(), limits, s.confirmBudget)
	s.busy = true
	s.turnCancel = cancel
	s.mu.Unlock()

	go func() {
		defer func() {
			_, _ = s.Record(log.KindTurnEnd, log.Note{Text: "turn ended"})
			s.flushOutput()
			cancel()
			s.mu.Lock()
			s.busy = false
			s.turnCancel = nil
			s.turnTrust = false
			s.mu.Unlock()
			s.Emit(event.Event{Kind: event.Status, Payload: event.StatusInfo{State: "idle"}})
		}()
		// P5 plan gate: noul → plan → Seam A score → revise ≤3.
		// The gate runs under the same guard/binding/redaction policy as a
		// model tool call (plan §Phase 5). On Jev outage it degrades to a
		// plain plan and the loop proceeds.
		safe, err := s.Record(log.KindUserMsg, log.UserMsg{Text: text})
		if err != nil {
			s.Emit(event.Event{Kind: event.Error, Payload: event.ErrorInfo{Err: err.Error()}})
			return
		}
		var msg log.UserMsg
		if err := safe.Decode(&msg); err != nil {
			return
		}
		text = msg.Text
		gres := plan.Run(ctx, s, text)
		if gres.Plan != "" {
			s.Emit(event.Event{Kind: event.Status, Payload: event.StatusInfo{State: "plan", Detail: fmt.Sprintf("plan score %.2f (%d revises)", gres.Score, gres.Revises)}})
		}
		if err := s.loop.Turn(ctx); err != nil {
			if ctx.Err() != nil {
				err = context.Cause(ctx)
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
// running tool is killed, a pending confirm is answered "no".
func (s *Session) Abort() {
	s.mu.Lock()
	cancel := s.turnCancel
	s.mu.Unlock()
	if cancel != nil {
		cancel()
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
	s.Abort()
	// Wait for the turn goroutine to finish so it does not emit after close.
	for s.Busy() {
		time.Sleep(5 * time.Millisecond)
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	s.mu.Unlock()
	_, _ = s.logw.Append(log.KindSessionEnd, log.SessionEnd{Reason: "closed"})
	err := s.logw.Close()
	close(s.out)
	return err
}

// evalScheme is the SchemeEval guard.WithSchemeEval uses to run a
// register-check predicate.
func (s *Session) evalScheme(ctx context.Context, proc scheme.Value, args []scheme.Value) (scheme.Value, error) {
	return s.interp.Apply(ctx, proc, args)
}
