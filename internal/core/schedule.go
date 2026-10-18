package core

import (
	"cmp"
	"context"
	"slices"
	"time"

	"github.com/fclaude/nodwyn/internal/budget"
	"github.com/fclaude/nodwyn/internal/log"
	"github.com/fclaude/nodwyn/internal/window"
)

// ScheduleInfo is folded from the log alongside its other derived views.
// Cursor is the last automatic due time handled, not the wall time of a run.
// History is the latest scheduled times, run or skipped, oldest first.
type ScheduleInfo struct {
	Definition *log.SessionSchedule `json:"definition,omitempty"`
	Sequence   uint64               `json:"sequence"`
	Cursor     time.Time            `json:"cursor"`
	LastRun    *log.ScheduleRun     `json:"last_run,omitempty"`
	LastSkip   *log.ScheduleSkip    `json:"last_skip,omitempty"`
	History    []ScheduleEntry      `json:"history,omitempty"`
}

// ScheduleEntry is one scheduled time as the schedule's history shows it: a
// run (Seq is its schedule.run) or a time skipped, and why. A run's Cost is
// what its requests cost as far as known, a person's follow-ups in it
// included, and Outcome how its own turn ended, empty until it has.
type ScheduleEntry struct {
	Seq     uint64    `json:"seq"`
	Due     time.Time `json:"due"`
	Started time.Time `json:"started"`
	Missed  int       `json:"missed,omitempty"`
	Manual  bool      `json:"manual,omitempty"`
	Skipped string    `json:"skipped,omitempty"`
	Outcome string    `json:"outcome,omitempty"`
	Cost    float64   `json:"cost,omitempty"`
}

// scheduleHistory is how many scheduled times the index keeps: enough to see
// how a schedule has gone lately, while a listing of every task stays small.
const scheduleHistory = 20

func (s *ScheduleInfo) add(e ScheduleEntry) {
	s.History = append(s.History, e)
	if len(s.History) > scheduleHistory {
		s.History = slices.Clone(s.History[len(s.History)-scheduleHistory:])
	}
}

// latestRun is the newest run in the history, skips passed over: a time
// skipped because a run was busy comes after that run began.
func (s *ScheduleInfo) latestRun() *ScheduleEntry {
	for i := len(s.History) - 1; i >= 0; i-- {
		if s.History[i].Skipped == "" {
			return &s.History[i]
		}
	}
	return nil
}

func (s *ScheduleInfo) observe(ev log.Event) {
	if cost, ok := knownCost(ev); ok {
		if run := s.latestRun(); run != nil {
			run.Cost += cost
		}
	}
	switch ev.Kind {
	case log.KindTurnEnd:
		var p log.TurnEnd
		if run := s.latestRun(); run != nil && run.Outcome == "" && ev.Decode(&p) == nil {
			run.Outcome = cmp.Or(p.Outcome, "completed")
		}
	case log.KindSessionSchedule:
		var p log.SessionSchedule
		if ev.Decode(&p) == nil {
			s.Sequence = ev.Seq
			s.Cursor = p.After
			s.Definition = &p
			if p.Removed {
				s.Definition = nil
			}
		}
	case log.KindScheduleRun:
		var p log.ScheduleRun
		if ev.Decode(&p) == nil {
			s.LastRun = &p
			if !p.Manual && p.Through.After(s.Cursor) {
				s.Cursor = p.Through
			}
			s.add(ScheduleEntry{Seq: ev.Seq, Due: p.Due, Started: p.Started, Missed: p.Missed, Manual: p.Manual})
		}
	case log.KindScheduleSkip:
		var p log.ScheduleSkip
		if ev.Decode(&p) == nil {
			s.LastSkip = &p
			s.add(ScheduleEntry{Seq: ev.Seq, Due: p.Due, Started: p.Started, Missed: p.Missed, Skipped: p.Reason})
			if p.Through.After(s.Cursor) {
				s.Cursor = p.Through
			}
		}
	}
}

// clone is a copy that shares no pointer with s, so the one kept in memory
// is never changed by a reader.
func (s ScheduleInfo) clone() ScheduleInfo {
	if s.Definition != nil {
		d := *s.Definition
		s.Definition = &d
	}
	if s.LastRun != nil {
		r := *s.LastRun
		s.LastRun = &r
	}
	if s.LastSkip != nil {
		k := *s.LastSkip
		s.LastSkip = &k
	}
	s.History = slices.Clone(s.History)
	return s
}

// Schedule is a session's schedule as its log has it, answered from memory
// while the log is unchanged, as Info is.
func (s Store) Schedule(id string) (ScheduleInfo, error) {
	_, schedule, err := s.infoAndSchedule(id)
	return schedule, err
}

func (s Store) SetSchedule(id string, p log.SessionSchedule) error {
	if !p.Removed {
		if err := p.Definition.Validate(); err != nil {
			return err
		}
	}
	return s.record(id, log.KindSessionSchedule, p)
}

