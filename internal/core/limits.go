package core

import (
	"errors"

	"github.com/fclaude/nodwyn/internal/budget"
	"github.com/fclaude/nodwyn/internal/log"
)

// LimitsChoice says whether a task's turns stop at the turn limits (ADR 49),
// and who chose, for the log. Off, a turn has no limit on tool calls, tokens,
// actions or time: a task that runs while nobody watches may wait for an
// answer overnight. The spend limit, the timeouts and the progress guard are
// not turn limits and stay.
type LimitsChoice struct {
	On    bool
	Actor string
}

// LimitsOn reports whether the task's turns stop at the turn limits, and who
// last chose (empty for the default, on).
func (s *Session) LimitsOn() (bool, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return !s.limitsOff, s.limitsActor
}

// SetLimits turns the task's turn limits on or off, as actor, from its next
// turn: a turn running now keeps the limits it started with, since its
// deadline is already its context's. The choice is recorded before it
// applies, and resuming the task keeps it.
func (s *Session) SetLimits(on bool, actor string) error {
	s.limitsMu.Lock()
	defer s.limitsMu.Unlock()
	s.mu.Lock()
	closed, same := s.closed || s.closing, s.limitsOff == !on
	s.mu.Unlock()
	if closed {
		return errors.New("core: session is closed")
	}
	if same {
		return nil
	}
	if _, err := s.Record(log.KindSessionLimits, log.SessionLimits{Actor: actor, On: on}); err != nil {
		return err
	}
	s.mu.Lock()
	s.limitsOff, s.limitsActor = !on, actor
	s.mu.Unlock()
	return nil
}

// withoutTurnLimits is limits with the four turn limits off: what a turn of
// a task whose limits are off starts from. The spend limit is the task's,
// not the turn's, and stays.
func withoutTurnLimits(limits budget.Limits) budget.Limits {
	return budget.Limits{Cost: limits.Cost}
}

// lastLimits is the latest session.limits record, if the log has one.
func lastLimits(events []log.Event) (rec log.SessionLimits, found bool) {
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].Kind != log.KindSessionLimits {
			continue
		}
		if err := events[i].Decode(&rec); err != nil {
			return log.SessionLimits{}, false
		}
		return rec, true
	}
	return log.SessionLimits{}, false
}
