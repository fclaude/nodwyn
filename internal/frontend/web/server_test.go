package web

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
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
	"github.com/fclaude/nodwyn/internal/config"
	"github.com/fclaude/nodwyn/internal/core"
	"github.com/fclaude/nodwyn/internal/event"
	"github.com/fclaude/nodwyn/internal/frontend/rpc"
	"github.com/fclaude/nodwyn/internal/keychain"
	"github.com/fclaude/nodwyn/internal/llm"
	fake "github.com/fclaude/nodwyn/test/fakes/ollama"
)

const testCode = "test-only-browser-pairing-capability-000000"

// testSession is the session a test bridge's paired local browser holds.
const testSession = "test-only-browser-session-000000000000000"

func TestBrowserAuthorityBoundary(t *testing.T) {
	b := &bridge{readOnly: true, ctx: context.Background(), code: testCode, session: testSession}
	b.listensAt("127.0.0.1:34567")
	for _, tt := range []struct {
		name, method, path, host, origin, site, body string
		status                                       int
	}{
		{"asset", "GET", "/", "127.0.0.1:34567", "", "none", "", 200},
		{"rebind", "GET", "/", "attacker.example:34567", "", "none", "", 403},
		{"foreign-origin", "POST", "/pair", "127.0.0.1:34567", "https://attacker.example", "", `{"code":"` + testCode + `"}`, 403},
		{"loopback-origin", "POST", "/pair", "127.0.0.1:34567", "http://127.0.0.1:44444", "same-site", `{"code":"` + testCode + `"}`, 403},
		{"missing-origin", "POST", "/pair", "127.0.0.1:34567", "", "", `{"code":"` + testCode + `"}`, 403},
		{"bad-code", "POST", "/pair", "127.0.0.1:34567", "http://127.0.0.1:34567", "same-origin", `{"code":"wrong"}`, 403},
		{"pair", "POST", "/pair", "127.0.0.1:34567", "http://127.0.0.1:34567", "same-origin", `{"code":"` + testCode + `"}`, 204},
		{"no-cookie", "GET", "/socket", "127.0.0.1:34567", "http://127.0.0.1:34567", "same-origin", "", 403},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest(tt.method, "http://"+tt.host+tt.path, strings.NewReader(tt.body))
			r.RemoteAddr = "127.0.0.1:50000"
			r.Header.Set("Origin", tt.origin)
			r.Header.Set("Sec-Fetch-Site", tt.site)
			r.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			b.ServeHTTP(w, r)
			if w.Code != tt.status {
				t.Fatal(w.Code, w.Body.String())
			}
			if !strings.Contains(w.Header().Get("Content-Security-Policy"), "frame-ancestors 'none'") || w.Header().Get("Cache-Control") != "no-store" {
				t.Fatal(w.Header())
			}
			if strings.Contains(w.Body.String(), testCode) {
				t.Fatal("code echoed in body")
			}
			if tt.name == "pair" {
				cookies := w.Result().Cookies()
				if len(cookies) != 1 || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteStrictMode {
					t.Fatal(cookies)
				}
			}
		})
	}
	for _, method := range []string{"session.open", "session.resume", "session.send", "session.eval", "session.friction", "session.answer", "session.secret", "session.binding.create", "session.approval.create", "session.abort", "session.close", "keychain.unlock", "keychain.passphrase", "settings.control.write", "settings.docs.write", "settings.write", "devices.revoke",
		"credentials.add", "credentials.replace", "credentials.remove", "credentials.bind", "credentials.unbind", "credentials.authorize"} {
		if readOnly(method) {
			t.Fatal(method)
		}
	}
}

