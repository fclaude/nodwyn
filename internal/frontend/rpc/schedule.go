package rpc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/fclaude/nodwyn/internal/core"
	"github.com/fclaude/nodwyn/internal/log"
	"github.com/fclaude/nodwyn/internal/schedule"
)

type scheduleSetParams struct {
	ID      string  `json:"id"`
	Message string  `json:"message,omitempty"`
	Repeat  string  `json:"repeat,omitempty"`
	At      string  `json:"at,omitempty"`
	Cost    float64 `json:"cost,omitempty"`
	Paused  bool    `json:"paused,omitempty"`
	Remove  bool    `json:"remove,omitempty"`
}

type schedulePauseParams struct {
	ID     string `json:"id"`
	Paused *bool  `json:"paused"`
}

type scheduleState struct {
	core.ScheduleInfo
	Next      []time.Time `json:"next"`
	Timezone  string      `json:"timezone"`
	Archived  bool        `json:"archived"`
	Scheduler bool        `json:"scheduler"`
}

type deferredSkip struct {
	sequence uint64
	record   log.ScheduleSkip
}

func (s *server) scheduleState(id string) (scheduleState, error) {
	info, err := s.store().Info(id)
	if err != nil {
		return scheduleState{}, err
	}
	state, err := s.store().Schedule(id)
	if err != nil {
		return scheduleState{}, err
	}
	result := scheduleState{ScheduleInfo: state, Next: []time.Time{}, Timezone: time.Local.String(), Archived: info.Archived, Scheduler: s.schedulerLock != nil}
	if d := state.Definition; d != nil && d.Definition.Validate() == nil && !d.Paused && !info.Archived {
		after := state.Cursor
		for i := 0; i < 5; i++ {
			next := d.Definition.Next(after, time.Local)
			if next.IsZero() {
				break
			}
			result.Next = append(result.Next, next)
			after = next
		}
	}
	return result, nil
}

func (s *server) readSchedule(req request, _ *peer) (any, *rpcError) {
	var p idParams
	if err := params(req.Params, &p, "id"); err != nil {
		return nil, err
	}
	if p.ID == "" {
		return nil, failure(-32602, "id is required")
	}
	state, err := s.scheduleState(p.ID)
	if err != nil {
		return nil, failure(-32000, err.Error())
	}
	return state, nil
}

func (s *server) writeSchedule(id string, rec log.SessionSchedule) error {
	if owned, ok := s.sessions[id]; ok {
		_, err := owned.session.Record(log.KindSessionSchedule, rec)
		return err
	}
	return s.store().SetSchedule(id, rec)
}

func (s *server) setSchedule(req request, client *peer) (any, *rpcError) {
	if strings.Contains(client.actor, " via ") {
		return nil, failure(-32602, "Schedules are created and changed on this computer, not from a paired device")
	}
	var p scheduleSetParams
	if err := params(req.Params, &p, "id", "message", "repeat", "at", "cost", "paused", "remove"); err != nil {
		return nil, err
	}
	if p.ID == "" {
		return nil, failure(-32602, "id is required")
	}
	definition := schedule.Definition{Message: p.Message, Repeat: p.Repeat, At: p.At, Cost: p.Cost, Paused: p.Paused}
	if !p.Remove {
		if err := definition.Validate(); err != nil {
			return nil, failure(-32602, err.Error())
		}
	}
	old, err := s.store().Schedule(p.ID)
	if err != nil {
		return nil, failure(-32000, err.Error())
	}
	after := time.Time{}
	if d := old.Definition; d != nil && d.At == p.At && d.Repeat == p.Repeat {
		after = old.Cursor
		if d.Paused && !p.Paused {
			after = time.Now()
		}
	}
	rec := log.SessionSchedule{Definition: definition, Actor: client.actor, Removed: p.Remove, After: after}
	if err := s.writeSchedule(p.ID, rec); err != nil {
		return nil, failure(-32000, err.Error())
	}
	delete(s.scheduleSkips, p.ID)
	return s.readSchedule(request{Params: mustID(p.ID)}, client)
}

