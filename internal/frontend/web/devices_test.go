package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"
)

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