func TestWebSocketUsesDaemonAndRefusesMutations(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	root, err := os.MkdirTemp("/tmp", "nodwyn-web-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(root) })
	socket := filepath.Join(root, "run", "sock")
	listener, err := rpc.Listen(socket)
	if err != nil {
		t.Fatal(err)
	}
	model := fake.New(fake.Turn{Content: "hello from daemon"})
	defer model.Close()
	cfg := config.Default()
	cfg.Control.URL = model.URL
	cfg.Control.Model = "test-model"
	paths := config.PathsUnder(filepath.Join(root, "config"), filepath.Join(root, "data"))
	done := make(chan error, 1)
	go func() {
		done <- rpc.Serve(ctx, listener, core.Options{Config: cfg, Paths: paths, Provider: llm.NewOllama(model.URL)})
	}()
	defer func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()
	client, err := rpc.Dial(ctx, socket)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	repo := filepath.Join(root, "repo")
	os.Mkdir(repo, 0700)
	var info rpc.SessionDetails
	if err := client.Call(ctx, "session.open", map[string]any{"repo": repo}, &info); err != nil {
		t.Fatal(err)
	}
	b := &bridge{readOnly: true, ctx: ctx, socket: socket, code: testCode, session: testSession}
	server := httptest.NewUnstartedServer(b)
	b.listensAt(server.Listener.Addr().String())
	server.Start()
	defer server.Close()
	header := http.Header{"Origin": {server.URL}, "Cookie": {b.cookieName() + "=" + testSession}}
	conn, _, err := websocket.Dial(ctx, "ws://"+b.local+"/socket", &websocket.DialOptions{HTTPHeader: header})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	conn.SetReadLimit(1 << 20) // as the server's; a browser has no limit, the library's default is 32 KiB
	send := func(id uint64, method string, params any) {
		t.Helper()
		if err := wsjson.Write(ctx, conn, map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params}); err != nil {
			t.Fatal(err)
		}
	}
	receive := func() map[string]json.RawMessage {
		t.Helper()
		ctx, stop := context.WithTimeout(ctx, 3*time.Second)
		defer stop()
		var frame map[string]json.RawMessage
		if err := wsjson.Read(ctx, conn, &frame); err != nil {
			t.Fatal(err)
		}
		return frame
	}
	send(10, "hello", map[string]any{"protocol": 1})
	var hello rpc.HelloResult
	if err := json.Unmarshal(receive()["result"], &hello); err != nil {
		t.Fatal(err)
	}
	if slices.Contains(hello.Methods, "session.send") || !slices.Contains(hello.Methods, "log.read") || !slices.Contains(hello.Methods, "session.watch") {
		t.Fatal(hello.Methods)
	}
	send(11, "session.watch", map[string]any{})
	if raw := receive(); !strings.Contains(string(raw["result"]), info.ID) {
		t.Fatal(raw)
	}
	send(1, "session.list", map[string]any{})
	if raw := receive(); !strings.Contains(string(raw["result"]), info.ID) {
		t.Fatal(raw)
	}
	send(2, "session.send", map[string]any{"id": info.ID, "text": "must not run"})
	if raw := receive(); !strings.Contains(string(raw["error"]), "read-only") {
		t.Fatal(raw)
	}
	for i, method := range []string{"session.answer", "session.secret", "session.approval.create", "session.binding.create", "session.friction", "session.policy.read", "session.policy.write"} {
		send(uint64(20+i), method, map[string]any{"id": info.ID, "prompt_id": "not-a-prompt", "reply": "y", "secret": "synthetic"})
		if raw := receive(); !strings.Contains(string(raw["error"]), "read-only") {
			t.Fatal(raw)
		}
	}
	send(3, "session.subscribe", map[string]any{"id": info.ID})
	if raw := receive(); len(raw["result"]) == 0 {
		t.Fatal(raw)
	}
	if err := client.Call(ctx, "session.send", map[string]any{"id": info.ID, "text": "hello"}, nil); err != nil {
		t.Fatal(err)
	}
	found := false
	for i := 0; i < 30; i++ {
		raw := receive()
		if strings.Contains(string(raw["params"]), "hello from daemon") {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("missing live event")
	}
	conn.CloseNow()
	// Browser detach cannot close the daemon-owned session.
	if err := client.Call(ctx, "session.info", map[string]any{"id": info.ID}, &info); err != nil {
		t.Fatal(err)
	}
}

func TestConversationLifecycleThroughBrowser(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	root, err := os.MkdirTemp("/tmp", "nodwyn-web-chat-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)
	socket := filepath.Join(root, "run", "sock")
	listener, err := rpc.Listen(socket)
	if err != nil {
		t.Fatal(err)
	}
	model := fake.New(fake.Turn{Content: "browser reply"})
	defer model.Close()
	cfg := config.Default()
	cfg.Control.URL = model.URL
	cfg.Control.Model = "test-model"
	cfg.Policy.Unbound = "confirm"
	cfg.Approval.Writes = config.ApproveAsk // a write is the prompt the browser answers
	cfg.Budget.ToolCallsSoft = 1
	cfg.Budget.ToolCallsHard = 3
	paths := config.PathsUnder(filepath.Join(root, "config"), filepath.Join(root, "data"))
	store, err := keychain.Create(filepath.Join(root, "keychain"), keychain.FastKDF, []byte("test-passphrase"))
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		done <- rpc.Serve(ctx, listener, core.Options{Keychain: store, Config: cfg, Paths: paths, Provider: llm.NewOllama(model.URL)})
	}()
	defer func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()
	b := &bridge{ctx: ctx, socket: socket, code: testCode, session: testSession}
	server := httptest.NewUnstartedServer(b)
	b.listensAt(server.Listener.Addr().String())
	server.Start()
	defer server.Close()
	header := http.Header{"Origin": {server.URL}, "Cookie": {b.cookieName() + "=" + testSession}}
	conn, _, err := websocket.Dial(ctx, "ws://"+b.local+"/socket", &websocket.DialOptions{HTTPHeader: header})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	conn.SetReadLimit(1 << 20) // as the server's; a browser has no limit, the library's default is 32 KiB
	var id uint64
	var events []rpc.ClientEvent
	read := func() map[string]json.RawMessage {
		t.Helper()
		var frame map[string]json.RawMessage
		if err := wsjson.Read(ctx, conn, &frame); err != nil {
			t.Fatal(err)
		}
		if string(frame["method"]) == `"session.event"` {
			var event rpc.ClientEvent
			if err := json.Unmarshal(frame["params"], &event); err != nil {
				t.Fatal(err)
			}
			events = append(events, event)
		}
		return frame
	}
	call := func(method string, params any, out any, wantError bool) {
		t.Helper()
		id++
		if err := wsjson.Write(ctx, conn, map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params}); err != nil {
			t.Fatal(err)
		}
		for {
			frame := read()
			if len(frame["id"]) == 0 {
				continue
			}
			if (len(frame["error"]) > 0) != wantError {
				t.Fatalf("%s: %s", method, frame)
			}
			if out != nil {
				if err := json.Unmarshal(frame["result"], out); err != nil {
					t.Fatal(err)
				}
			}
			return
		}
	}
	var hello rpc.HelloResult
	call("hello", map[string]any{"protocol": 1}, &hello, false)
	if !slices.Contains(hello.Methods, "session.send") || !slices.Contains(hello.Methods, "session.secret") {
		t.Fatal(hello.Methods)
	}
	for _, method := range []string{"session.eval"} {
		call(method, map[string]any{}, nil, true)
	}
	repo := filepath.Join(root, "repo")
	if err := os.Mkdir(repo, 0700); err != nil {
		t.Fatal(err)
	}
	var info rpc.SessionDetails
	call("session.open", map[string]any{"repo": repo}, &info, false)
	p := map[string]any{"id": info.ID}
	call("session.subscribe", p, nil, false)
	call("session.send", map[string]any{"id": info.ID, "text": "hello browser"}, nil, false)
	has := func(kind, content string) bool {
		for _, event := range events {
			if event.Kind == kind && strings.Contains(string(event.Payload), content) {
				return true
			}
		}
		return false
	}
	for !has("status", "idle") {
		read()
	}
	if !has("user.msg", "hello browser") || !has("llm.resp", "browser reply") {
		t.Fatal(events)
	}
	terminal, err := rpc.Dial(ctx, socket)
	if err != nil {
		t.Fatal(err)
	}
	defer terminal.Close()
	next := func(kind string) rpc.ClientEvent {
		t.Helper()
		for {
			for i, event := range events {
				if event.Kind == kind {
					events = events[i+1:]
					return event
				}
			}
			read()
		}
	}
	os.MkdirAll(filepath.Join(repo, "docs"), 0700)
	os.WriteFile(filepath.Join(repo, "docs", "friction.md"), []byte("Browser friction fixture\nAKIAABCDEFGHIJKLMNOP\n"), 0600)
	var inspection struct {
		OperationID string `json:"operation_id"`
	}
	call("session.friction", p, &inspection, false)
	var friction event.Inspection
	if err := json.Unmarshal(next(event.InspectionResult).Payload, &friction); err != nil {
		t.Fatal(err)
	}
	if friction.ID != inspection.OperationID || friction.Err != "" || !strings.Contains(friction.Text, "[REDACTED aws-access-key]") {
		t.Fatalf("unsafe or missing inspection: %+v", friction)
	}
	for !strings.Contains(string(next("status").Payload), "idle") {
	}
	checkBrowserPrompts(t, terminal, ctx, info.ID, repo, store, call, next)
	model.Script(fake.Turn{ToolCalls: []fake.ToolCall{
		{ID: "list1", Name: "fs-list", Args: map[string]any{"path": "."}},
		{ID: "list2", Name: "fs-list", Args: map[string]any{"path": "."}},
	}}, fake.Turn{Content: "checkpoint complete"})
	call("session.send", map[string]any{"id": info.ID, "text": "test checkpoint"}, nil, false)
	var checkpoint event.Checkpoint
	if err := json.Unmarshal(next("prompt.checkpoint").Payload, &checkpoint); err != nil {
		t.Fatal(err)
	}
	if checkpoint.Checkpoint.Used != 1 {
		t.Fatal(checkpoint)
	}
	call("session.answer", map[string]any{"id": info.ID, "prompt_id": checkpoint.ID, "reply": "4"}, nil, true)
	call("session.answer", map[string]any{"id": info.ID, "prompt_id": checkpoint.ID, "reply": "3"}, nil, false)
	if raw := next("turn.checkpoint").Payload; !strings.Contains(string(raw), `"soft":3`) || !strings.Contains(string(raw), `unix:uid=`) {
		t.Fatal(string(raw))
	}
	for {
		raw := next("status").Payload
		if strings.Contains(string(raw), "idle") {
			break
		}
	}
	call("session.info", p, &info, false)
	if info.TurnWrites || len(info.Writes) != 1 {
		t.Fatal("budget answer widened authority")
	}
	call("session.abort", p, nil, false)
	call("session.close", p, nil, false)
	call("session.info", p, nil, true)
	call("session.resume", p, &info, false)
	if info.Busy || len(info.Writes) != 0 || info.TurnWrites {
		t.Fatal("resume restored busy state or transient authority")
	}
	var page struct {
		Events []json.RawMessage `json:"events"`
	}
	call("log.read", p, &page, false)
	raw, _ := json.Marshal(page)
	if !strings.Contains(string(raw), "hello browser") || !strings.Contains(string(raw), "browser reply") {
		t.Fatal(string(raw))
	}
}

