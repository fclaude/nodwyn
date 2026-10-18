package rpc

import (
	"encoding/json"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/fclaude/nodwyn/internal/core"
	"github.com/fclaude/nodwyn/internal/keychain"
	"github.com/fclaude/nodwyn/internal/llm"
	"github.com/fclaude/nodwyn/internal/log"
	"github.com/fclaude/nodwyn/internal/schedule"
	"github.com/fclaude/nodwyn/test/fakes/must"
	fake "github.com/fclaude/nodwyn/test/fakes/ollama"
)

func schedulerServer(t *testing.T, o core.Options) *server {
	t.Helper()
	s := &server{options: o, sessions: map[string]ownedSession{}}
	if err := s.ensureScheduler(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.close(); err != nil {
			t.Error(err)
		}
		if s.schedulerLock != nil {
			s.schedulerLock.Close()
		}
	})
	return s
}
func tickSchedule(s *server, now time.Time) { s.mu.Lock(); defer s.mu.Unlock(); s.scheduleTick(now) }
func scheduleIdle(t *testing.T, s *server, id string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		s.mu.Lock()
		owned := s.sessions[id]
		s.mu.Unlock()
		if owned.session != nil && !owned.session.Busy() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("scheduled turn did not finish")
}
func scheduledTask(t *testing.T, s *server, at time.Time) string {
	t.Helper()
	o := s.options
	o.Repo = t.TempDir()
	session, err := core.New(o)
	if err != nil {
		t.Fatal(err)
	}
	id := session.ID()
	if err = session.Close(); err != nil {
		t.Fatal(err)
	}
	def := log.SessionSchedule{Definition: schedule.Definition{Message: "check", Repeat: "hourly", At: at.Format(schedule.Layout), Cost: 1}}
	if err = s.store().SetSchedule(id, def); err != nil {
		t.Fatal(err)
	}
	return id
}
func TestSchedulerCoalescesPersistsAndClosesIdle(t *testing.T) {
	fakeModel := fake.NewT(t, fake.Turn{Content: "done"})
	defer fakeModel.Close()
	o := options(t)
	o.Provider = llm.NewOllama(fakeModel.URL)
	s := schedulerServer(t, o)
	now := time.Now().Truncate(time.Minute)
	id := scheduledTask(t, s, now.Add(-3*time.Hour))
	tickSchedule(s, now)
	scheduleIdle(t, s, id)
	state, err := s.store().Schedule(id)
	if err != nil {
		t.Fatal(err)
	}
	if state.LastRun == nil || state.LastRun.Missed != 3 || !state.Cursor.Equal(now) {
		t.Fatalf("%+v", state)
	}
	tickSchedule(s, now)
	scheduleIdle(t, s, id)
	totals, err := s.store().Totals(id)
	if err != nil {
		t.Fatal(err)
	}
	if totals.Runs != 1 {
		t.Fatal(totals.Runs)
	}
	// Pausing suppresses later due times but leaves the idle resource cleanup.
	def := *state.Definition
	def.Paused = true
	def.After = state.Cursor
	if err = s.writeSchedule(id, def); err != nil {
		t.Fatal(err)
	}
	tickSchedule(s, now.Add(16*time.Minute))
	if _, open := s.sessions[id]; open {
		t.Fatal("idle session stayed open")
	}
	second := schedulerServer(t, o)
	if second.schedulerLock != nil {
		t.Fatal("second daemon acquired the scheduler lease")
	}
	if err = syscall.Flock(int(s.schedulerLock.Fd()), syscall.LOCK_UN); err != nil {
		t.Fatal(err)
	}
	s.schedulerLock.Close()
	s.schedulerLock = nil
	if err = second.ensureScheduler(); err != nil {
		t.Fatal(err)
	}
	if second.schedulerLock == nil {
		t.Fatal("lease did not pass to second daemon")
	}
	state, err = second.store().Schedule(id)
	if err != nil || state.LastRun == nil || !state.Definition.Paused {
		t.Fatalf("restart: %+v %v", state, err)
	}
}

