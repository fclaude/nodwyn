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
// one invited by the person's passkey device makes the person's choices and
// changes the host's settings, but is offered no other setup, one invited
// from the terminal answers. The host's revocation closes the open socket
// and refuses the next; an address changed in hosts.toml is sent no cookie;
// unlinking tells the host and forgets the token, its bindings and the line.
func TestALinkedHostIsReachedThroughThisComputersDaemon(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// The host, workshop: a daemon and a console other machines reach, at a
	// localhost name, where a browser could use passkeys.
	tarroRoot := tempRoot(t, "nodwyn-workshop-")
	tarroSocket, tarroPaths := startDaemon(t, ctx, tarroRoot, nil)
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
	for method, want := range map[string]bool{"session.open": true, "session.model.set": true, "schedule.set": true, "settings.write": true, "settings.model.write": true, "models.test": true, "settings.provider.write": false, "settings.provider.remove": false, "settings.control.write": false, "devices.revoke": false, "session.delete": false, "startup.write": false, "devices.leave": true} {
		if slices.Contains(greeting.Methods, method) != want {
			t.Errorf("a link the person's passkey vouched for is offered %s: %v, want %v", method, !want, want)
		}
	}

	// It changes workshop's settings, and not the laptop's (ADR 54, amended
	// 2026-10-23).
	var settings struct {
		Values map[string]struct {
			Value any  `json:"value"`
			Set   bool `json:"set"`
		} `json:"values"`
	}
	if err := call(conn, "settings.write", map[string]any{"set": map[string]any{"budget.tool_calls_soft": 40}}, &settings); err != nil {
		t.Fatalf("the vouched link's settings.write: %v", err)
	}
	if v := settings.Values["budget.tool_calls_soft"]; v.Value != float64(40) || !v.Set {
		t.Fatalf("workshop's settings after the link's write: %+v", v)
	}
	if written, err := os.ReadFile(tarroPaths.ConfigFile); err != nil || !bytes.Contains(written, []byte(`tool_calls_soft = 40`)) {
		t.Fatalf("workshop's config.toml after the link's write: %s %v", written, err)
	}
	if mine, err := os.ReadFile(paths.ConfigFile); err == nil && bytes.Contains(mine, []byte("tool_calls_soft")) {
		t.Fatalf("the link's write changed the laptop's config.toml: %s", mine)
	}
	// What would widen what a task may do waits for a touch, which a link
	// cannot give, even in a call that also holds a setting it may change.
	for _, set := range []map[string]any{{"approval.dont_ask": true}, {"policy.allow_insecure_http": true}, {"judge.server": nil}, {"budget.tool_calls_hard": 90, "approval.writes": "ask"}} {
		if err := call(conn, "settings.write", map[string]any{"set": set}, &settings); err == nil || !strings.Contains(err.Error(), "widens what this host's tasks may do") {
			t.Errorf("the vouched link's settings.write of %v: %v", set, err)
		}
	}
	if written, err := os.ReadFile(tarroPaths.ConfigFile); err != nil || bytes.Contains(written, []byte("dont_ask")) || bytes.Contains(written, []byte("tool_calls_hard")) || bytes.Contains(written, []byte(`writes = "ask"`)) {
		t.Fatalf("a refused write changed workshop's config.toml: %s %v", written, err)
	}
	if err := call(conn, "settings.docs.write", map[string]any{"google_upload": "allow"}, &settings); err == nil || !strings.Contains(err.Error(), "docs.google_upload") {
		t.Errorf("the vouched link's settings.docs.write allow: %v", err)
	}
	if err := call(conn, "settings.provider.write", map[string]any{"provider": "ollama", "url": "http://127.0.0.1:1"}, &settings); err == nil {
		t.Error("the vouched link wrote a provider")
	}
	// A setting it changed is no touch of a passkey: the rest of setup stays
	// refused to it afterwards, as it was before.
	for _, method := range []string{"mcp.remove", "credentials.remove", "startup.write", "trust.set", "host.unlink"} {
		if err := call(conn, method, map[string]any{}, &settings); err == nil || !strings.Contains(err.Error(), "not available from this device") {
			t.Errorf("after changing a setting the vouched link's %s: %v", method, err)
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
	// and makes none of the person's choices nor changes workshop's settings.
	plain, _, _ := devices.Invite(Terminal, true)
	link(plain)
	conn = open()
	greeting = hello(conn)
	if !strings.HasSuffix(greeting.Peer, " via web:laptop") {
		t.Fatalf("workshop's daemon sees the answering link as %q", greeting.Peer)
	}
	for method, want := range map[string]bool{"session.open": true, "session.answer": true, "session.model.set": false, "schedule.set": false, "settings.write": false} {
		if slices.Contains(greeting.Methods, method) != want {
			t.Errorf("a link from workshop's terminal is offered %s: %v, want %v", method, !want, want)
		}
	}
	if err := call(conn, "settings.write", map[string]any{"set": map[string]any{"budget.tool_calls_soft": nil}}, &settings); err == nil {
		t.Fatal("a link from workshop's terminal changed workshop's settings")
	}
	if written, _ := os.ReadFile(tarroPaths.ConfigFile); !bytes.Contains(written, []byte(`tool_calls_soft = 40`)) {
		t.Fatalf("a refused write changed workshop's config.toml: %s", written)
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
	// A link never touches, so a setup method other than the settings is
	// refused to it outright rather than offered for a touch it cannot give.
	if b.access("host.link", caller{person: true, link: true}) != refused || b.access("host.link", caller{person: true}) != needsTouch {
		t.Error("a link is offered setup")
	}
}

// Another nodwyn that is the person's changes this computer's settings
// without a touch (ADR 54, amended 2026-10-23), and nothing else of its
// setup; one that only answers does not, nor does any link of a console
// started with --no-link-settings, which still lets the person's own
// devices in.
func TestALinkChangesTheHostsSettings(t *testing.T) {
	settings := []string{"settings.write", "settings.docs.write", "settings.model.write", "settings.model.remove", "models.test"}
	// Where a task's content goes is the touch's: a provider's address and
	// key, and the default model.
	touch := []string{"settings.control.write", "settings.provider.write", "settings.provider.remove"}
	other := []string{"host.link", "host.unlink", "startup.write", "devices.revoke", "passkeys.remove", "session.delete", "credentials.add", "keychain.passphrase", "mcp.save", "workspace.save", "trust.set"}
	person, answers := caller{person: true, link: true}, caller{link: true}
	b := &bridge{devices: OpenDevices(filepath.Join(t.TempDir(), "devices.toml"))}
	for _, method := range settings {
		if got := b.access(method, person); got != granted {
			t.Errorf("the person's link is given %s: %v, want granted", method, got)
		}
		if got := b.access(method, answers); got != refused {
			t.Errorf("a link that only answers is given %s: %v, want refused", method, got)
		}
		if got := b.access(method, caller{person: true}); got != needsTouch {
			t.Errorf("the person's device is given %s before a touch: %v", method, got)
		}
		if !b.allowed(method, caller{local: true}) || !b.allowed(method, caller{person: true, touched: true}) {
			t.Errorf("%s is refused to this computer or to a device that touched", method)
		}
	}
	for _, method := range append(touch, other...) {
		if got := b.access(method, person); got != refused {
			t.Errorf("the person's link is given %s: %v, want refused", method, got)
		}
	}
	for _, method := range touch {
		if !b.allowed(method, caller{local: true}) || !b.allowed(method, caller{person: true, touched: true}) || b.access(method, caller{person: true}) != needsTouch {
			t.Errorf("%s is not setup for this computer and the person's device", method)
		}
	}

	readOnly := &bridge{readOnly: true, devices: b.devices}
	if readOnly.access("settings.write", person) != refused {
		t.Error("a read-only console lets a link change its settings")
	}

	blocked := &bridge{noLinkSettings: true, devices: b.devices}
	for _, method := range append(settings, touch...) {
		if got := blocked.access(method, person); got != refused {
			t.Errorf("with --no-link-settings the person's link is given %s: %v", method, got)
		}
		if !blocked.allowed(method, caller{local: true}) || !blocked.allowed(method, caller{person: true, touched: true}) || blocked.access(method, caller{person: true}) != needsTouch {
			t.Errorf("--no-link-settings keeps %s from this computer or the person's device", method)
		}
	}
}

// A link changes the settings that cannot widen what a task may do, and none
// of the approvals, the policy or the judge, nor an upload to Google Docs
// that is never asked about (ADR 54, amended 2026-10-23).
func TestLinkWidens(t *testing.T) {
	for _, tt := range []struct {
		name, method, params, want string
	}{
		{"a budget", "settings.write", `{"set":{"budget.tool_calls_soft":40,"loop.shell_timeout":"2m"}}`, ""},
		{"a notice", "settings.write", `{"set":{"notify.desktop":true,"mcp.runtime":null}}`, ""},
		{"a window", "settings.write", `{"set":{"window.high_water":9000}}`, ""},
		{"an approval", "settings.write", `{"set":{"approval.commands":"ask"}}`, "approval.commands"},
		{"Don't ask taken away", "settings.write", `{"set":{"approval.dont_ask":null}}`, "approval.dont_ask"},
		{"the policy", "settings.write", `{"set":{"policy.allow_insecure_http":false}}`, "policy.allow_insecure_http"},
		{"the judge", "settings.write", `{"set":{"budget.cost_hard":1,"judge.approve_threshold":0.5}}`, "judge.approve_threshold"},
		{"unreadable", "settings.write", `[]`, "its settings could not be read"},
		{"uploads asked", "settings.docs.write", `{"google_upload":"ask"}`, ""},
		{"uploads refused", "settings.docs.write", `{"google_upload":"deny"}`, ""},
		{"uploads allowed", "settings.docs.write", `{"google_upload":"allow"}`, "docs.google_upload"},
		{"a model", "settings.model.write", `{"server":"p","model":"m"}`, ""},
	} {
		if got := linkWidens(tt.method, json.RawMessage(tt.params)); got != tt.want {
			t.Errorf("%s: linkWidens = %q, want %q", tt.name, got, tt.want)
		}
	}
}