func TestEmbeddedAssetsReachable(t *testing.T) {
	b := &bridge{ctx: context.Background(), code: testCode, session: testSession}
	b.listensAt("127.0.0.1:34567")
	var paths []string
	err := fs.WalkDir(assets, "assets", func(name string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			paths = append(paths, strings.TrimPrefix(name, "assets"))
		}
		return err
	})
	if err != nil || len(paths) == 0 {
		t.Fatal(err)
	}
	for _, path := range paths {
		if path == "/index.html" {
			path = "/"
		}
		req := httptest.NewRequest("GET", "http://"+b.local+path, nil)
		response := httptest.NewRecorder()
		b.ServeHTTP(response, req)
		if response.Code != http.StatusOK || response.Body.Len() == 0 {
			t.Fatalf("embedded asset %s returned %d", path, response.Code)
		}
	}
	for _, path := range []string{"/fonts", "/../server.go", "/missing.js", "/fonts/"} {
		req := httptest.NewRequest("GET", "http://"+b.local+path, nil)
		response := httptest.NewRecorder()
		b.ServeHTTP(response, req)
		if response.Code != http.StatusNotFound {
			t.Fatalf("%s returned %d", path, response.Code)
		}
	}
}

func TestLaunchTicketPairsOnceAndExpires(t *testing.T) {
	s := New(Options{PairingCode: testCode})
	s.b.ctx = context.Background()
	s.b.listensAt("127.0.0.1:34567")
	post := func(body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "http://127.0.0.1:34567/pair", strings.NewReader(body))
		r.RemoteAddr = "127.0.0.1:50000"
		r.Header.Set("Origin", "http://127.0.0.1:34567")
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		s.b.ServeHTTP(w, r)
		return w
	}
	ticket, err := s.Ticket()
	if err != nil {
		t.Fatal(err)
	}
	if w := post(`{"ticket":"` + ticket + `"}`); w.Code != http.StatusNoContent || len(w.Result().Cookies()) != 1 {
		t.Fatalf("first use: %d %s", w.Code, w.Body.String())
	}
	if w := post(`{"ticket":"` + ticket + `"}`); w.Code != http.StatusForbidden {
		t.Fatalf("reused ticket: %d", w.Code)
	}
	if w := post(`{"ticket":"forged"}`); w.Code != http.StatusForbidden {
		t.Fatalf("forged ticket: %d", w.Code)
	}
	expired, _ := s.Ticket()
	s.b.mu.Lock()
	s.b.tickets[expired] = time.Now().Add(-time.Second)
	s.b.mu.Unlock()
	if w := post(`{"ticket":"` + expired + `"}`); w.Code != http.StatusForbidden {
		t.Fatalf("expired ticket: %d", w.Code)
	}
	if w := post(`{"ticket":"x","extra":1}`); w.Code != http.StatusForbidden {
		t.Fatalf("unknown field: %d", w.Code)
	}
}

