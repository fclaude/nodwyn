package web

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/fclaude/nodwyn/internal/binding"
	"github.com/fclaude/nodwyn/internal/config"
	"github.com/fclaude/nodwyn/internal/core"
	"github.com/fclaude/nodwyn/internal/frontend/rpc"
	"github.com/fclaude/nodwyn/internal/keychain"
	"github.com/fclaude/nodwyn/test/fakes/must"
)

// startDaemon serves a daemon for root's config and data on a socket under
// root, until the test ends.
func startDaemon(t *testing.T, ctx context.Context, root string, store *keychain.Store) (string, config.Paths) {
	t.Helper()
	socket := filepath.Join(root, "run", "sock")
	listener, err := rpc.Listen(socket)
	if err != nil {
		t.Fatal(err)
	}
	paths := config.PathsUnder(filepath.Join(root, "config"), filepath.Join(root, "data"))
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() {
		done <- rpc.Serve(ctx, listener, core.Options{Keychain: store, Config: config.Default(), Paths: paths})
	}()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	})
	return socket, paths
}

// tempRoot is a private directory under /tmp, short enough for a socket.
func tempRoot(t *testing.T, prefix string) string {
	t.Helper()
	root, err := os.MkdirTemp("/tmp", prefix)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(root) })
	return root
}

