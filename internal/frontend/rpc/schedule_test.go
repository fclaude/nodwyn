package rpc

import (
	"encoding/json"
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
	close(held.release)
	scheduleIdle(t, s, id)
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