func TestRemoteDevicesPairByNameAndCannotWidenPolicy(t *testing.T) {
	devices := OpenDevices(filepath.Join(t.TempDir(), "devices.toml"))
	s := New(Options{PairingCode: testCode, Devices: devices, Hosts: []string{"desk.tailnet.ts.net:7777"}})
	s.b.ctx = context.Background()
	s.b.listensAt("100.64.0.5:7777")
	s.b.hosts["desk.tailnet.ts.net:7777"] = true
	request := func(method, host, path, body, cookie string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "http://"+host+path, strings.NewReader(body))
		r.RemoteAddr = "100.64.0.9:40000"
		r.Header.Set("Origin", "http://"+host)
		r.Header.Set("Content-Type", "application/json")
		if cookie != "" {
			r.Header.Set("Cookie", cookie)
		}
		w := httptest.NewRecorder()
		s.b.ServeHTTP(w, r)
		return w
	}
	host := "desk.tailnet.ts.net:7777"
	if w := request("POST", host, "/pair", `{"code":"`+testCode+`"}`, ""); w.Code != http.StatusBadRequest {
		t.Fatalf("unnamed remote pairing: %d %s", w.Code, w.Body)
	}
	ticket, _ := s.Ticket()
	if w := request("POST", host, "/pair", `{"ticket":"`+ticket+`"}`, ""); w.Code != http.StatusForbidden {
		t.Fatalf("remote ticket: %d", w.Code)
	}
	if w := request("POST", host, "/pair", `{"code":"wrong","device":"phone"}`, ""); w.Code != http.StatusForbidden {
		t.Fatalf("wrong code: %d", w.Code)
	}
	if w := request("POST", host, "/pair", `{"code":"`+testCode+`","device":"Phone Of Mine"}`, ""); w.Code != http.StatusBadRequest {
		t.Fatalf("bad name: %d", w.Code)
	}
	w := request("POST", host, "/pair", `{"code":"`+testCode+`","device":"phone"}`, "")
	cookies := w.Result().Cookies()
	if w.Code != http.StatusNoContent || len(cookies) != 1 || cookies[0].Name != deviceCookie || !cookies[0].HttpOnly || cookies[0].MaxAge <= 0 {
		t.Fatalf("pairing: %d %v", w.Code, cookies)
	}
	raw, _ := os.ReadFile(devices.path)
	if strings.Contains(string(raw), cookies[0].Value) || !strings.Contains(string(raw), `name = "phone"`) {
		t.Fatalf("device file:\n%s", raw)
	}
	r := httptest.NewRequest("GET", "http://"+host+"/socket", nil)
	r.RemoteAddr = "100.64.0.9:40000"
	r.Header.Set("Cookie", deviceCookie+"="+cookies[0].Value)
	if name, ok := s.b.authenticated(r); !ok || name != "phone" {
		t.Fatalf("device cookie: %q %v", name, ok)
	}
	// The local cookie means nothing from another machine.
	r.Header.Set("Cookie", s.b.cookieName()+"="+s.b.session)
	if _, ok := s.b.authenticated(r); ok {
		t.Fatal("local cookie accepted remotely")
	}
	// Even arriving from loopback with this console's own address (a proxy
	// that keeps the Host header), a device is remote.
	local := httptest.NewRequest("GET", "http://100.64.0.5:7777/socket", nil)
	local.RemoteAddr = "127.0.0.1:40000"
	local.Host = "100.64.0.5:7777"
	local.Header.Set("Cookie", deviceCookie+"="+cookies[0].Value)
	if name, ok := s.b.authenticated(local); !ok || !s.b.remoteFor(local, name) {
		t.Fatal("a device counted as this machine")
	}
	if err := devices.Revoke("phone"); err != nil {
		t.Fatal(err)
	}
	r.Header.Set("Cookie", deviceCookie+"="+cookies[0].Value)
	if _, ok := s.b.authenticated(r); ok {
		t.Fatal("revoked device still paired")
	}
	for _, method := range []string{"session.binding.create", "session.policy.write", "keychain.unlock", "keychain.create", "settings.control.write", "settings.docs.write", "settings.write", "devices.revoke", "devices.list", "session.eval", "mcp.save", "mcp.approve", "mcp.inspect", "mcp.pull",
		"keychain.passphrase", "credentials.add", "credentials.replace", "credentials.remove", "credentials.bind", "credentials.unbind", "credentials.authorize", "session.secret", "session.delete", "session.model.set", "session.limits.set"} {
		if s.b.allowed(method, true) {
			t.Fatalf("remote device may call %s", method)
		}
	}
	// ... and this computer may: choosing a conversation's model is its
	// act (ADR 30, ADR 40), so the refusal above is a boundary, not a
	// method the bridge never allowed anyone.
	for _, method := range []string{"session.model.set", "session.approve_all", "settings.control.write", "session.limits.set"} {
		if !s.b.allowed(method, false) {
			t.Fatalf("this computer may not call %s", method)
		}
	}
	// A device may start a task in a worktree, as it may start one (ADR 34);
	// a console that cannot reach worktree.open shows no way to ask for one.
	for _, method := range []string{"session.answer", "session.approval.create", "session.approve_all", "session.send", "session.abort", "attention.list", "mcp.list", "mcp.runtime", "credentials.list", "session.rename", "session.archive", "session.open", "worktree.open", "worktree.diff", "worktree.review"} {
		if !s.b.allowed(method, true) {
			t.Fatalf("remote device may not call %s", method)
		}
	}
	// Reading a review's diff changes nothing, so it stays open to a
	// read-only console; sending the review is a message and does not.
	if !readOnly("worktree.diff") || readOnly("worktree.review") {
		t.Fatal("worktree.diff must be read-only and worktree.review must not be")
	}
	// A non-loopback console without device pairing refuses to start.
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	if err := New(Options{PairingCode: testCode, Hosts: []string{"x:1"}}).Serve(context.Background(), listener); err == nil {
		t.Fatal("served extra hosts without device pairing")
	}
}