// A time due while the keychain holds the model's key locked waits for it:
// nothing is recorded, the schedule says it waits, and the first tick after
// the unlock runs it once, standing for every time it held. It used to be
// recorded as skipped and lost, so a reboot at night lost every run until
// someone unlocked.
func TestALockedKeychainHoldsAScheduledTime(t *testing.T) {
	fakeModel := fake.NewT(t, fake.Turn{Content: "done"})
	defer fakeModel.Close()
	o := options(t)
	o.Provider = llm.NewOllama(fakeModel.URL)
	o.Config.Control.Credential = "model-key"
	must.MkdirAll(t, o.Paths.ConfigDir, 0o700)
	store, err := keychain.Create(o.Paths.Keychain, keychain.FastKDF, []byte("correct horse"))
	if err != nil {
		t.Fatal(err)
	}
	s := schedulerServer(t, o)
	now := time.Now().Truncate(time.Minute)
	id := scheduledTask(t, s, now.Add(-3*time.Hour))
	tickSchedule(s, now)
	tickSchedule(s, now.Add(time.Minute))
	state, err := s.store().Schedule(id)
	if err != nil {
		t.Fatal(err)
	}
	if state.LastRun != nil || state.LastSkip != nil || !state.Cursor.IsZero() {
		t.Fatalf("a locked keychain recorded the time: %+v", state)
	}
	// Read as a client reads it, so this compiles against a daemon without
	// the field and fails there on what it records.
	says := func() string {
		s.mu.Lock()
		defer s.mu.Unlock()
		view, err := s.scheduleState(id)
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(view)
		var fields struct {
			Held string `json:"held"`
		}
		json.Unmarshal(raw, &fields)
		return fields.Held
	}
	if held := says(); !strings.Contains(held, "Waiting for the keychain") {
		t.Fatalf("the schedule does not say it waits: %q", held)
	}
	s.mu.Lock()
	s.options.Keychain = store
	s.mu.Unlock()
	tickSchedule(s, now.Add(2*time.Minute))
	scheduleIdle(t, s, id)
	state, err = s.store().Schedule(id)
	if err != nil {
		t.Fatal(err)
	}
	if state.LastRun == nil || state.LastRun.Missed != 3 || state.LastSkip != nil {
		t.Fatalf("after the unlock: %+v", state)
	}
	if held := says(); held != "" {
		t.Fatalf("an unlocked keychain still holds the schedule: %q", held)
	}
}

func TestSchedulerSkipsBusyAndExternallyOwnedTasks(t *testing.T) {
	held := &heldProvider{started: make(chan struct{}), release: make(chan struct{})}
	o := options(t)
	o.Provider = held
	s := schedulerServer(t, o)
	now := time.Now().Truncate(time.Minute)
	id := scheduledTask(t, s, now)
	tickSchedule(s, now)
	waitStarted(t, held)
	tickSchedule(s, now.Add(time.Hour))
	state, err := s.store().Schedule(id)
	if err != nil {
		t.Fatal(err)
	}
	if state.LastSkip == nil || !strings.Contains(state.LastSkip.Reason, "busy") || !state.Cursor.Equal(now.Add(time.Hour)) {
		t.Fatalf("%+v", state)
	}
	// The history says it in words, newest first: the time skipped, then the
	// run that made the task busy, still running.
	s.mu.Lock()
	shown, err := s.scheduleState(id)
	s.mu.Unlock()
	if err != nil || len(shown.History) != 2 || shown.History[0].What != "Skipped: task is busy" || shown.History[1].What != "Running" || shown.History[1].When == "" {
		t.Fatalf("history while running: %+v %v", shown.History, err)
	}
	close(held.release)
	scheduleIdle(t, s, id)
	s.mu.Lock()
	shown, err = s.scheduleState(id)
	s.mu.Unlock()
	if err != nil || shown.History[1].What != "Finished" {
		t.Fatalf("history once finished: %+v %v", shown.History, err)
	}
	// Another process owns this log. Its skip is appended only once the
	// writer releases it; the scheduler must never steal that ownership.
	external := scheduledTask(t, s, now)
	other, err := core.Resume(o, external)
	if err != nil {
		t.Fatal(err)
	}
	tickSchedule(s, now)
	if len(s.scheduleSkips) != 1 {
		t.Fatalf("deferred skips: %+v", s.scheduleSkips)
	}
	if err = other.Close(); err != nil {
		t.Fatal(err)
	}
	tickSchedule(s, now)
	state, err = s.store().Schedule(external)
	if err != nil {
		t.Fatal(err)
	}
	if state.LastRun != nil || state.LastSkip == nil || len(s.scheduleSkips) != 0 {
		t.Fatalf("external lock: %+v", state)
	}
}

