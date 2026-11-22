package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// Terminal invitations authorise a computer directly; restricted invitations
// cannot acquire owner access merely by claiming to be another nodwyn.
func TestTerminalInvitationsAuthoriseComputerLinks(t *testing.T) {
	for _, owner := range []bool{true, false} {
		t.Run(map[bool]string{true: "owner", false: "tasks-only"}[owner], func(t *testing.T) {
			devices := OpenDevices(filepath.Join(t.TempDir(), "devices.toml"))
			invite, _, err := devices.Invite(Terminal, owner)
			if err != nil {
				t.Fatal(err)
			}
			token, err := devices.Link(invite, "laptop", true)
			if err != nil {
				t.Fatal(err)
			}
			device, ok := devices.Check(token)
			if !ok {
				t.Fatal("the new link was not paired")
			}
			b := &bridge{devices: devices, tls: true}
			r := httptest.NewRequest("GET", "https://workshop.example/socket", nil)
			c := b.callerOf(r, device)
			if got := b.allowed("session.model.set", c); got != owner {
				t.Fatalf("model access = %v, want %v", got, owner)
			}
			if got := b.allowed("settings.write", c); got != owner {
				t.Fatalf("settings access = %v, want %v", got, owner)
			}
			for _, method := range []string{"settings.provider.write", "credentials.add", "trust.set", "devices.authorise"} {
				if b.access(method, c) != refused {
					t.Errorf("a computer link acquired sensitive method %s", method)
				}
			}
		})
	}
	// A browser's name can be terminal too; its grant must remain bound to
	// the actual passkey rather than becoming an independent terminal grant.
	t.Run("passkey-device-named-terminal", func(t *testing.T) {
		devices := OpenDevices(filepath.Join(t.TempDir(), "devices.toml"))
		first, _, err := devices.Invite(Terminal, true)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := devices.Register(first, "terminal", Passkey{ID: "terminal", PublicKey: "unused"}); err != nil {
			t.Fatal(err)
		}
		invite, _, err := devices.InviteAsPerson("terminal", "terminal")
		if err != nil {
			t.Fatal(err)
		}
		token, err := devices.Link(invite, "laptop", true)
		if err != nil {
			t.Fatal(err)
		}
		device, ok := devices.Check(token)
		if !ok || device.Passkey != "terminal" {
			t.Fatalf("the authorising passkey was lost: %+v", device)
		}
		raw, err := json.Marshal(device)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), `"owner":true`) {
			t.Fatal("a browser named terminal acquired an independent owner grant")
		}
	})
}