func TestUploadAttachesThroughTheBridgeOnly(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	root, err := os.MkdirTemp("/tmp", "nodwyn-web-attach-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)
	socket := filepath.Join(root, "run", "sock")
	listener, err := rpc.Listen(socket)
	if err != nil {
		t.Fatal(err)
	}
	paths := config.PathsUnder(filepath.Join(root, "config"), filepath.Join(root, "data"))
	done := make(chan error, 1)
	go func() { done <- rpc.Serve(ctx, listener, core.Options{Config: config.Default(), Paths: paths}) }()
	defer func() {
		cancel()
		<-done
	}()
	repo := filepath.Join(root, "work")
	os.MkdirAll(repo, 0o755)
	client, err := rpc.Dial(ctx, socket)
	if err != nil {
		t.Fatal(err)
	}
	var details struct {
		ID string `json:"id"`
	}
	if err := client.Call(ctx, "session.open", map[string]string{"repo": repo}, &details); err != nil {
		t.Fatal(err)
	}
	client.Close()
	b := &bridge{ctx: ctx, socket: socket, code: testCode, session: testSession}
	server := httptest.NewUnstartedServer(b)
	b.listensAt(server.Listener.Addr().String())
	server.Start()
	defer server.Close()
	upload := func(name, body string, cookie bool) (int, string) {
		req, _ := http.NewRequest("POST", server.URL+"/attach?id="+details.ID+"&name="+name, strings.NewReader(body))
		req.Header.Set("Origin", server.URL)
		if cookie {
			req.Header.Set("Cookie", b.cookieName()+"="+testSession)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(raw)
	}
	if code, _ := upload("notes.md", "# Notes", false); code != http.StatusForbidden {
		t.Fatalf("unpaired upload: %d", code)
	}
	code, body := upload("notes.md", "# Notes\nhello", true)
	if code != http.StatusOK || !strings.Contains(body, `"name":"notes.md"`) || strings.Contains(body, "hello") {
		t.Fatalf("upload: %d %s", code, body)
	}
	if code, body := upload("photo.png", "\x89PNG", true); code != http.StatusUnprocessableEntity || !strings.Contains(body, "not a PNG image") {
		t.Fatalf("image upload: %d %s", code, body)
	}
	// An image needs a control model that takes images; this daemon has none.
	if code, body := upload("dot.gif", "GIF89a\x02\x00\x02\x00\x00\x00\x00;", true); code != http.StatusUnprocessableEntity || !strings.Contains(body, "no control model is configured") {
		t.Fatalf("image upload without a vision model: %d %s", code, body)
	}
	matches, _ := filepath.Glob(filepath.Join(repo, ".nodwyn", "attachments", "*", "notes.md"))
	if len(matches) != 1 {
		t.Fatalf("stored copies: %v", matches)
	}
	// A page cannot name a path for the daemon to read.
	if b.allowed("session.attach", false) {
		t.Fatal("session.attach reachable from the page")
	}
}

// Revoking a device ends its open console: before its next request, and
// within deviceRecheck when it is only watching.
func TestRevokingADeviceClosesItsOpenConsole(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	root, err := os.MkdirTemp("/tmp", "nodwyn-web-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(root) })
	socket := filepath.Join(root, "run", "sock")
	listener, err := rpc.Listen(socket)
	if err != nil {
		t.Fatal(err)
	}
	paths := config.PathsUnder(filepath.Join(root, "config"), filepath.Join(root, "data"))
	done := make(chan error, 1)
	go func() { done <- rpc.Serve(ctx, listener, core.Options{Config: config.Default(), Paths: paths}) }()
	defer func() { cancel(); <-done }()

	devices := OpenDevices(filepath.Join(root, "devices.toml"))
	b := &bridge{ctx: ctx, socket: socket, code: testCode, session: testSession, devices: devices, recheck: 50 * time.Millisecond}
	server := httptest.NewUnstartedServer(b)
	b.listensAt(server.Listener.Addr().String())
	server.Start()
	defer server.Close()
	open := func(name string) *websocket.Conn {
		t.Helper()
		token, err := devices.Pair(name)
		if err != nil {
			t.Fatal(err)
		}
		header := http.Header{"Origin": {server.URL}, "Cookie": {deviceCookie + "=" + token}}
		conn, _, err := websocket.Dial(ctx, "ws://"+b.local+"/socket", &websocket.DialOptions{HTTPHeader: header})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { conn.CloseNow() })
		return conn
	}
	closed := func(conn *websocket.Conn, send bool) bool {
		t.Helper()
		if send {
			if err := wsjson.Write(ctx, conn, map[string]any{"jsonrpc": "2.0", "id": 1, "method": "attention.list", "params": map[string]any{}}); err != nil {
				return true
			}
		}
		readCtx, stop := context.WithTimeout(ctx, 2*time.Second)
		defer stop()
		var reply map[string]json.RawMessage
		err := wsjson.Read(readCtx, conn, &reply)
		return websocket.CloseStatus(err) == websocket.StatusPolicyViolation
	}
	asking := open("phone")
	if closed(asking, true) {
		t.Fatal("a paired device was refused")
	}
	watching := open("tablet")
	devices.Revoke("phone")
	devices.Revoke("tablet")
	if !closed(asking, true) {
		t.Fatal("a revoked device's request was relayed")
	}
	if !closed(watching, false) {
		t.Fatal("a revoked device's watching console stayed open")
	}
}

// Pairing this computer's browser gives it the run's session, not the code:
// the browser sends the cookie to every server on the loopback address, and
// the code can pair a device for months. A cookie holding the code is no
// pairing.
func TestLocalCookieHoldsTheSessionNotTheCode(t *testing.T) {
	b := &bridge{ctx: context.Background(), code: testCode, session: testSession}
	b.listensAt("127.0.0.1:34567")
	r := httptest.NewRequest("POST", "http://127.0.0.1:34567/pair", strings.NewReader(`{"code":"`+testCode+`"}`))
	r.RemoteAddr = "127.0.0.1:50000"
	r.Header.Set("Origin", "http://127.0.0.1:34567")
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	b.ServeHTTP(w, r)
	cookies := w.Result().Cookies()
	if w.Code != 204 || len(cookies) != 1 || cookies[0].Value == testCode || cookies[0].Value != testSession {
		t.Fatalf("pairing set %d %+v", w.Code, cookies)
	}
	r = httptest.NewRequest("GET", "http://127.0.0.1:34567/socket", nil)
	r.RemoteAddr = "127.0.0.1:50000"
	r.Header.Set("Cookie", b.cookieName()+"="+testCode)
	if _, ok := b.authenticated(r); ok {
		t.Fatal("a cookie holding the pairing code was taken as paired")
	}
}

// With no daemon to relay to, the socket opens and closes saying so (1013):
// a refused handshake was all the browser learned, which the console reads
// as a browser that is not paired, and it showed the pairing form.
func TestSocketSaysTheDaemonIsNotRunning(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	b := &bridge{ctx: ctx, socket: filepath.Join(t.TempDir(), "no-daemon.sock"), code: testCode, session: testSession}
	server := httptest.NewUnstartedServer(b)
	b.listensAt(server.Listener.Addr().String())
	server.Start()
	defer server.Close()
	header := http.Header{"Origin": {server.URL}, "Cookie": {b.cookieName() + "=" + testSession}}
	conn, _, err := websocket.Dial(ctx, "ws://"+b.local+"/socket", &websocket.DialOptions{HTTPHeader: header})
	if err != nil {
		t.Fatalf("the socket was refused: %v", err)
	}
	defer conn.CloseNow()
	_, _, err = conn.Read(ctx)
	if status := websocket.CloseStatus(err); status != websocket.StatusTryAgainLater {
		t.Fatalf("closed with %v (%v)", status, err)
	}
}

// A socket refused before it opened tells the console nothing of why, so it
// asks /paired whether the console takes it: one revoked, or paired with an
// earlier run of the console, pairs again, and only one that cannot reach
// the console keeps trying to.
func TestPairedSaysWhetherTheConsoleTakesThisBrowser(t *testing.T) {
	devices := OpenDevices(filepath.Join(t.TempDir(), "devices.toml"))
	phone, err := devices.Pair("phone")
	if err != nil {
		t.Fatal(err)
	}
	tablet, err := devices.Pair("tablet")
	if err != nil {
		t.Fatal(err)
	}
	if err := devices.Revoke("tablet"); err != nil {
		t.Fatal(err)
	}
	b := &bridge{ctx: context.Background(), code: testCode, session: testSession, devices: devices, extra: []string{"desk.test:34567"}}
	b.listensAt("127.0.0.1:34567")
	for _, tt := range []struct {
		name, host, from, cookie, site string
		status                         int
		body                           string
	}{
		{"this computer", "127.0.0.1:34567", "127.0.0.1:50000", b.cookieName() + "=" + testSession, "same-origin", 200, `{"paired":true}`},
		{"an earlier run", "127.0.0.1:34567", "127.0.0.1:50000", b.cookieName() + "=test-only-an-earlier-run-000000000000000", "same-origin", 200, `{"paired":false}`},
		{"never paired", "127.0.0.1:34567", "127.0.0.1:50000", "", "same-origin", 200, `{"paired":false}`},
		{"a paired device", "desk.test:34567", "100.101.102.103:50000", deviceCookie + "=" + phone, "same-origin", 200, `{"paired":true}`},
		{"a revoked device", "desk.test:34567", "100.101.102.103:50000", deviceCookie + "=" + tablet, "same-origin", 200, `{"paired":false}`},
		{"this computer's cookie from another", "desk.test:34567", "100.101.102.103:50000", b.cookieName() + "=" + testSession, "same-origin", 200, `{"paired":false}`},
		{"another site", "127.0.0.1:34567", "127.0.0.1:50000", b.cookieName() + "=" + testSession, "cross-site", 403, ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", "http://"+tt.host+"/paired", nil)
			r.RemoteAddr = tt.from
			r.Header.Set("Sec-Fetch-Site", tt.site)
			if tt.cookie != "" {
				r.Header.Set("Cookie", tt.cookie)
			}
			w := httptest.NewRecorder()
			b.ServeHTTP(w, r)
			if w.Code != tt.status || (tt.body != "" && strings.TrimSpace(w.Body.String()) != tt.body) {
				t.Fatalf("%d %s, want %d %s", w.Code, w.Body.String(), tt.status, tt.body)
			}
		})
	}
}

// The bridge pings an open console: a proxy in front of it (tailscale serve)
// may close a connection that carries nothing for a while, and a browser
// that slept or lost its network leaves one open, holding a daemon
// connection, with nobody at the other end. One that answers the pings stays
// open; one that does not is closed.
func TestASilentConsoleIsPingedAndClosed(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	root, err := os.MkdirTemp("/tmp", "nodwyn-web-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(root) })
	socket := filepath.Join(root, "run", "sock")
	listener, err := rpc.Listen(socket)
	if err != nil {
		t.Fatal(err)
	}
	paths := config.PathsUnder(filepath.Join(root, "config"), filepath.Join(root, "data"))
	done := make(chan error, 1)
	go func() { done <- rpc.Serve(ctx, listener, core.Options{Config: config.Default(), Paths: paths}) }()
	defer func() { cancel(); <-done }()

	const every = 200 * time.Millisecond
	b := &bridge{ctx: ctx, socket: socket, code: testCode, session: testSession, ping: every}
	server := httptest.NewUnstartedServer(b)
	b.listensAt(server.Listener.Addr().String())
	server.Start()
	defer server.Close()
	open := func() *websocket.Conn {
		t.Helper()
		header := http.Header{"Origin": {server.URL}, "Cookie": {b.cookieName() + "=" + testSession}}
		conn, _, err := websocket.Dial(ctx, "ws://"+b.local+"/socket", &websocket.DialOptions{HTTPHeader: header})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { conn.CloseNow() })
		return conn
	}
	// A browser reads all the time, which answers the pings.
	answering := open()
	replies := make(chan error, 8)
	go func() {
		for {
			var reply map[string]json.RawMessage
			if err := wsjson.Read(ctx, answering, &reply); err != nil {
				replies <- err
				return
			}
			replies <- nil
		}
	}()
	silent := open()
	time.Sleep(6 * every)
	select {
	case err := <-replies:
		t.Fatalf("the console that answers the pings got %v before it asked anything", err)
	default:
	}
	if err := wsjson.Write(ctx, answering, map[string]any{"jsonrpc": "2.0", "id": 1, "method": "attention.list", "params": map[string]any{}}); err != nil {
		t.Fatal(err)
	}
	if err := <-replies; err != nil {
		t.Fatalf("the console that answers the pings was closed: %v", err)
	}
	readCtx, stop := context.WithTimeout(ctx, 2*time.Second)
	defer stop()
	if _, _, err := silent.Read(readCtx); err == nil || errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("the console that answered no ping is still open (%v)", err)
	}
}