func TestScheduleControlsAndDeviceAuthority(t *testing.T) {
	srv := fake.NewT(t, fake.Turn{Content: "manual"})
	defer srv.Close()
	o := options(t)
	o.Provider = llm.NewOllama(srv.URL)
	path, _ := start(t, o)
	c := connect(t, path)
	c.hello()
	id := opened(t, c, t.TempDir())
	definition := map[string]any{"id": id, "message": "check", "repeat": "daily", "at": time.Now().Add(48 * time.Hour).Format(schedule.Layout), "cost": 2}
	var state scheduleState
	into(t, c.ok("schedule.set", definition), &state)
	if state.Definition == nil || len(state.Next) != 5 {
		t.Fatalf("%+v", state)
	}
	phone := connect(t, path)
	phone.ok("hello", map[string]any{"protocol": 1, "via": "web:phone"})
	if r := phone.call("schedule.set", definition); r.Error == nil {
		t.Fatal("device changed a schedule")
	}
	phone.ok("schedule.pause", map[string]any{"id": id, "paused": true})
	if r := phone.call("schedule.pause", map[string]any{"id": id, "paused": false}); r.Error == nil {
		t.Fatal("device resumed a schedule")
	}
	if r := c.call("schedule.pause", map[string]any{"id": id}); r.Error == nil {
		t.Fatal("missing pause value accepted")
	}
	phone.ok("schedule.run", map[string]any{"id": id})
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var details sessionDetails
		into(t, c.ok("session.info", map[string]any{"id": id}), &details)
		if !details.Busy {
			break
		}
		time.Sleep(time.Millisecond)
	}
	c.ok("session.close", map[string]any{"id": id})
	c.ok("session.archive", map[string]any{"id": id, "archived": true})
	c.ok("session.archive", map[string]any{"id": id, "archived": false})
	into(t, c.ok("schedule.read", map[string]any{"id": id}), &state)
	if !state.Definition.Paused || state.LastRun == nil || !state.LastRun.Manual || !state.Cursor.IsZero() {
		t.Fatalf("%+v", state)
	}
	if len(state.History) != 1 || state.History[0].What != "Finished (Run now)" {
		t.Fatalf("history: %+v", state.History)
	}
	c.ok("schedule.set", map[string]any{"id": id, "remove": true})
	raw := c.ok("schedule.read", map[string]any{"id": id})
	var removed scheduleState
	if err := json.Unmarshal(raw, &removed); err != nil {
		t.Fatal(err)
	}
	if removed.Definition != nil {
		t.Fatal("schedule was not removed")
	}
}

// A device whose person proved themself with a passkey makes this computer's
// choices, a schedule among them, and the log names both the device and the
// passkey. The claim goes with a device's via and a passkey's name, or not
// at all.
func TestAPasskeyDeviceMakesThisComputersChoices(t *testing.T) {
	o := options(t)
	path, _ := start(t, o)
	c := connect(t, path)
	c.hello()
	id := opened(t, c, t.TempDir())
	for _, hello := range []map[string]any{
		{"protocol": 1, "passkey": "laptop"},
		{"protocol": 1, "via": "web:laptop", "passkey": "My Laptop"},
	} {
		if r := connect(t, path).call("hello", hello); r.Error == nil {
			t.Fatalf("hello %v was taken", hello)
		}
	}
	laptop := connect(t, path)
	var hello struct {
		Peer string `json:"peer"`
	}
	into(t, laptop.ok("hello", map[string]any{"protocol": 1, "via": "web:tablet", "passkey": "laptop"}), &hello)
	if !strings.HasSuffix(hello.Peer, " via web:tablet passkey:laptop") {
		t.Fatalf("peer %q", hello.Peer)
	}
	laptop.ok("schedule.set", map[string]any{"id": id, "message": "check", "repeat": "daily", "at": time.Now().Add(48 * time.Hour).Format(schedule.Layout)})
	state, err := core.Store{Dir: o.Paths.SessionsDir}.Schedule(id)
	if err != nil {
		t.Fatal(err)
	}
	if state.Definition == nil || state.Definition.Actor != hello.Peer {
		t.Fatalf("the schedule's actor: %+v", state.Definition)
	}
}