func (s Store) SkipSchedule(id string, p log.ScheduleSkip) error {
	return s.record(id, log.KindScheduleSkip, p)
}

func (s *Session) noteSchedule(ev log.Event) {
	switch ev.Kind {
	case log.KindSessionSchedule:
		var p log.SessionSchedule
		if ev.Decode(&p) == nil {
			if p.Removed {
				s.schedule.Store(nil)
			} else {
				s.schedule.Store(&p)
			}
		}
	case log.KindScheduleRun:
		var p log.ScheduleRun
		if ev.Decode(&p) == nil {
			s.scheduledRun.Store(&p)
			s.mu.Lock()
			s.costSoft = 0
			s.spendNoted = false
			s.judgeSpendNoted = false
			s.mu.Unlock()
		}
	case log.KindContextHandover:
		var p log.ContextHandover
		if ev.Decode(&p) == nil {
			s.handover.Store(&p)
		}
	}
}

func scheduledCost(cost float64) budget.Limit {
	n := int64(cost * 1e6)
	return budget.Limit{Soft: n, Hard: n}
}

// RunSpend reports the current scheduled run's cost, including its human
// follow-ups. Its limit was fixed by the run marker, not by a later edit.
func (s *Session) RunSpend() (float64, bool) {
	if s.scheduledRun.Load() == nil {
		return 0, false
	}
	return s.knownSpend(), true
}

// SendScheduled reserves a normal chat turn. The host, never a primitive,
// records its marker, discards Don't ask and applies a prepared handover.
func (s *Session) SendScheduled(text string, run log.ScheduleRun) error {
	if err := s.modelAvailable(); err != nil {
		return err
	}
	if err := s.scheduledApproval(); err != nil {
		return err
	}
	return s.startScheduledTurn(func(ctx context.Context) error {
		if s.worktree != nil {
			moved, err := catchUp(*s.worktree)
			if err != nil {
				_, _ = s.Record(log.KindNote, log.Note{Text: "The worktree could not be brought up to " + s.worktree.Base + ", so this run starts where the last one left it: " + err.Error()})
			} else if moved != "" {
				if _, err := s.Record(log.KindNote, log.Note{Text: "The worktree held no work, so it was brought up to " + s.worktree.Base + " at " + moved[:min(7, len(moved))] + ": this run starts from there."}); err != nil {
					return err
				}
			}
		}
		// A note stands for the conversation only if it covers all of it: a
		// run or a follow-up written after the note is in no note, and
		// narrowing to the note would drop it from the model's memory for
		// good. Until a later note covers it, the conversation stays whole.
		if note := s.handover.Load(); note != nil && note.Text != "" && !s.conversationAfter(note.Through) {
			if _, err := s.Record(log.KindContextNarrow, log.ContextNarrow{System: s.loop.WindowContext().System, Agent: "scheduled handover", Lasting: true}); err != nil {
				return err
			}
			if _, err := s.Record(log.KindHarnessMsg, log.HarnessMsg{Text: note.Text}); err != nil {
				return err
			}
		}
		return s.turn(ctx, text, false, true)
	}, &run)
}

// isConversation says whether an event is part of what the model was told
// or said, as against the host's own records.
func isConversation(kind log.Kind) bool {
	switch kind {
	case log.KindUserMsg, log.KindHarnessMsg, log.KindLLMResp, log.KindToolResult:
		return true
	}
	return false
}

// conversationAfter says whether the conversation has anything after seq.
func (s *Session) conversationAfter(seq uint64) bool {
	for _, ev := range s.EventsSoFar() {
		if ev.Seq > seq && isConversation(ev.Kind) {
			return true
		}
	}
	return false
}

// prepareHandover runs before the turn releases its interpreter, while its
// cached prefix is still warm. It never replaces the current conversation.
func (s *Session) prepareHandover(ctx context.Context) {
	def := s.schedule.Load()
	if def == nil || def.Paused {
		return
	}
	pending, err := s.PendingPrompts()
	if err != nil || len(pending) > 0 {
		return
	}
	history := s.EventsSoFar()
	var after uint64
	if note := s.handover.Load(); note != nil {
		after = note.Through
	}
	size := 0
	for _, ev := range history {
		if ev.Seq > after && isConversation(ev.Kind) {
			size += window.Estimate(string(ev.Payload))
		}
	}
	high := s.loop.WindowContext().HighWater
	if high <= 0 || size <= high/4 {
		return
	}
	from := history[len(history)-1].Seq + 1
	text, err := s.loop.Handover(ctx, "Keep what the next scheduled run needs for this request: "+def.Message)
	if err != nil {
		_, _ = s.Record(log.KindNote, log.Note{Text: "Could not prepare the next scheduled handover: " + err.Error()})
		return
	}
	if text == "" {
		return
	}
	history = s.EventsSoFar()
	through := history[len(history)-1].Seq
	_, _ = s.Record(log.KindContextHandover, log.ContextHandover{Text: text, From: from, Through: through})
}