// A slow call (a model's test, an image pull) does not hold up the
// console's next requests: the daemon answers a connection one request at
// a time, so the bridge relays it on a connection of its own. A task's
// abort sent while a pull ran used to wait for the pull.
func TestASlowCallHoldsUpNothingElse(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	root, err := os.MkdirTemp("/tmp", "nodwyn-web-slow-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)
	socket := filepath.Join(root, "run", "sock")
	listener, err := rpc.Listen(socket)
	if err != nil {
		t.Fatal(err)
	}
	release := make(chan struct{})
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
		http.NotFound(w, r)
	}))
	defer slow.Close()
	cfg := config.Default()
	cfg.Control.URL, cfg.Control.Model = slow.URL, "test-model"
	paths := config.PathsUnder(filepath.Join(root, "config"), filepath.Join(root, "data"))
	done := make(chan error, 1)
	go func() {
		done <- rpc.Serve(ctx, listener, core.Options{Config: cfg, Paths: paths, Provider: llm.NewOllama(slow.URL)})
	}()
	// The model is let go first: the daemon waits for its test to end.
	defer func() { close(release); cancel(); <-done }()
	b := &bridge{ctx: ctx, socket: socket, code: testCode, session: testSession}
	server := httptest.NewUnstartedServer(b)
	b.listensAt(server.Listener.Addr().String())
	server.Start()
	defer server.Close()
	header := http.Header{"Origin": {server.URL}, "Cookie": {b.cookieName() + "=" + testSession}}
	conn, _, err := websocket.Dial(ctx, "ws://"+b.local+"/socket", &websocket.DialOptions{HTTPHeader: header})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	for id, method := range []string{"models.test", "session.list"} {
		if err := wsjson.Write(ctx, conn, map[string]any{"jsonrpc": "2.0", "id": id + 1, "method": method, "params": map[string]any{}}); err != nil {
			t.Fatal(err)
		}
	}
	for {
		var frame map[string]json.RawMessage
		if err := wsjson.Read(ctx, conn, &frame); err != nil {
			t.Fatalf("no answer to session.list while the test runs: %v", err)
		}
		if string(frame["method"]) == `"session.event"` {
			continue
		}
		if string(frame["id"]) != "2" {
			t.Fatalf("the slow call held up the next: %s", frame["id"])
		}
		return
	}
}