// An owner invitation cannot be silently downgraded or consumed on an unsafe
// transport; retrying it over https must still work.
func TestOwnerInvitationRequiresSecureTransport(t *testing.T) {
	devices := OpenDevices(filepath.Join(t.TempDir(), "devices.toml"))
	invite, _, err := devices.Invite(Terminal, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := devices.Link(invite, "laptop", false); err == nil || !strings.Contains(err.Error(), "https") {
		t.Fatalf("owner invitation over an unsafe transport: %v", err)
	}
	if _, err := devices.Link(invite, "laptop", true); err != nil {
		t.Fatalf("the refused attempt consumed the invitation: %v", err)
	}
}

// Existing links retain their token and pairing date when the person confirms
// owner access, and lose a passkey-backed grant if that passkey is removed.
func TestAuthoriseAnExistingComputerLink(t *testing.T) {
	devices := OpenDevices(filepath.Join(t.TempDir(), "devices.toml"))
	first, _, err := devices.Invite(Terminal, true)
	if err != nil {
		t.Fatal(err)
	}
	phoneToken, err := devices.Register(first, "phone", Passkey{ID: "phone", PublicKey: "unused"})
	if err != nil {
		t.Fatal(err)
	}
	phone, ok := devices.Check(phoneToken)
	if !ok {
		t.Fatal("the authorising phone was not paired")
	}
	invite, _, err := devices.Invite(Terminal, false)
	if err != nil {
		t.Fatal(err)
	}
	token, err := devices.Link(invite, "laptop", true)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := devices.Check(token)
	b := &bridge{devices: devices, tls: true}
	if b.access("devices.authorise", caller{local: true}) != refused || b.access("devices.authorise", caller{person: true}) != needsTouch || b.access("devices.authorise", caller{person: true, touched: true}) != granted {
		t.Fatal("authorising a link must require a passkey confirmation")
	}
	if _, err := b.deviceMethod(request{Method: "devices.authorise", Params: json.RawMessage(`{"name":"laptop"}`)}, phone, caller{person: true, touched: true}); err != nil {
		t.Fatal(err)
	}
	after, ok := devices.Check(token)
	if !ok || before.Hash != after.Hash || !before.Paired.Equal(after.Paired) || after.Passkey != "phone" {
		t.Fatalf("authorisation replaced the link: before %+v, after %+v", before, after)
	}
	r := httptest.NewRequest("GET", "https://workshop.example/socket", nil)
	if !b.allowed("settings.write", b.callerOf(r, after)) {
		t.Fatal("the authorised link still cannot change settings")
	}
	backup, _, err := devices.Invite(Terminal, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := devices.Register(backup, "backup", Passkey{ID: "backup", PublicKey: "unused"}); err != nil {
		t.Fatal(err)
	}
	if err := devices.RemovePasskey("phone"); err != nil {
		t.Fatal(err)
	}
	after, _ = devices.Check(token)
	if b.allowed("settings.write", b.callerOf(r, after)) {
		t.Fatal("a removed passkey still authorises the link")
	}
}

// join pairs a device named name with a plain invitation from the terminal
// and returns its token.
func join(t *testing.T, devices *Devices, name string) string {
	t.Helper()
	code, _, err := devices.Invite(Terminal, false)
	if err != nil {
		t.Fatal(err)
	}
	token, err := devices.Join(code, name)
	if err != nil {
		t.Fatal(err)
	}
	return token
}

// A device stays signed in while it is used and is signed out only after
// idleLife without use, whatever time passed since it paired: it used to be
// signed out 180 days after pairing, however often it was used, since its
// cookie was set once and never again. Loading its console renews the
// cookie, as connecting does.
func TestADeviceInUseStaysSignedIn(t *testing.T) {
	devices := OpenDevices(filepath.Join(t.TempDir(), "devices.toml"))
	code, _, err := devices.Invite(Terminal, false)
	if err != nil {
		t.Fatal(err)
	}
	token, err := devices.Join(code, "phone")
	if err != nil {
		t.Fatal(err)
	}
	age := func(paired, seen time.Duration) {
		t.Helper()
		if err := devices.change(func(f *deviceFile) error {
			now := time.Now().UTC()
			f.Device[0].Paired, f.Device[0].LastSeen = now.Add(-paired), now.Add(-seen)
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	// Paired a year ago and used yesterday: still signed in.
	age(365*24*time.Hour, 24*time.Hour)
	if _, ok := devices.Check(token); !ok {
		t.Fatal("a device in use was signed out")
	}
	s := New(Options{PairingCode: testCode, Devices: devices, Hosts: []string{"https://desk.example"}})
	s.b.ctx = context.Background()
	s.b.listensAt("127.0.0.1:7777")
	r := httptest.NewRequest("GET", "https://desk.example/", nil)
	r.RemoteAddr = "127.0.0.1:40000"
	r.AddCookie(&http.Cookie{Name: deviceCookie, Value: token})
	w := httptest.NewRecorder()
	s.b.ServeHTTP(w, r)
	if cookies := w.Result().Cookies(); w.Code != http.StatusOK || len(cookies) != 1 || cookies[0].Value != token || cookies[0].MaxAge != int(idleLife/time.Second) || !cookies[0].Secure {
		t.Fatalf("loading the console renewed %d %v", w.Code, cookies)
	}
	// Not used for longer than idleLife: signed out, and forgotten.
	age(365*24*time.Hour, idleLife+time.Hour)
	if _, ok := devices.Check(token); ok {
		t.Fatal("a device idle past idleLife is still signed in")
	}
	if list, _ := devices.List(); len(list) != 0 {
		t.Fatalf("an idle device is still listed: %v", list)
	}
}

// An invitation is good once and for inviteLife, and is stored only as its
// digest.
func TestAnInvitationIsGoodOnceAndExpires(t *testing.T) {
	devices := OpenDevices(filepath.Join(t.TempDir(), "devices.toml"))
	code, expires, err := devices.Invite("laptop", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(normalCode(code)) != 20 || time.Until(expires) > inviteLife {
		t.Fatalf("code %q expires %v", code, expires)
	}
	if ok, passkey := devices.Invited(code); !ok || passkey {
		t.Fatal("a plain invitation reads wrong")
	}
	stale, _, _ := devices.Invite("laptop", false)
	if err := devices.change(func(f *deviceFile) error {
		f.Invite[1].Expires = time.Now().Add(-time.Second)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := devices.Join(stale, "tablet"); err == nil {
		t.Fatal("an expired invitation paired")
	}
	if _, err := devices.Join(code, "phone"); err != nil {
		t.Fatal(err)
	}
	if _, err := devices.Join(code, "tablet"); err == nil {
		t.Fatal("an invitation paired twice")
	}
	if _, err := devices.Register(code, "tablet", Passkey{ID: "x", PublicKey: "y"}); err == nil {
		t.Fatal("a used invitation registered a passkey")
	}
	list, _ := devices.List()
	if len(list) != 1 || list[0].By != "laptop" {
		t.Fatalf("devices: %+v", list)
	}
}

// An invitation names a new device: one that took the name of a device or a
// passkey already there would lock that one out, so it is refused, and the
// invitation stays good for another name. Only the person, signing in with
// their passkey, replaces a device of the same name.
func TestAnInvitationTakesNoNameInUse(t *testing.T) {
	devices := OpenDevices(filepath.Join(t.TempDir(), "devices.toml"))
	first, _, _ := devices.Invite(Terminal, true)
	laptop, err := devices.Register(first, "laptop", Passkey{ID: "id-1", PublicKey: "key-1"})
	if err != nil {
		t.Fatal(err)
	}
	plain, _, _ := devices.Invite("phone", false)
	if _, err := devices.Join(plain, "laptop"); err == nil || !strings.Contains(err.Error(), "paired already") {
		t.Fatalf("an invitation took a device's name: %v", err)
	}
	if _, ok := devices.Check(laptop); !ok {
		t.Fatal("the laptop was locked out")
	}
	if _, err := devices.Join(plain, "tablet"); err != nil {
		t.Fatalf("a refused name used up the invitation: %v", err)
	}
	if err := devices.Revoke("laptop"); err != nil {
		t.Fatal(err)
	}
	second, _, _ := devices.Invite(Terminal, true)
	if _, err := devices.Register(second, "laptop", Passkey{ID: "id-2", PublicKey: "key-2"}); err == nil || !strings.Contains(err.Error(), "registered already") {
		t.Fatalf("an invitation replaced a passkey: %v", err)
	}
	if keys, _ := devices.Passkeys(); len(keys) != 1 || keys[0].ID != "id-1" {
		t.Fatalf("passkeys: %+v", keys)
	}
	again, err := devices.SignIn("tablet", "laptop")
	if err != nil {
		t.Fatalf("the person signing in again under a name in use: %v", err)
	}
	if d, ok := devices.Check(again); !ok || d.Passkey != "laptop" {
		t.Fatalf("the tablet signed in again: %+v", d)
	}
}

// The console, the daemon and nodwyn devices each load devices.toml, change
// it and save it, and the lock was only each process's own: a revocation
// saved from the terminal between a console's load and its save was undone.
// A change waits for the file's lock, which another process may hold.
func TestAChangeToTheDevicesFileWaitsForAnotherProcess(t *testing.T) {
	path := filepath.Join(t.TempDir(), "devices.toml")
	d := OpenDevices(path)
	code, _, err := d.Invite(Terminal, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Join(code, "phone"); err != nil {
		t.Fatal(err)
	}
	// Another process holds the file, as it does between its load and save.
	held, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := syscall.Flock(int(held.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- OpenDevices(path).Revoke("phone") }()
	select {
	case err := <-done:
		held.Close()
		t.Fatalf("the revocation did not wait for the other process: %v", err)
	case <-time.After(300 * time.Millisecond):
	}
	held.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the revocation never went ahead")
	}
	if list, err := d.List(); err != nil || len(list) != 0 {
		t.Fatalf("devices: %v %v", list, err)
	}
}