// A laptop links to an always-on host with an invitation made there (ADR
// 54): the token is kept in the laptop's keychain, bound to the host's name,
// and the laptop's console reaches the host's tasks through the laptop's
// daemon, as the host's device. The host decides what that device may do:
// one invited by the person's passkey device makes the person's choices but
// is offered no setup, one invited from the terminal answers. The host's
// revocation closes the open socket and refuses the next; an address changed
// in hosts.toml is sent no cookie; unlinking tells the host and forgets the
// token, its bindings and the line.
func TestALinkedHostIsReachedThroughThisComputersDaemon(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// The host, workshop: a daemon and a console other machines reach, at a
	// localhost name, where a browser could use passkeys.
	tarroRoot := tempRoot(t, "nodwyn-workshop-")
	tarroSocket, _ := startDaemon(t, ctx, tarroRoot, nil)
	devices := OpenDevices(filepath.Join(tarroRoot, "devices.toml"))
	workshop := &bridge{ctx: ctx, socket: tarroSocket, code: testCode, session: testSession, devices: devices, recheck: 50 * time.Millisecond}
	tarroServer := httptest.NewUnstartedServer(workshop)
	_, port, _ := net.SplitHostPort(tarroServer.Listener.Addr().String())
	workshop.extra = []string{"localhost:" + port}
	workshop.listensAt(tarroServer.Listener.Addr().String())
	tarroServer.Start()
	defer tarroServer.Close()
	address := "http://localhost:" + port

	// The laptop: a daemon with a keychain, and this computer's console.
	laptopRoot := tempRoot(t, "nodwyn-laptop-")
	store, err := keychain.Create(filepath.Join(laptopRoot, "keychain"), keychain.FastKDF, []byte("test-passphrase"))
	if err != nil {
		t.Fatal(err)
	}
	laptopSocket, paths := startDaemon(t, ctx, laptopRoot, store)
	laptop := &bridge{ctx: ctx, socket: laptopSocket, code: testCode, session: testSession}
	laptopServer := httptest.NewUnstartedServer(laptop)
	laptop.listensAt(laptopServer.Listener.Addr().String())
	laptopServer.Start()
	defer laptopServer.Close()
	daemon, err := rpc.Dial(ctx, laptopSocket)
	if err != nil {
		t.Fatal(err)
	}
	defer daemon.Close()

	// The person's phone, signed in to workshop with its passkey, invites the
	// laptop.
	first, _, _ := devices.Invite(Terminal, true)
	if _, err := devices.Register(first, "phone", Passkey{ID: "cGhvbmU", PublicKey: "unused"}); err != nil {
		t.Fatal(err)
	}
	vouched, _, err := devices.InviteAsPerson("phone", "phone")
	if err != nil {
		t.Fatal(err)
	}
	link := func(invite string) {
		t.Helper()
		var info struct {
			Name, Address, As, Locked string
		}
		if err := daemon.Call(ctx, "host.link", map[string]string{"name": "workshop", "address": address + "/", "as": "laptop", "invite": invite}, &info); err != nil {
			t.Fatal(err)
		}
		if info.Address != address || info.As != "laptop" || info.Locked != "" {
			t.Fatalf("host.link answered %+v", info)
		}
	}
	link(vouched)
	paired, _ := devices.List()
	if i := slices.IndexFunc(paired, func(d Device) bool { return d.Name == "laptop" }); i < 0 || !paired[i].Host || paired[i].Passkey != "phone" {
		t.Fatalf("workshop's devices after the link: %+v", paired)
	}
	if h := store.HandleForClass("nodwyn-host-workshop"); h == nil {
		t.Fatal("the link's token is not in the laptop's keychain")
	}
	table, err := binding.Load(paths.Bindings)
	if err != nil {
		t.Fatal(err)
	}
	for _, method := range []string{"GET", "POST"} {
		if bound, why := table.Lookup("nodwyn-host-workshop", "localhost", method, "header:Cookie"); !bound {
			t.Fatalf("the token is not bound for %s to workshop: %s", method, why)
		}
	}
	if bound, _ := table.Lookup("nodwyn-host-workshop", "127.0.0.1", "GET", "header:Cookie"); bound {
		t.Fatal("the token is bound for another host")
	}
	hosts, err := os.ReadFile(filepath.Join(paths.ConfigDir, "hosts.toml"))
	if err != nil || !bytes.Contains(hosts, []byte(`name = "workshop"`)) || bytes.Contains(hosts, []byte("nodwyn_device")) {
		t.Fatalf("hosts.toml: %s %v", hosts, err)
	}

	// The laptop's console opens a socket for workshop.
	open := func() *websocket.Conn {
		t.Helper()
		header := http.Header{"Origin": {laptopServer.URL}, "Authorization": {"Bearer " + testSession}}
		conn, _, err := websocket.Dial(ctx, "ws://"+laptop.local+"/socket?host=workshop", &websocket.DialOptions{HTTPHeader: header})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { conn.CloseNow() })
		return conn
	}
	id := 0
	call := func(conn *websocket.Conn, method string, params any, result any) error {
		t.Helper()
		id++
		if err := wsjson.Write(ctx, conn, map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params}); err != nil {
			return err
		}
		for {
			var frame struct {
				ID     int             `json:"id"`
				Result json.RawMessage `json:"result"`
				Error  *struct {
					Message string `json:"message"`
				} `json:"error"`
			}
			if err := wsjson.Read(ctx, conn, &frame); err != nil {
				return err
			}
			if frame.ID != id {
				continue
			}
			if frame.Error != nil {
				return errors.New(frame.Error.Message)
			}
			return json.Unmarshal(frame.Result, result)
		}
	}
	hello := func(conn *websocket.Conn) rpc.HelloResult {
		t.Helper()
		var hello rpc.HelloResult
		if err := call(conn, "hello", map[string]any{"protocol": 1}, &hello); err != nil {
			t.Fatal(err)
		}
		return hello
	}
	conn := open()
	greeting := hello(conn)
	if !strings.HasSuffix(greeting.Peer, " via web:laptop passkey:phone") {
		t.Fatalf("workshop's daemon sees the link as %q", greeting.Peer)
	}
	for method, want := range map[string]bool{"session.open": true, "session.model.set": true, "schedule.set": true, "settings.write": false, "devices.revoke": false, "session.delete": false, "devices.leave": true} {
		if slices.Contains(greeting.Methods, method) != want {
			t.Errorf("a link the person's passkey vouched for is offered %s: %v, want %v", method, !want, want)
		}
	}

	// A task on workshop, started and given a file from the laptop's console.
	repo := filepath.Join(tarroRoot, "work")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	var details struct {
		ID string `json:"id"`
	}
	if err := call(conn, "session.open", map[string]string{"repo": repo}, &details); err != nil {
		t.Fatal(err)
	}
	upload, _ := http.NewRequest("POST", laptopServer.URL+"/attach?host=workshop&id="+details.ID+"&name=notes.md", strings.NewReader("# Notes\nhello"))
	upload.Header.Set("Origin", laptopServer.URL)
	upload.Header.Set("Authorization", "Bearer "+testSession)
	resp, err := http.DefaultClient.Do(upload)
	if err != nil {
		t.Fatal(err)
	}
	receipt, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !bytes.Contains(receipt, []byte(`"name":"notes.md"`)) {
		t.Fatalf("a file for workshop's task: %d %s", resp.StatusCode, receipt)
	}
	if stored, _ := filepath.Glob(filepath.Join(repo, ".nodwyn", "attachments", "*", "notes.md")); len(stored) != 1 {
		t.Fatalf("the file is not in workshop's task folder: %v", stored)
	}
	var listed []struct {
		ID string `json:"id"`
	}
	if err := call(conn, "session.list", map[string]any{}, &listed); err != nil || !slices.ContainsFunc(listed, func(s struct {
		ID string `json:"id"`
	}) bool {
		return s.ID == details.ID
	}) {
		t.Fatalf("workshop's tasks through the link: %v %v", listed, err)
	}

	// workshop revokes the laptop: the open socket closes as a revoked device's
	// does, and the next is refused, so the page stops trying.
	if err := devices.Revoke("laptop"); err != nil {
		t.Fatal(err)
	}
	closedWith := func(conn *websocket.Conn) (websocket.StatusCode, string) {
		t.Helper()
		readCtx, stop := context.WithTimeout(ctx, 10*time.Second)
		defer stop()
		for {
			if _, _, err := conn.Read(readCtx); err != nil {
				var ce websocket.CloseError
				errors.As(err, &ce)
				return websocket.CloseStatus(err), ce.Reason
			}
		}
	}
	if code, reason := closedWith(conn); code != websocket.StatusPolicyViolation {
		t.Fatalf("the revoked link's socket closed with %d %q", code, reason)
	}
	if code, reason := closedWith(open()); code != websocket.StatusPolicyViolation || !strings.Contains(reason, "does not take this computer") {
		t.Fatalf("a socket after the revocation closed with %d %q", code, reason)
	}

	// Unlinking a revoked link cannot tell workshop, and says so; the token,
	// its bindings and the line go.
	var unlinked struct {
		Told   bool   `json:"told"`
		Detail string `json:"detail"`
	}
	if err := daemon.Call(ctx, "host.unlink", map[string]string{"name": "workshop"}, &unlinked); err != nil {
		t.Fatal(err)
	}
	if unlinked.Told || !strings.Contains(unlinked.Detail, "revoke laptop") {
		t.Fatalf("unlinking a revoked link: %+v", unlinked)
	}
	forgotten := func() {
		t.Helper()
		if store.HandleForClass("nodwyn-host-workshop") != nil {
			t.Fatal("the token is still in the keychain")
		}
		table, err := binding.Load(paths.Bindings)
		if err != nil {
			t.Fatal(err)
		}
		if bound, _ := table.Lookup("nodwyn-host-workshop", "localhost", "GET", "header:Cookie"); bound {
			t.Fatal("the token is still bound")
		}
		hosts, _ := os.ReadFile(filepath.Join(paths.ConfigDir, "hosts.toml"))
		if bytes.Contains(hosts, []byte("workshop")) {
			t.Fatalf("hosts.toml still names workshop: %s", hosts)
		}
	}
	forgotten()

	// One invited from workshop's terminal answers: it starts tasks and answers,
	// and makes none of the person's choices.
	plain, _, _ := devices.Invite(Terminal, true)
	link(plain)
	conn = open()
	greeting = hello(conn)
	if !strings.HasSuffix(greeting.Peer, " via web:laptop") {
		t.Fatalf("workshop's daemon sees the answering link as %q", greeting.Peer)
	}
	for method, want := range map[string]bool{"session.open": true, "session.answer": true, "session.model.set": false, "schedule.set": false} {
		if slices.Contains(greeting.Methods, method) != want {
			t.Errorf("a link from workshop's terminal is offered %s: %v, want %v", method, !want, want)
		}
	}
	conn.Close(websocket.StatusNormalClosure, "")

	// An address changed in hosts.toml is sent no cookie: the binding names
	// workshop's host, which the daemon checks before it sends.
	elsewhere := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Cookie") != "" {
			t.Errorf("an address changed in hosts.toml was sent the cookie %q", r.Header.Get("Cookie"))
		}
		http.Error(w, "no", http.StatusForbidden)
	}))
	defer elsewhere.Close()
	hostsFile := filepath.Join(paths.ConfigDir, "hosts.toml")
	raw, err := os.ReadFile(hostsFile)
	if err != nil {
		t.Fatal(err)
	}
	must.WriteFile(t, hostsFile, bytes.ReplaceAll(raw, []byte(address), []byte(elsewhere.URL)), 0o600)
	_, err = rpc.DialLink(ctx, laptopSocket, "", "", "workshop")
	var refused *rpc.LinkError
	if !errors.As(err, &refused) || refused.Refused || !strings.Contains(refused.Message, "not bound") {
		t.Fatalf("a link to a changed address: %v", err)
	}
	must.WriteFile(t, hostsFile, raw, 0o600)

	// Unlinking a link workshop still takes tells workshop, which forgets it.
	if err := daemon.Call(ctx, "host.unlink", map[string]string{"name": "workshop"}, &unlinked); err != nil {
		t.Fatal(err)
	}
	if !unlinked.Told {
		t.Fatalf("unlinking did not tell workshop: %+v", unlinked)
	}
	paired, _ = devices.List()
	if slices.ContainsFunc(paired, func(d Device) bool { return d.Name == "laptop" }) {
		t.Fatalf("workshop still has the laptop after it unlinked: %+v", paired)
	}
	forgotten()
}