func TestDaemonDispatchesAnOverdueScheduleAtStartup(t *testing.T) {
	fakeModel := fake.NewT(t, fake.Turn{Content: "startup run"})
	defer fakeModel.Close()
	o := options(t)
	o.Provider = llm.NewOllama(fakeModel.URL)
	seed := &server{options: o}
	now := time.Now().Truncate(time.Minute)
	id := scheduledTask(t, seed, now.Add(-time.Hour))
	path, _ := start(t, o)
	c := connect(t, path)
	c.hello()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var state scheduleState
		into(t, c.ok("schedule.read", map[string]any{"id": id}), &state)
		if state.LastRun != nil {
			if state.LastRun.Manual || state.LastRun.Missed != 1 {
				t.Fatalf("%+v", state.LastRun)
			}
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("daemon did not dispatch its startup tick")
}

// A rule's schedule is dispatched by the tick like a repeat's. Comparing the
// definition a tick read with the one read after resuming compared a rule's
// pointers, and called every rule's schedule changed.
func TestSchedulerRunsARule(t *testing.T) {
	fakeModel := fake.NewT(t, fake.Turn{Content: "ruled"})
	defer fakeModel.Close()
	o := options(t)
	o.Provider = llm.NewOllama(fakeModel.URL)
	s := schedulerServer(t, o)
	now := time.Now().Truncate(time.Minute)
	id := scheduledTask(t, s, now)
	rule := schedule.Rule{Every: 30, From: now.Add(-2 * time.Hour).Format(schedule.Layout)}.Normal()
	if err := s.store().SetSchedule(id, log.SessionSchedule{Definition: schedule.Definition{Message: "check", Rule: &rule}}); err != nil {
		t.Fatal(err)
	}
	tickSchedule(s, now)
	scheduleIdle(t, s, id)
	state, err := s.store().Schedule(id)
	if err != nil {
		t.Fatal(err)
	}
	if state.LastRun == nil || state.LastSkip != nil || state.LastRun.Missed < 3 {
		t.Fatalf("%+v %+v", state.LastRun, state.LastSkip)
	}
}

// A schedule is set with a rule, and the daemon says it in words: the rule
// in a sentence, the next times as labels, and the same sentence in the
// record, for the transcript. A change that keeps the timing keeps its
// progress; a new timing starts after now.
func TestScheduleRulesAreSaidInWords(t *testing.T) {
	t.Setenv("TZ", "America/Los_Angeles")
	o := options(t)
	path, _ := start(t, o)
	c := connect(t, path)
	c.hello()
	id := opened(t, c, t.TempDir())
	// Closed, its log can be written here as a daemon that had run it would.
	c.ok("session.close", map[string]any{"id": id})
	weekdays := []string{"fri", "mon", "tue", "wed", "thu"}
	set := func(rule map[string]any, message string) scheduleState {
		t.Helper()
		var state scheduleState
		into(t, c.ok("schedule.set", map[string]any{"id": id, "message": message, "rule": rule}), &state)
		return state
	}
	state := set(map[string]any{"every": 30, "between": []string{"9:00", "18:00"}, "days": weekdays}, "check")
	if state.Sentence != "Every 30 minutes from 09:00 to 18:00 on weekdays" || len(state.Next) != 5 || len(state.NextWords) != 5 || state.Timezone != "America/Los_Angeles" {
		t.Fatalf("%+v", state)
	}
	if state.Rule == nil || state.Rule.From == "" || state.Definition.Rule.Between[0] != "09:00" || state.Definition.Rule.Days[0] != "mon" {
		t.Fatalf("the rule was not kept in its normal form, from now: %+v", state.Definition.Rule)
	}
	for i, next := range state.Next {
		local := next.In(time.Local)
		if local.Weekday() == time.Saturday || local.Weekday() == time.Sunday || local.Hour() < 9 || local.Hour()*60+local.Minute() > 18*60 || local.Minute()%30 != 0 {
			t.Fatalf("next %d is %s", i, local)
		}
		if state.NextWords[i] != schedule.Label(local, time.Now()) {
			t.Fatalf("next %d is said %q", i, state.NextWords[i])
		}
	}
	var page core.LogPage
	into(t, c.ok("log.read", map[string]any{"id": id, "limit": 100}), &page)
	words := ""
	for _, ev := range page.Events {
		var rec log.SessionSchedule
		if ev.Kind == log.KindSessionSchedule && ev.Decode(&rec) == nil {
			words = rec.Words
		}
	}
	if words != state.Sentence {
		t.Fatalf("the record says %q", words)
	}
	// The same timing written another way keeps the progress recorded.
	cursor := time.Now().Add(-time.Hour).Truncate(time.Minute)
	kept := *state.Definition
	kept.After = cursor
	srv := &server{options: o}
	if err := srv.store().SetSchedule(id, kept); err != nil {
		t.Fatal(err)
	}
	state = set(map[string]any{"every": 30, "between": []string{"09:00", "18:00"}, "days": []string{"mon", "tue", "wed", "thu", "fri", "mon"}, "from": kept.Rule.From}, "check again")
	if !state.Cursor.Equal(cursor) || state.Definition.Message != "check again" {
		t.Fatalf("a change of message lost the progress: %s", state.Cursor)
	}
	// A new timing starts after now, even from a first minute long past.
	state = set(map[string]any{"at": []string{"15:00"}, "days": []string{"tue", "thu"}, "from": "2020-01-01T00:00"}, "check")
	if state.Sentence != "Tuesdays and Thursdays at 15:00" || time.Since(state.Cursor) > time.Minute || len(state.Next) != 5 {
		t.Fatalf("%s from %s: %v", state.Sentence, state.Cursor, state.Next)
	}
	for _, next := range state.Next {
		if local := next.In(time.Local); local.Hour() != 15 || (local.Weekday() != time.Tuesday && local.Weekday() != time.Thursday) {
			t.Fatalf("next %s", local)
		}
	}
	// The preview says the same before anything is saved, and refuses what
	// a schedule could not be.
	var preview scheduleState
	into(t, c.ok("schedule.next", map[string]any{"message": "x", "cost": 0, "paused": false, "rule": map[string]any{"at": []string{"08:00"}, "dates": []int{15, 1}}}), &preview)
	if preview.Sentence != "On the 1st and 15th of each month at 08:00" || len(preview.Next) != 5 {
		t.Fatalf("%+v", preview)
	}
	for _, bad := range []map[string]any{
		{"message": "x", "cost": 0, "paused": false, "rule": map[string]any{"every": 4}},
		{"message": "x", "cost": 0, "paused": false, "rule": map[string]any{"every": 30, "at": []string{"09:00"}}},
		{"message": "x", "cost": 0, "paused": false, "repeat": "daily", "at": "2026-10-20T09:00", "rule": map[string]any{"every": 30}},
	} {
		if r := c.call("schedule.next", bad); r.Error == nil {
			t.Fatalf("accepted %v", bad)
		}
		bad["id"] = id
		if r := c.call("schedule.set", bad); r.Error == nil {
			t.Fatalf("saved %v", bad)
		}
	}
}

func TestTheZoneIsNamed(t *testing.T) {
	for tz, want := range map[string]string{"America/Santiago": "America/Santiago", ":Europe/Lisbon": "Europe/Lisbon", "": "UTC"} {
		t.Setenv("TZ", tz)
		if got := zoneName(); got != want {
			t.Errorf("TZ=%q: got %q want %q", tz, got, want)
		}
	}
	if err := os.Unsetenv("TZ"); err != nil {
		t.Fatal(err)
	}
	if got := zoneName(); got == "" || got == "Local" {
		t.Fatalf("the computer's zone is named %q", got)
	}
}

// A daily spend limit stops the automatic runs of a day once what that day's
// runs cost reaches it, with one skip standing for the rest of the day's
// times, and they go on the next day.
func TestADailySpendLimitSkipsTheRestOfTheDay(t *testing.T) {
	o := options(t)
	s := schedulerServer(t, o)
	y, m, d := time.Now().Date()
	noon := time.Date(y, m, d, 12, 0, 0, 0, time.Local)
	id := scheduledTask(t, s, noon.Add(-time.Hour))
	session, err := core.Resume(o, id)
	if err != nil {
		t.Fatal(err)
	}
	cost := 1.5
	if _, err := session.Record(log.KindScheduleRun, log.ScheduleRun{Due: noon.Add(-time.Hour), Through: noon.Add(-time.Hour), Started: noon.Add(-time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if _, err := session.Record(log.KindLLMResp, log.LLMResp{Usage: log.Usage{Cost: &cost}}); err != nil {
		t.Fatal(err)
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	state, err := s.store().Schedule(id)
	if err != nil {
		t.Fatal(err)
	}
	def := *state.Definition
	def.DayCost, def.After = 1, state.Cursor
	if err := s.store().SetSchedule(id, def); err != nil {
		t.Fatal(err)
	}
	tickSchedule(s, noon)
	state, err = s.store().Schedule(id)
	if err != nil {
		t.Fatal(err)
	}
	if state.LastSkip == nil || !strings.Contains(state.LastSkip.Reason, "daily spend limit") || state.LastSkip.Through.Format("15:04") != "23:00" || state.LastRun.Manual || !state.LastRun.Started.Equal(noon.Add(-time.Hour)) {
		t.Fatalf("skip %+v, run %+v", state.LastSkip, state.LastRun)
	}
	skips := len(state.History)
	tickSchedule(s, noon.Add(3*time.Hour))
	if state, err = s.store().Schedule(id); err != nil || len(state.History) != skips {
		t.Fatalf("the rest of the day was skipped again: %+v %v", state.History, err)
	}
	if def.Definition.Validate() != nil || (schedule.Definition{Message: "x", Repeat: "daily", At: "2026-10-20T09:00", DayCost: -1}).Validate() == nil {
		t.Fatal("a negative daily limit was taken")
	}
}
