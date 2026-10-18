package rpc

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"time"

	"github.com/fclaude/nodwyn/internal/core"
	"github.com/fclaude/nodwyn/internal/log"
	"github.com/fclaude/nodwyn/internal/schedule"
)

type scheduleSetParams struct {
	ID      string         `json:"id"`
	Message string         `json:"message,omitempty"`
	Repeat  string         `json:"repeat,omitempty"`
	At      string         `json:"at,omitempty"`
	Rule    *schedule.Rule `json:"rule,omitempty"`
	Cost    float64        `json:"cost,omitempty"`
	Paused  bool           `json:"paused,omitempty"`
	Remove  bool           `json:"remove,omitempty"`
}

type schedulePauseParams struct {
	ID     string `json:"id"`
	Paused *bool  `json:"paused"`
}

// scheduleState is a schedule as clients show it. The daemon says it in
// words, so that the console and the TUI print the same: the rule in a
// sentence, each next time as a label, and each recent time with what came of
// it. History, newest first, stands in for the folded one, oldest first.
type scheduleState struct {
	core.ScheduleInfo
	Rule      *schedule.Rule    `json:"rule,omitempty"`
	Sentence  string            `json:"sentence,omitempty"`
	Next      []time.Time       `json:"next"`
	NextWords []string          `json:"next_words"`
	History   []scheduleHistory `json:"history,omitempty"`
	Timezone  string            `json:"timezone"`
	Archived  bool              `json:"archived"`
	Scheduler bool              `json:"scheduler"`
}

type scheduleHistory struct {
	core.ScheduleEntry
	When string `json:"when"`
	What string `json:"what"`
}

type deferredSkip struct {
	sequence uint64
	record   log.ScheduleSkip
}

// zoneName names the zone the daemon's civil times are in. Go calls the
// computer's own zone "Local"; its name is in TZ or where /etc/localtime
// points.
func zoneName() string {
	if tz, set := os.LookupEnv("TZ"); set {
		if tz = strings.TrimPrefix(tz, ":"); tz == "" {
			return "UTC"
		} else if !filepath.IsAbs(tz) {
			return tz
		}
	}
	if target, err := os.Readlink("/etc/localtime"); err == nil {
		if _, name, found := strings.Cut(target, "zoneinfo/"); found {
			return name
		}
	}
	name, _ := time.Now().Zone()
	return name
}

// preview is what a client shows of a definition before or after it is
// saved: its rule, the sentence, and the next five times after `after`.
func preview(d schedule.Definition, after, now time.Time) scheduleState {
	rule := d.Timetable()
	state := scheduleState{Rule: &rule, Sentence: d.Sentence(now), Next: []time.Time{}, NextWords: []string{}, Timezone: zoneName()}
	for i := 0; i < 5; i++ {
		next := d.Next(after, time.Local)
		if next.IsZero() {
			break
		}
		state.Next = append(state.Next, next)
		state.NextWords = append(state.NextWords, schedule.Label(next, now))
		after = next
	}
	return state
}

var outcomes = map[string]string{
	"completed":   "Finished",
	"canceled":    "Stopped",
	"stopped":     "Stopped at a turn limit",
	"hard_budget": "Stopped at a hard limit",
	"spend_limit": "Stopped at the spend limit",
	"error":       "Failed",
}

