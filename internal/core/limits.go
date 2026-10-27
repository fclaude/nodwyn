package core

import (
	"errors"

	"github.com/fclaude/nodwyn/internal/budget"
	"github.com/fclaude/nodwyn/internal/log"
)

// LimitsChoice is a task's own limits (ADR 49, amended 2026-10-27), and who
// set them, for the log. There is no configured default: a task has none
// unless a person set some, on the task or on the command line. Each is one
// number, and a turn stops at it without asking first.
type LimitsChoice struct {
	Limits budget.Task
	Actor  string
}

// TaskLimits are the task's limits, and who last set them (empty while it
// has never had any).
func (s *Session) TaskLimits() (budget.Task, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.taskLimits, s.limitsActor
}

// SetLimits replaces the task's limits, as actor, from its next turn: a turn
// running now keeps the limits it started with, since its deadline is
// already its context's. The change is recorded before it applies, and
// resuming the task keeps it.
func (s *Session) SetLimits(limits budget.Task, actor string) error {
	if s.opts.Managed != nil {
		return errors.New("managed: turn limits are operator-owned")
	}
	if err := limits.Validate(); err != nil {
		return err
	}
	s.limitsMu.Lock()
	defer s.limitsMu.Unlock()
	s.mu.Lock()
	closed, same := s.closed || s.closing, s.taskLimits == limits
	s.mu.Unlock()
	if closed {
		return errors.New("core: session is closed")
	}
	if same {
		return nil
	}
	if _, err := s.Record(log.KindSessionLimits, log.SessionLimits{Actor: actor, Task: limits}); err != nil {
		return err
	}
	s.mu.Lock()
	s.taskLimits, s.limitsActor = limits, actor
	s.mu.Unlock()
	return nil
}

// lastLimits is the latest session.limits record, if the log has one. One
// written before a task's limits were its own held only on or off, and
// reads as none.
func lastLimits(events []log.Event) (rec log.SessionLimits, found bool) {
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].Kind != log.KindSessionLimits {
			continue
		}
		if err := events[i].Decode(&rec); err != nil || rec.Validate() != nil {
			return log.SessionLimits{}, false
		}
		return rec, true
	}
	return log.SessionLimits{}, false
}