// Only the person uses this computer's links (ADR 54): its own browser and
// a device signed in with the passkey reach a linked host, a device that
// only answers here does not, and the console never calls host.connect or
// host.attach itself, which would turn its own daemon connection into a
// relay.
func TestOnlyThePersonUsesThisComputersLinks(t *testing.T) {
	b := &bridge{devices: OpenDevices(filepath.Join(t.TempDir(), "devices.toml"))}
	for c, want := range map[caller]bool{{local: true}: true, {person: true}: true, {}: false, {link: true}: false} {
		if got := b.allowed("host.list", c); got != want {
			t.Errorf("a caller %+v may use links: %v, want %v", c, got, want)
		}
	}
	for _, method := range []string{"host.connect", "host.attach"} {
		if b.access(method, caller{local: true}) != refused {
			t.Errorf("the console may call %s", method)
		}
	}
	for _, method := range []string{"host.link", "host.unlink"} {
		if b.allowed(method, caller{person: true}) || !b.allowed(method, caller{person: true, touched: true}) || !b.allowed(method, caller{local: true}) {
			t.Errorf("%s is not setup", method)
		}
	}
	// A link never touches, so a setup method is refused to it outright
	// rather than offered for a touch it cannot give.
	if b.access("settings.write", caller{person: true, link: true}) != refused || b.access("settings.write", caller{person: true}) != needsTouch {
		t.Error("a link is offered setup")
	}
}