// historyWords says what came of one scheduled time. The newest run with no
// outcome yet is running when its task is busy; an older one never finished.
func historyWords(e core.ScheduleEntry, newest, busy bool) string {
	what := ""
	switch {
	case e.Skipped != "":
		what = "Skipped: " + e.Skipped
	case e.Outcome == "" && newest && busy:
		what = "Running"
	case e.Outcome == "":
		what = "Did not finish"
	default:
		what = cmp.Or(outcomes[e.Outcome], e.Outcome)
	}
	switch {
	case e.Manual:
		what += " (Run now)"
	case e.Missed == 1:
		what += " (late, standing for 1 missed time)"
	case e.Missed > 1:
		what += fmt.Sprintf(" (late, standing for %d missed times)", e.Missed)
	}
	return what
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
	now := time.Now()
	result := scheduleState{ScheduleInfo: state, Next: []time.Time{}, NextWords: []string{}, Timezone: zoneName(), Archived: info.Archived, Scheduler: s.schedulerLock != nil}
	if d := state.Definition; d != nil && d.Definition.Validate() == nil {
		shown := preview(d.Definition, state.Cursor, now)
		result.Rule, result.Sentence = shown.Rule, shown.Sentence
		if !d.Paused && !info.Archived {
			result.Next, result.NextWords = shown.Next, shown.NextWords
		}
	}
	owned, open := s.sessions[id]
	busy := open && (owned.session.Busy() || owned.driver.Active())
	newest := true
	for i := len(state.History) - 1; i >= 0; i-- {
		e := state.History[i]
		result.History = append(result.History, scheduleHistory{e, schedule.Label(e.Started.In(time.Local), now), historyWords(e, newest, busy)})
		if e.Skipped == "" {
			newest = false
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

// writeSchedule records a schedule with its timetable in words, as the daemon
// says it now.
func (s *server) writeSchedule(id string, rec log.SessionSchedule) error {
	if !rec.Removed {
		rec.Words = rec.Definition.Sentence(time.Now())
	}
	if owned, ok := s.sessions[id]; ok {
		_, err := owned.session.Record(log.KindSessionSchedule, rec)
		return err
	}
	return s.store().SetSchedule(id, rec)
}

func (s *server) setSchedule(req request, client *peer) (any, *rpcError) {
	if why := client.refuseDevice("Schedules are created and changed on this computer, not from a paired device"); why != nil {
		return nil, why
	}
	var p scheduleSetParams
	if err := params(req.Params, &p, "id", "message", "repeat", "at", "rule", "cost", "paused", "remove"); err != nil {
		return nil, err
	}
	if p.ID == "" {
		return nil, failure(-32602, "id is required")
	}
	now := time.Now()
	definition := schedule.Definition{Message: p.Message, Repeat: p.Repeat, At: p.At, Rule: p.Rule, Cost: p.Cost, Paused: p.Paused}
	if !p.Remove {
		if err := settle(&definition, now); err != nil {
			return nil, failure(-32602, err.Error())
		}
	}
	old, err := s.store().Schedule(p.ID)
	if err != nil {
		return nil, failure(-32000, err.Error())
	}
	// A change that keeps the timing keeps the schedule's progress. A new
	// timing starts after now: under it, the times before now were never
	// due, and running one late for each would be a run nobody asked for.
	after := time.Time{}
	if d := old.Definition; d != nil {
		after = now
		if reflect.DeepEqual(d.Timetable(), definition.Timetable()) && !(d.Paused && !p.Paused) {
			after = old.Cursor
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
	if !*p.Paused {
		if why := client.refuseDevice("Resuming a schedule is a change made on this computer, not from a paired device"); why != nil {
			return nil, why
		}
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
	if err := s.executeSchedule(p.ID, *state.Definition, state.Sequence, log.ScheduleRun{Due: now, Started: now, Manual: true, Actor: client.actor, Cost: state.Definition.Cost}); err != nil {
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
// tests; it releases the lock around the slow parts, as executeSchedule and
// closeIdle do. A tick after sleep coalesces all missed civil occurrences once.
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
				if err := s.executeSchedule(info.ID, *def, state.Sequence, run); err != nil {
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
				s.closeIdle(info.ID, owned)
			}
		}
	}
}

// closeIdle closes an idle scheduled task outside the lifecycle lock, which
// it holds when it is called and holds again when it returns. Closing stops
// the task's containers (up to 30 seconds each) and asks git about its
// worktree (up to two minutes), and every request to the daemon takes the
// lock. The task leaves s.sessions first and is marked as resuming, so an
// open or a resume of it meanwhile waits for the close instead of starting a
// second session on the same log, as a resume already does for another.
func (s *server) closeIdle(id string, owned ownedSession) {
	done := make(chan struct{})
	if s.resuming == nil {
		s.resuming = map[string]chan struct{}{}
	}
	s.resuming[id] = done
	delete(s.sessions, id)
	s.mu.Unlock()
	owned.session.Close()
	<-owned.drained
	s.mu.Lock()
	delete(s.resuming, id)
	close(done)
}

func (s *server) recordSkip(id string, rec log.ScheduleSkip) error {
	if owned, ok := s.sessions[id]; ok {
		_, err := owned.session.Record(log.KindScheduleSkip, rec)
		return err
	}
	return s.store().SkipSchedule(id, rec)
}

// executeSchedule sends def's message as a run, unless the schedule changed
// since sequence was read: resuming the task releases the lifecycle lock.
func (s *server) executeSchedule(id string, def log.SessionSchedule, sequence uint64, run log.ScheduleRun) error {
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
		var session *core.Session
		var resumeErr error
		s.slow(id, func() { session, resumeErr = core.Resume(options, id) })
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
	if current.Definition == nil || current.Sequence != sequence {
		return fmt.Errorf("schedule changed while the task resumed")
	}
	return owned.session.SendScheduled(def.Message, run)
}

func mustID(id string) json.RawMessage { raw, _ := json.Marshal(idParams{ID: id}); return raw }

// settle fills in what a definition leaves to the daemon, the first minute
// of a rule (now), and checks it. A rule is kept in its normal form.
func settle(d *schedule.Definition, now time.Time) error {
	if d.Rule != nil {
		rule := *d.Rule
		if rule.Once == "" && rule.From == "" {
			rule.From = now.Format(schedule.Layout)
		}
		d.Rule = &rule
	}
	if err := d.Validate(); err != nil {
		return err
	}
	if d.Rule != nil {
		rule := d.Rule.Normal()
		d.Rule = &rule
	}
	return nil
}

func (s *server) nextSchedule(req request, _ *peer) (any, *rpcError) {
	var d schedule.Definition
	if err := params(req.Params, &d, "message", "repeat", "at", "rule", "cost", "paused"); err != nil {
		return nil, err
	}
	now := time.Now()
	if err := settle(&d, now); err != nil {
		return nil, failure(-32602, err.Error())
	}
	return preview(d, time.Time{}, now), nil
}