// Every method the daemon advertises is classified by the bridge: read-only,
// or allowed to this computer (and to a device or not). One added to the
// daemon and not to the bridge is refused to everyone, silently (P2-080).
func TestEveryAdvertisedMethodIsClassifiedByTheBridge(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	root, err := os.MkdirTemp("/tmp", "nodwyn-web-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(root) })
	socket := filepath.Join(root, "run", "sock")
	listener, err := rpc.Listen(socket)
	if err != nil {
		t.Fatal(err)
	}
	paths := config.PathsUnder(filepath.Join(root, "config"), filepath.Join(root, "data"))
	done := make(chan error, 1)
	go func() { done <- rpc.Serve(ctx, listener, core.Options{Config: config.Default(), Paths: paths}) }()
	defer func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()
	client, err := rpc.Dial(ctx, socket)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	var hello rpc.HelloResult
	if err := client.Call(ctx, "hello", map[string]any{"protocol": 1}, &hello); err != nil {
		t.Fatal(err)
	}
	// What the console never calls: a run is the terminal's (nodwyn run),
	// :eval is line mode's, and a file attached from a page goes through
	// POST /attach, which stages it, never through a path the page names.
	terminalOnly := []string{"run.start", "run.info", "run.stop", "run.resume", "session.eval", "session.attach"}
	b := &bridge{}
	for _, method := range hello.Methods {
		switch allowed := b.allowed(method, false); {
		case slices.Contains(terminalOnly, method) && allowed:
			t.Errorf("%s is allowed by the bridge, but this test lists it as never offered to a browser", method)
		case !slices.Contains(terminalOnly, method) && !allowed:
			t.Errorf("%s is advertised by the daemon, but the bridge refuses it even on this computer: add it to readOnly or to allowed, or to terminalOnly here", method)
		}
	}
}

// A proxy that ends TLS in front of the console (tailscale serve) makes the
// browser's origin https while the console speaks http: a name given as
// https://NAME is answered with that origin, and its device cookie is Secure
// (P2-085). Names given plain keep their http origin, and a refused origin
// says which was expected.
func TestAHostBehindATLSProxyPairsOverHTTPS(t *testing.T) {
	devices := OpenDevices(filepath.Join(t.TempDir(), "devices.toml"))
	s := New(Options{PairingCode: testCode, Devices: devices, Hosts: []string{"https://desk.tailnet.ts.net", "desk.local:7777"}})
	s.b.ctx = context.Background()
	s.b.listensAt("127.0.0.1:7777")
	pair := func(host, origin string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "http://"+host+"/pair", strings.NewReader(`{"code":"`+testCode+`","device":"phone"}`))
		r.RemoteAddr = "127.0.0.1:40000" // the proxy, on the phone's behalf
		r.Header.Set("Origin", origin)
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		s.b.ServeHTTP(w, r)
		return w
	}
	w := pair("desk.tailnet.ts.net", "https://desk.tailnet.ts.net")
	cookies := w.Result().Cookies()
	if w.Code != http.StatusNoContent || len(cookies) != 1 || !cookies[0].Secure {
		t.Fatalf("pairing through the proxy: %d %s %v", w.Code, w.Body, cookies)
	}
	if w := pair("desk.tailnet.ts.net", "http://desk.tailnet.ts.net"); w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "expected https://desk.tailnet.ts.net") {
		t.Fatalf("an http origin for the https name: %d %s", w.Code, w.Body)
	}
	if w := pair("desk.local:7777", "http://desk.local:7777"); w.Code != http.StatusNoContent {
		t.Fatalf("a plain name keeps its http origin: %d %s", w.Code, w.Body)
	}
	if w := pair("desk.local:7777", "https://desk.local:7777"); w.Code != http.StatusForbidden {
		t.Fatalf("a plain name does not take an https origin: %d", w.Code)
	}
}