func (s *server) pauseSchedule(req request, client *peer) (any, *rpcError) {
	var p schedulePauseParams
	if err := params(req.Params, &p, "id", "paused"); err != nil {
		return nil, err
	}
	if p.ID == "" || p.Paused == nil {
		return nil, failure(-32602, "id and paused are required")
	}
	if !*p.Paused && strings.Contains(client.actor, " via ") {
		return nil, failure(-32602, "Resuming a schedule is a change made on this computer, not from a paired device")
	}
	state, err := s.store().Schedule(p.ID)
	if err != nil {
		return nil, failure(-32000, err.Error())
	}
	if state.Definition == nil {
		return nil, failure(-32000, "This task has no schedule")
	}
	rec := *state.Definition
	rec.Actor = client.actor
	rec.After = state.Cursor
	if rec.Paused && !*p.Paused {
		rec.After = time.Now()
	}
	rec.Paused = *p.Paused
	if err := s.writeSchedule(p.ID, rec); err != nil {
		return nil, failure(-32000, err.Error())
	}
	delete(s.scheduleSkips, p.ID)
	return s.readSchedule(request{Params: mustID(p.ID)}, client)
}

func (s *server) runSchedule(req request, client *peer) (any, *rpcError) {
	var p idParams
	if err := params(req.Params, &p, "id"); err != nil {
		return nil, err
	}
	state, err := s.store().Schedule(p.ID)
	if err != nil {
		return nil, failure(-32000, err.Error())
	}
	if state.Definition == nil {
		return nil, failure(-32000, "This task has no schedule")
	}
	if err := state.Definition.Definition.Validate(); err != nil {
		return nil, failure(-32602, err.Error())
	}
	if err := s.ensureScheduler(); err != nil {
		return nil, failure(-32000, err.Error())
	}
	if s.schedulerLock == nil {
		return nil, failure(-32000, "The scheduler is owned by another daemon")
	}
	now := time.Now()
	if err := s.executeSchedule(p.ID, *state.Definition, log.ScheduleRun{Due: now, Started: now, Manual: true, Actor: client.actor, Cost: state.Definition.Cost}); err != nil {
		skip := log.ScheduleSkip{Due: now, Started: now, Reason: "Run now: " + err.Error()}
		if recordErr := s.recordSkip(p.ID, skip); recordErr != nil {
			return nil, failure(-32000, err.Error()+"; could not record the skip: "+recordErr.Error())
		}
		return nil, failure(-32000, err.Error())
	}
	return s.readSchedule(request{Params: mustID(p.ID)}, client)
}

// ensureScheduler takes the data-directory lease, held for this daemon's
// lifetime. A second daemon may serve clients but cannot dispatch schedules.
func (s *server) ensureScheduler() error {
	if s.schedulerLock != nil {
		return nil
	}
	if err := os.MkdirAll(s.options.Paths.DataDir, 0700); err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(s.options.Paths.DataDir, "scheduler.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	if err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil
		}
		return err
	}
	s.schedulerLock = f
	return nil
}

