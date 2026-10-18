package rpc

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fclaude/nodwyn/internal/config"
	"github.com/fclaude/nodwyn/internal/event"
	"github.com/fclaude/nodwyn/internal/keychain"
	"github.com/fclaude/nodwyn/internal/startup"
)

// told collects the daemon's desktop notices.
func told() (chan string, ServeOption) {
	notices := make(chan string, 16)
	return notices, WithNotify(func(title, body string) error { notices <- title + ": " + body; return nil })
}

func noticed(t *testing.T, notices chan string, want string) {
	t.Helper()
	select {
	case got := <-notices:
		if got != want {
			t.Fatalf("told %q, want %q", got, want)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("not told %q", want)
	}
}

func quiet(t *testing.T, notices chan string, why string) {
	t.Helper()
	select {
	case got := <-notices:
		t.Fatalf("told %q %s", got, why)
	case <-time.After(300 * time.Millisecond):
	}
}

// The daemon says on the desktop that a task needs an answer when no window
// of this computer watches, in fixed words: a paired phone's page is not
// this computer's, and a console watching says so itself (ADR 53 §11).
func TestTheDaemonSaysOnTheDesktopThatATaskNeedsAnAnswer(t *testing.T) {
	notices, notify := told()
	o := options(t)
	o.Config.Notify.Desktop = true
	path, _ := start(t, o, notify)
	c := connect(t, path)
	c.hello()
	id := opened(t, c, t.TempDir())
	subscribed(t, c, id, 0, 100)
	phone := connect(t, path)
	phone.ok("hello", map[string]any{"protocol": 1, "via": "web:phone"})
	phone.ok("session.watch", map[string]any{})
	eval(c, id, `(fs-write "notes.txt" "one")`)
	p := prompt[event.Confirm](t, c, event.PromptConfirm)
	noticed(t, notices, answerTitle+": "+answerBody)
	c.ok("session.answer", map[string]any{"id": id, "prompt_id": p.ID, "reply": "y"})
	finishEval(t, c)
	quiet(t, notices, "once the answer was given")
	// A console of this computer watching shows its own notification.
	c.ok("session.watch", map[string]any{})
	eval(c, id, `(fs-write "more.txt" "two")`)
	p = prompt[event.Confirm](t, c, event.PromptConfirm)
	quiet(t, notices, "while a console of this computer watched")
	c.ok("session.answer", map[string]any{"id": id, "prompt_id": p.ID, "reply": "n"})
	finishEval(t, c)
}

// Off in Settings, as it is unless a person turns it on, nothing is shown.
func TestDesktopNoticesAreOffUnlessTurnedOn(t *testing.T) {
	calls := 0
	s := &server{notify: func(string, string) error { calls++; return nil }}
	s.tell(answerTitle, answerBody)
	if config.Default().Notify.Desktop || calls != 0 {
		t.Fatal("a notice was shown with notify.desktop off")
	}
	o := options(t)
	path, _ := start(t, o)
	c := connect(t, path)
	c.hello()
	var settings settingsResult
	into(t, c.ok("settings.write", map[string]any{"set": map[string]any{"notify.desktop": true}}), &settings)
	if settings.Values["notify.desktop"].Value != true {
		t.Fatalf("%+v", settings.Values["notify.desktop"])
	}
	if raw, err := os.ReadFile(o.Paths.ConfigFile); err != nil || !strings.Contains(string(raw), "[notify]\ndesktop = true") {
		t.Fatalf("config.toml: %s %v", raw, err)
	}
}

// A locked keychain stopping a scheduled run is said on the desktop once,
// until the keychain is unlocked; the schedule says its next run is held, and
// the time waits for the unlock instead of being skipped.
func TestALockedKeychainStoppingAScheduledRunIsSaidOnce(t *testing.T) {
	notices, notify := told()
	o := options(t)
	s := schedulerServer(t, o)
	notify(s)
	s.notifyOn.Store(true)
	now := time.Now().Truncate(time.Minute)
	id := scheduledTask(t, s, now)
	// The model needs a key, and the keychain holding it is locked.
	s.options.Config.Control = config.Model{Provider: "openai", URL: "http://127.0.0.1:1/v1", Model: "m", Credential: "model-key"}
	if err := os.MkdirAll(filepath.Dir(o.Paths.Keychain), 0o700); err != nil {
		t.Fatal(err)
	}
	keys, err := keychain.Create(o.Paths.Keychain, keychain.FastKDF, []byte("passphrase"))
	if err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	state, err := s.scheduleState(id)
	s.mu.Unlock()
	if err != nil || state.Held == "" {
		t.Fatalf("the schedule does not say its run is held: %+v %v", state, err)
	}
	tickSchedule(s, now)
	noticed(t, notices, keysTitle+": "+keysBody)
	tickSchedule(s, now.Add(time.Hour))
	quiet(t, notices, "a second time while the keychain stayed locked")
	// The time waits for the unlock: it is neither run nor recorded as
	// skipped (ADR 49, amended 2026-10-17).
	if state, err := s.store().Schedule(id); err != nil || state.LastSkip != nil || state.LastRun != nil {
		t.Fatalf("%+v %v", state, err)
	}
	// Unlocked at a tick with nothing due (a time held waits for the next
	// tick that finds the keychain open, and would run then), then locked
	// again: said again.
	s.options.Keychain = keys
	tickSchedule(s, now.Add(-time.Hour))
	s.options.Keychain = nil
	tickSchedule(s, now.Add(2*time.Hour))
	noticed(t, notices, keysTitle+": "+keysBody)
}

// What starts at login is this computer's setting: a paired device can
// neither read nor change it, and a daemon without a login item, or one
// that is a temporary build, offers none to turn on.
func TestStartingAtLoginIsThisComputersSetting(t *testing.T) {
	home := t.TempDir()
	place := startup.Place{GOOS: "linux", Home: home, ConfigHome: filepath.Join(home, ".config")}
	unit := filepath.Join(home, ".config", "systemd", "user", "nodwyn.service")
	path, _ := start(t, options(t), WithStartup(place, "/opt/nodwyn/bin/nodwyn"))
	c := connect(t, path)
	c.hello()
	var state startupState
	into(t, c.ok("startup.read", map[string]any{}), &state)
	if !state.Offered || !state.Supported || state.On || state.Why != "" || state.Program != "/opt/nodwyn/bin/nodwyn" {
		t.Fatalf("%+v", state)
	}
	phone := connect(t, path)
	phone.ok("hello", map[string]any{"protocol": 1, "via": "web:phone"})
	for _, method := range []string{"startup.read", "startup.write"} {
		if r := phone.call(method, map[string]any{"on": true}); r.Error == nil {
			t.Fatalf("a device called %s", method)
		}
	}
	if _, err := os.Stat(unit); !os.IsNotExist(err) {
		t.Fatal("a device turned it on")
	}
	if r := c.call("startup.write", map[string]any{}); r.Error == nil {
		t.Fatal("startup.write without on was taken")
	}
	into(t, c.ok("startup.write", map[string]any{"on": true}), &state)
	if raw, err := os.ReadFile(unit); !state.On || state.Other || err != nil || !strings.Contains(string(raw), `ExecStart="/opt/nodwyn/bin/nodwyn" daemon`) {
		t.Fatalf("%+v %s %v", state, raw, err)
	}
	into(t, c.ok("startup.write", map[string]any{"on": false}), &state)
	if _, err := os.Stat(unit); state.On || !os.IsNotExist(err) {
		t.Fatalf("still on: %+v %v", state, err)
	}

	none, _ := start(t, options(t))
	c = connect(t, none)
	c.hello()
	into(t, c.ok("startup.read", map[string]any{}), &state)
	if state.Offered || c.call("startup.write", map[string]any{"on": true}).Error == nil {
		t.Fatalf("a daemon without a login item offered one: %+v", state)
	}

	temporary, _ := start(t, options(t), WithStartup(place, "/tmp/go-build123/b001/exe/nodwyn"))
	c = connect(t, temporary)
	c.hello()
	into(t, c.ok("startup.read", map[string]any{}), &state)
	if state.Why == "" || c.call("startup.write", map[string]any{"on": true}).Error == nil {
		t.Fatalf("a temporary build was offered: %+v", state)
	}
	if _, err := os.Stat(unit); !os.IsNotExist(err) {
		t.Fatal("a temporary build was written into the login item")
	}
}