// A message larger than the daemon takes is answered with an error and the
// connection stays: the console used to be dropped, and every pending call
// with it, by one pasted log (P2-086).
func TestAnOversizedMessageIsAnsweredNotFatal(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	root, err := os.MkdirTemp("/tmp", "nodwyn-web-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(root) })
	socket := filepath.Join(root, "run", "sock")
	listener, err := rpc.Listen(socket)
	if err != nil {
		t.Fatal(err)
	}
	paths := config.PathsUnder(filepath.Join(root, "config"), filepath.Join(root, "data"))
	done := make(chan error, 1)
	go func() { done <- rpc.Serve(ctx, listener, core.Options{Config: config.Default(), Paths: paths}) }()
	defer func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()
	b := &bridge{ctx: ctx, socket: socket, code: testCode, session: testSession}
	server := httptest.NewUnstartedServer(b)
	b.listensAt(server.Listener.Addr().String())
	server.Start()
	defer server.Close()
	header := http.Header{"Origin": {server.URL}, "Cookie": {b.cookieName() + "=" + testSession}}
	conn, _, err := websocket.Dial(ctx, "ws://"+b.local+"/socket", &websocket.DialOptions{HTTPHeader: header})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	conn.SetReadLimit(1 << 20)
	call := func(id uint64, method string, params any) map[string]json.RawMessage {
		t.Helper()
		if err := wsjson.Write(ctx, conn, map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params}); err != nil {
			t.Fatal(err)
		}
		rctx, stop := context.WithTimeout(ctx, 5*time.Second)
		defer stop()
		var frame map[string]json.RawMessage
		if err := wsjson.Read(rctx, conn, &frame); err != nil {
			t.Fatalf("the connection was dropped: %v", err)
		}
		return frame
	}
	call(1, "hello", map[string]any{"protocol": 1})
	big := strings.Repeat("a", 1200<<10)
	if raw := call(2, "session.send", map[string]any{"id": "x", "text": big}); !strings.Contains(string(raw["error"]), "too large") {
		t.Fatalf("an oversized message was answered with %s", raw["error"])
	}
	if raw := call(3, "session.list", map[string]any{}); len(raw["result"]) == 0 {
		t.Fatalf("the connection is not usable after it: %s", raw["error"])
	}
}