func (s *server) scheduler(ctx context.Context, done chan<- struct{}) {
	defer close(done)
	tick := time.NewTicker(30 * time.Second)
	defer tick.Stop()
	for {
		s.mu.Lock()
		if ctx.Err() == nil {
			if err := s.ensureScheduler(); err == nil && s.schedulerLock != nil {
				s.scheduleTick(time.Now())
			}
		}
		s.mu.Unlock()
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

// scheduleTick runs under the lifecycle lock, with an injected wall time in
// tests. A tick after sleep coalesces all missed civil occurrences once.
func (s *server) scheduleTick(now time.Time) {
	// A log that cannot be read is no schedule's: the others still run.
	infos, err := core.Readable(s.store().List())
	if err != nil {
		return
	}
	if s.scheduleSkips == nil {
		s.scheduleSkips = map[string]deferredSkip{}
	}
	for _, info := range infos {
		state, err := s.store().Schedule(info.ID)
		if err != nil || state.Definition == nil {
			continue
		}
		def := state.Definition
		if err := def.Definition.Validate(); err != nil {
			continue
		}
		if pending, ok := s.scheduleSkips[info.ID]; ok {
			if pending.sequence != state.Sequence {
				delete(s.scheduleSkips, info.ID)
			} else {
				if err := s.recordSkip(info.ID, pending.record); err != nil {
					_, last, missed := def.Definition.Due(pending.record.Through, now, time.Local)
					if !last.IsZero() {
						pending.record.Through = last
						pending.record.Missed += missed + 1
						s.scheduleSkips[info.ID] = pending
					}
					continue
				}
				if pending.record.Through.After(state.Cursor) {
					state.Cursor = pending.record.Through
				}
				delete(s.scheduleSkips, info.ID)
			}
		}
		if !def.Paused && !info.Archived {
			due, through, missed := def.Definition.Due(state.Cursor, now, time.Local)
			if !due.IsZero() {
				run := log.ScheduleRun{Due: due, Through: through, Started: now, Missed: missed, Cost: def.Cost, Actor: "scheduler"}
				if err := s.executeSchedule(info.ID, *def, run); err != nil {
					skip := log.ScheduleSkip{Due: due, Through: through, Started: now, Missed: missed, Reason: err.Error()}
					// Resume releases the lifecycle lock. A skip for the old
					// definition must not consume the replacement's times.
					current, readErr := s.store().Schedule(info.ID)
					if readErr != nil || current.Sequence != state.Sequence {
						skip.Through = time.Time{}
					}
					if s.recordSkip(info.ID, skip) != nil {
						s.scheduleSkips[info.ID] = deferredSkip{state.Sequence, skip}
					}
				}
			}
		}
		if owned, ok := s.sessions[info.ID]; ok && !owned.session.Busy() && !owned.driver.Active() && now.Sub(info.LastEvent) >= 15*time.Minute {
			pending, err := owned.session.PendingPrompts()
			if err == nil && len(pending) == 0 {
				owned.session.Close()
				<-owned.drained
				delete(s.sessions, info.ID)
			}
		}
	}
}

func (s *server) recordSkip(id string, rec log.ScheduleSkip) error {
	if owned, ok := s.sessions[id]; ok {
		_, err := owned.session.Record(log.KindScheduleSkip, rec)
		return err
	}
	return s.store().SkipSchedule(id, rec)
}

func (s *server) executeSchedule(id string, def log.SessionSchedule, run log.ScheduleRun) error {
	info, err := s.store().Info(id)
	if err != nil {
		return err
	}
	if info.Archived {
		return fmt.Errorf("task is archived")
	}
	if s.resuming[id] != nil {
		return fmt.Errorf("task is being resumed")
	}
	owned, open := s.sessions[id]
	if !open {
		if why := s.lockedKeyRefusal(core.ModelChoice{}); why != "" {
			return errors.New(why)
		}
		options := s.options
		options.Repo = ""
		options.NoConfirm = false
		options.AutoApprove = false
		options.Unattended = false
		options.JudgeApprove = false
		done := make(chan struct{})
		if s.resuming == nil {
			s.resuming = map[string]chan struct{}{}
		}
		s.resuming[id] = done
		s.mu.Unlock()
		session, resumeErr := core.Resume(options, id)
		s.mu.Lock()
		delete(s.resuming, id)
		close(done)
		if resumeErr != nil {
			return resumeErr
		}
		s.own(session)
		owned = s.sessions[id]
	}
	if owned.session.Busy() || owned.driver.Active() {
		return fmt.Errorf("task is busy")
	}
	pending, err := owned.session.PendingPrompts()
	if err != nil {
		return err
	}
	if len(pending) > 0 {
		return fmt.Errorf("task is waiting for an answer")
	}
	latest, err := s.store().Info(id)
	if err != nil {
		return err
	}
	if latest.Archived {
		return fmt.Errorf("task is archived")
	}
	current, err := s.store().Schedule(id)
	if err != nil {
		return err
	}
	if current.Definition == nil || *current.Definition != def {
		return fmt.Errorf("schedule changed while the task resumed")
	}
	return owned.session.SendScheduled(def.Message, run)
}

func mustID(id string) json.RawMessage { raw, _ := json.Marshal(idParams{ID: id}); return raw }

func (s *server) nextSchedule(req request, _ *peer) (any, *rpcError) {
	var d schedule.Definition
	if err := params(req.Params, &d, "message", "repeat", "at", "cost", "paused"); err != nil {
		return nil, err
	}
	if err := d.Validate(); err != nil {
		return nil, failure(-32602, err.Error())
	}
	result := scheduleState{Next: []time.Time{}, Timezone: time.Local.String()}
	after := time.Time{}
	for i := 0; i < 5; i++ {
		next := d.Next(after, time.Local)
		if next.IsZero() {
			break
		}
		result.Next = append(result.Next, next)
		after = next
	}
	return result, nil
}
