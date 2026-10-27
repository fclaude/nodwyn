package web

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type managedFake struct {
	parent bool
	called bool
}

func (m *managedFake) ServeHTTP(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) }
func (m *managedFake) ServeChild(w http.ResponseWriter, _ *http.Request, parent bool) {
	m.parent = parent
	m.called = true
	w.WriteHeader(204)
}
func TestManagedChildCannotReachRPCOrParentAuthority(t *testing.T) {
	devices := OpenDevices(filepath.Join(t.TempDir(), "devices.toml"))
	invite, _, e := devices.Invite(Terminal, false)
	if e != nil {
		t.Fatal(e)
	}
	token, e := devices.Join(invite, "child-browser")
	if e != nil {
		t.Fatal(e)
	}
	fake := &managedFake{}
	b := New(Options{Managed: fake, Devices: devices, Hosts: []string{"https://child.test"}}).b
	b.listensAt("127.0.0.1:12345")
	for _, path := range []string{"/socket", "/attach", "/kids/admin/invite", "/kids/admin/devices", "/kids/admin/revoke"} {
		r := httptest.NewRequest("POST", "https://child.test"+path, strings.NewReader(`{}`))
		r.Host = "child.test"
		r.Header.Set("Origin", "https://child.test")
		r.AddCookie(&http.Cookie{Name: deviceCookie, Value: token})
		w := httptest.NewRecorder()
		b.ServeHTTP(w, r)
		if w.Code != 403 && w.Code != 404 {
			t.Fatalf("%s = %d", path, w.Code)
		}
	}
	r := httptest.NewRequest("GET", "https://child.test/kids/api/state", nil)
	r.Host = "child.test"
	r.AddCookie(&http.Cookie{Name: deviceCookie, Value: token})
	w := httptest.NewRecorder()
	b.ServeHTTP(w, r)
	if w.Code != 204 || !fake.called || fake.parent {
		t.Fatal("child role was widened")
	}
	if e = devices.Revoke("child-browser"); e != nil {
		t.Fatal(e)
	}
	w = httptest.NewRecorder()
	b.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal("revoked device still has access")
	}
}
func TestManagedConsoleUsesPrivateUnixHTTPAndExactOrigin(t *testing.T) {
	listener, e := net.Listen("unix", filepath.Join(tempRoot(t, "nodwyn-managed-"), "http.sock"))
	if e != nil {
		t.Fatal(e)
	}
	defer listener.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	code, e := PairingCode()
	if e != nil {
		t.Fatal(e)
	}
	server := New(Options{PairingCode: code, Devices: OpenDevices(filepath.Join(t.TempDir(), "devices.toml")), Hosts: []string{"https://child.test"}, Managed: &managedFake{}})
	done := make(chan error, 1)
	go func() { done <- server.Serve(ctx, listener) }()
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", listener.Addr().String())
	}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 3 * time.Second}
	r, e := http.NewRequest("GET", "http://child.test/pair", nil)
	if e != nil {
		t.Fatal(e)
	}
	response, e := client.Do(r)
	if e != nil {
		t.Fatal(e)
	}
	response.Body.Close()
	if response.StatusCode != 200 {
		t.Fatal(response.StatusCode)
	}
	r.Header.Set("Origin", "https://child.test:8443")
	response, e = client.Do(r)
	if e != nil {
		t.Fatal(e)
	}
	response.Body.Close()
	if response.StatusCode != 403 {
		t.Fatal("preview origin accepted")
	}
	cancel()
	if e = <-done; e != nil {
		t.Fatal(e)
	}
}
