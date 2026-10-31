package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image/png"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
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
		{"pair", "POST", "/pair", "127.0.0.1:34567", "http://127.0.0.1:34567", "same-origin", `{"code":"` + testCode + `"}`, 200},
		{"not-signed-in", "GET", "/socket", "127.0.0.1:34567", "http://127.0.0.1:34567", "same-origin", "", 403},
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
			// The page that paired keeps the run's session itself: no
			// cookie, which every server on loopback would be sent.
			if tt.name == "pair" && (len(w.Result().Cookies()) != 0 || !strings.Contains(w.Body.String(), `"session":"`+testSession+`"`)) {
				t.Fatal(w.Result().Cookies(), w.Body.String())
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
	header := http.Header{"Origin": {server.URL}, "Authorization": {"Bearer " + testSession}}
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
	header := http.Header{"Origin": {server.URL}, "Authorization": {"Bearer " + testSession}}
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
	}}, fake.Turn{Content: "never asked"})
	// The console sets a task's limits through the bridge (ADR 49, amended
	// 2026-10-27), and the turn stops at one without asking.
	call("session.limits.set", map[string]any{"id": info.ID, "limits": map[string]any{"tool_calls": 1}}, nil, false)
	if raw := next("session.limits").Payload; !strings.Contains(string(raw), `"tool_calls":1`) || !strings.Contains(string(raw), `unix:uid=`) {
		t.Fatal(string(raw))
	}
	call("session.send", map[string]any{"id": info.ID, "text": "list twice"}, nil, false)
	if raw := next("turn.end").Payload; !strings.Contains(string(raw), `"hard_budget"`) {
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
		t.Fatal("a limit widened authority")
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

// The console installs as an app (ADR 52): the page links a manifest that
// the bridge serves to whoever reaches it, as it serves the page, with its
// icons at the sizes and types it names, and a service worker that handles
// no fetch, so nothing the console shows is ever cached.
func TestTheConsoleInstallsAsAnApp(t *testing.T) {
	b := &bridge{ctx: context.Background(), code: testCode, session: testSession}
	b.listensAt("127.0.0.1:34567")
	get := func(path string) *httptest.ResponseRecorder {
		t.Helper()
		response := httptest.NewRecorder()
		b.ServeHTTP(response, httptest.NewRequest("GET", "http://"+b.local+path, nil))
		if response.Code != http.StatusOK {
			t.Fatalf("%s returned %d", path, response.Code)
		}
		return response
	}
	size := func(path string) string {
		t.Helper()
		image, err := png.DecodeConfig(get(path).Body)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		return fmt.Sprintf("%dx%d", image.Width, image.Height)
	}

	page := get("/").Body.String()
	for _, link := range []string{`rel="manifest" href="/manifest.json"`, `rel="apple-touch-icon" href="/icons/apple-touch-icon.png"`, `name="theme-color"`} {
		if !strings.Contains(page, link) {
			t.Errorf("the page has no %s", link)
		}
	}
	if got := size("/icons/apple-touch-icon.png"); got != "180x180" {
		t.Errorf("the Apple touch icon is %s, not 180x180", got)
	}

	response := get("/manifest.json")
	if kind := response.Header().Get("Content-Type"); !strings.HasPrefix(kind, "application/json") {
		t.Errorf("the manifest is served as %q", kind)
	}
	var manifest struct {
		Name     string `json:"name"`
		StartURL string `json:"start_url"`
		Scope    string `json:"scope"`
		Display  string `json:"display"`
		Icons    []struct {
			Src     string `json:"src"`
			Sizes   string `json:"sizes"`
			Type    string `json:"type"`
			Purpose string `json:"purpose"`
		} `json:"icons"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.Name != "nodwyn" || manifest.StartURL != "/" || manifest.Scope != "/" || manifest.Display != "standalone" {
		t.Errorf("the manifest opens %q at %q in %q, scope %q", manifest.Name, manifest.StartURL, manifest.Display, manifest.Scope)
	}
	sizes := map[string]bool{}
	for _, icon := range manifest.Icons {
		if kind := get(icon.Src).Header().Get("Content-Type"); kind != icon.Type {
			t.Errorf("%s is served as %q, and the manifest says %q", icon.Src, kind, icon.Type)
		}
		if icon.Type == "image/png" {
			if got := size(icon.Src); got != icon.Sizes {
				t.Errorf("%s is %s, and the manifest says %s", icon.Src, got, icon.Sizes)
			}
		}
		sizes[icon.Sizes+" "+icon.Purpose] = true
	}
	for _, want := range []string{"192x192 ", "512x512 ", "512x512 maskable"} {
		if !sizes[want] {
			t.Errorf("the manifest has no %q icon: %v", want, sizes)
		}
	}

	worker := get("/sw.js")
	if kind := worker.Header().Get("Content-Type"); !strings.HasPrefix(kind, "text/javascript") {
		t.Errorf("the service worker is served as %q", kind)
	}
	if body := worker.Body.String(); strings.Contains(body, `"fetch"`) || strings.Contains(body, "caches.") {
		t.Error("the service worker handles fetches or keeps a cache")
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
	if w := post(`{"ticket":"` + ticket + `"}`); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"session":"`+s.b.session+`"`) {
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

func TestRemoteDevicesPairByInvitationAndCannotWidenPolicy(t *testing.T) {
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
	// A console other machines reach takes no pairing code, named or not.
	for _, body := range []string{`{"code":"` + testCode + `"}`, `{"code":"` + testCode + `","device":"phone"}`} {
		if w := request("POST", host, "/pair", body, ""); w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "nodwyn devices invite") {
			t.Fatalf("pairing with the code: %d %s", w.Code, w.Body)
		}
	}
	ticket, _ := s.Ticket()
	if w := request("POST", host, "/pair", `{"ticket":"`+ticket+`"}`, ""); w.Code != http.StatusForbidden {
		t.Fatalf("remote ticket: %d", w.Code)
	}
	code, _, err := devices.Invite(Terminal, false)
	if err != nil {
		t.Fatal(err)
	}
	if w := request("POST", host, "/pair", `{"invite":"`+code+`"}`, ""); w.Code != http.StatusBadRequest {
		t.Fatalf("unnamed remote pairing: %d %s", w.Code, w.Body)
	}
	if w := request("POST", host, "/pair", `{"invite":"wrong","device":"phone"}`, ""); w.Code != http.StatusForbidden {
		t.Fatalf("wrong invitation: %d", w.Code)
	}
	if w := request("POST", host, "/pair", `{"invite":"`+code+`","device":"Phone Of Mine"}`, ""); w.Code != http.StatusBadRequest {
		t.Fatalf("bad name: %d", w.Code)
	}
	// The code as shown, in capitals and with spaces for its dashes, pairs.
	typed := strings.ToUpper(strings.ReplaceAll(code, "-", " "))
	w := request("POST", host, "/pair", `{"invite":"`+typed+`","device":"phone"}`, "")
	cookies := w.Result().Cookies()
	if w.Code != http.StatusNoContent || len(cookies) != 1 || cookies[0].Name != deviceCookie || !cookies[0].HttpOnly || cookies[0].MaxAge != int(idleLife/time.Second) {
		t.Fatalf("pairing: %d %s %v", w.Code, w.Body, cookies)
	}
	if w := request("POST", host, "/pair", `{"invite":"`+code+`","device":"tablet"}`, ""); w.Code != http.StatusForbidden {
		t.Fatalf("an invitation used twice: %d", w.Code)
	}
	raw, _ := os.ReadFile(devices.path)
	if strings.Contains(string(raw), cookies[0].Value) || strings.Contains(string(raw), code) || !strings.Contains(string(raw), `name = "phone"`) {
		t.Fatalf("device file:\n%s", raw)
	}
	r := httptest.NewRequest("GET", "http://"+host+"/socket", nil)
	r.RemoteAddr = "100.64.0.9:40000"
	r.Header.Set("Cookie", deviceCookie+"="+cookies[0].Value)
	if device, ok := s.b.authenticated(r); !ok || device.Name != "phone" || device.Passkey != "" || device.By != Terminal {
		t.Fatalf("device cookie: %+v %v", device, ok)
	}
	// This computer's token means nothing from another machine.
	r.Header.Del("Cookie")
	r.Header.Set("Authorization", "Bearer "+s.b.session)
	if _, ok := s.b.authenticated(r); ok {
		t.Fatal("this computer's token accepted remotely")
	}
	// Even arriving from loopback with this console's own address (a proxy
	// that keeps the Host header), a device is remote.
	local := httptest.NewRequest("GET", "http://100.64.0.5:7777/socket", nil)
	local.RemoteAddr = "127.0.0.1:40000"
	local.Host = "100.64.0.5:7777"
	local.Header.Set("Cookie", deviceCookie+"="+cookies[0].Value)
	if device, ok := s.b.authenticated(local); !ok || !s.b.remoteFor(local, device.Name) {
		t.Fatal("a device counted as this machine")
	}
	if err := devices.Revoke("phone"); err != nil {
		t.Fatal(err)
	}
	r.Header.Set("Cookie", deviceCookie+"="+cookies[0].Value)
	if _, ok := s.b.authenticated(r); ok {
		t.Fatal("revoked device still paired")
	}
	device, person, touched := caller{}, caller{person: true}, caller{person: true, touched: true}
	setup := []string{"session.binding.create", "session.policy.write", "keychain.create", "settings.control.write", "settings.docs.write", "settings.write", "devices.revoke", "passkeys.remove", "mcp.save", "mcp.approve", "mcp.inspect", "mcp.pull",
		"keychain.passphrase", "credentials.add", "credentials.replace", "credentials.remove", "credentials.bind", "credentials.unbind", "credentials.authorize", "session.delete", "workspace.save", "index.forget"}
	choices := []string{"session.model.set", "session.judge.set", "session.commands.set", "schedule.set", "session.secret", "keychain.unlock", "workspace.active", "newtask.last", "devices.list", "passkeys.list"}
	for _, method := range append(append([]string{"session.eval"}, setup...), choices...) {
		if s.b.access(method, device) != refused {
			t.Fatalf("a device that only answers may call %s", method)
		}
	}
	// A device may add a limit to a task or lower one, which the daemon
	// holds it to (ADR 49, amended 2026-10-27), as it may turn Don't ask off.
	for _, method := range []string{"session.limits.set", "session.approve_all"} {
		if s.b.access(method, device) != granted {
			t.Fatalf("a device that answers may not call %s", method)
		}
	}
	// The person's device makes this computer's choices, and its setup
	// actions wait for a touch of the passkey; the eval stays the terminal's.
	for _, method := range choices {
		if s.b.access(method, person) != granted {
			t.Fatalf("the person's device may not call %s", method)
		}
	}
	for _, method := range setup {
		if s.b.access(method, person) != needsTouch || s.b.access(method, touched) != granted {
			t.Fatalf("%s: %v before a touch, %v after", method, s.b.access(method, person), s.b.access(method, touched))
		}
	}
	// Trusting a project's startup code needs the passkey itself: not this
	// computer's cookie, which a task's own dev server on loopback may hold.
	if s.b.access("trust.set", caller{local: true}) != refused || s.b.access("trust.set", device) != refused ||
		s.b.access("trust.set", person) != needsTouch || s.b.access("trust.set", touched) != granted {
		t.Fatal("trust.set is not the passkey's alone")
	}
	if s.b.access("trust.read", caller{local: true}) != granted || s.b.access("trust.read", person) != granted || s.b.access("trust.read", device) != refused {
		t.Fatal("trust.read is not offered to this computer and the person's device")
	}
	if s.b.access("session.eval", touched) != refused {
		t.Fatal("a touched device may evaluate Scheme")
	}
	// ... and this computer may do all of it at once.
	for _, method := range append(setup, choices...) {
		if !s.b.allowed(method, caller{local: true}) {
			t.Fatalf("this computer may not call %s", method)
		}
	}
	// A device may start a task in a worktree, as it may start one (ADR 34);
	// a console that cannot reach worktree.open shows no way to ask for one.
	for _, method := range []string{"session.answer", "session.approval.create", "session.approve_all", "session.send", "session.abort", "attention.list", "mcp.list", "mcp.runtime", "credentials.list", "session.rename", "session.archive", "session.open", "worktree.open", "worktree.diff", "worktree.review", "devices.invite"} {
		if !s.b.allowed(method, device) {
			t.Fatalf("a device may not call %s", method)
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

// On a console other machines reach, the pairing code pairs nothing: not a
// device, and not this computer either, from the console's own loopback
// address, which is what an ssh forward to it delivers. The code was printed
// to the journal of a computer nobody sits at, for as long as the console
// ran, and any account there can reach loopback. A launch ticket, which only
// a launcher on this computer mints, still pairs this computer.
func TestASharedConsolePairsThisComputerOnlyByTicket(t *testing.T) {
	s := New(Options{PairingCode: testCode, Devices: OpenDevices(filepath.Join(t.TempDir(), "devices.toml")), Hosts: []string{"https://desk.example"}})
	s.b.ctx = context.Background()
	s.b.listensAt("127.0.0.1:7777")
	post := func(body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "http://127.0.0.1:7777/pair", strings.NewReader(body))
		r.RemoteAddr = "127.0.0.1:50000"
		r.Header.Set("Origin", "http://127.0.0.1:7777")
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		s.b.ServeHTTP(w, r)
		return w
	}
	if w := post(`{"code":"` + testCode + `"}`); w.Code != http.StatusForbidden {
		t.Fatalf("the code paired this computer on a shared console: %d", w.Code)
	}
	ticket, _ := s.Ticket()
	if w := post(`{"ticket":"` + ticket + `"}`); w.Code != http.StatusOK {
		t.Fatalf("a launch ticket: %d %s", w.Code, w.Body)
	}
	r := httptest.NewRequest("GET", "http://127.0.0.1:7777/pair", nil)
	r.RemoteAddr = "127.0.0.1:50000"
	w := httptest.NewRecorder()
	s.b.ServeHTTP(w, r)
	var info pairingInfo
	if err := json.Unmarshal(w.Body.Bytes(), &info); err != nil || !info.Shared || info.Passkey || info.Passkeys {
		t.Fatalf("pairing info at an address: %s", w.Body)
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
			req.Header.Set("Authorization", "Bearer "+testSession)
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
	if b.allowed("session.attach", caller{local: true}) {
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
		header := http.Header{"Origin": {server.URL}, "Cookie": {deviceCookie + "=" + join(t, devices, name)}}
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

// This computer's page holds its sign-in itself (ADR 30, amended 2026-10-19).
// Pairing with the code or the launch ticket answers with the run's token and
// sets no cookie, which the browser sent to every server on the loopback
// address that it visited; the page sends the token in its requests and among
// its socket's subprotocols, and the bridge takes it there, from this computer
// alone. A cookie carrying it, an earlier run's token and the pairing code are
// no sign-in.
func TestThisComputersSignInIsNoCookie(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	b := &bridge{ctx: ctx, socket: filepath.Join(t.TempDir(), "no-daemon.sock"), code: testCode, session: testSession, tickets: map[string]time.Time{}}
	server := httptest.NewUnstartedServer(b)
	b.listensAt(server.Listener.Addr().String())
	server.Start()
	defer server.Close()
	ticket, err := (&Server{b: b}).Ticket()
	if err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{`{"code":"` + testCode + `"}`, `{"ticket":"` + ticket + `"}`} {
		r := httptest.NewRequest("POST", "http://"+b.local+"/pair", strings.NewReader(body))
		r.RemoteAddr = "127.0.0.1:50000"
		r.Header.Set("Origin", "http://"+b.local)
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		b.ServeHTTP(w, r)
		var paired struct {
			Session string `json:"session"`
		}
		if w.Code != http.StatusOK || len(w.Result().Cookies()) != 0 || json.Unmarshal(w.Body.Bytes(), &paired) != nil || paired.Session != testSession {
			t.Fatalf("pairing with %s: %d %v %s", body, w.Code, w.Result().Cookies(), w.Body)
		}
	}
	signedIn := func(from string, header http.Header) bool {
		t.Helper()
		r := httptest.NewRequest("GET", "http://"+b.local+"/paired", nil)
		r.RemoteAddr = from
		for k, v := range header {
			r.Header[k] = v
		}
		w := httptest.NewRecorder()
		b.ServeHTTP(w, r)
		return strings.TrimSpace(w.Body.String()) == `{"paired":true}`
	}
	here := "127.0.0.1:50000"
	if !signedIn(here, http.Header{"Authorization": {"Bearer " + testSession}}) || !signedIn(here, http.Header{"Sec-Websocket-Protocol": {"nodwyn, nodwyn.session." + testSession}}) {
		t.Fatal("the token the page sends is not taken")
	}
	for name, header := range map[string]http.Header{
		"a cookie holding the token": {"Cookie": {"nodwyn_ui_" + strings.ReplaceAll(b.local, ":", "_") + "=" + testSession}},
		"an earlier run's token":     {"Authorization": {"Bearer test-only-an-earlier-run-000000000000000"}},
		"the pairing code":           {"Authorization": {"Bearer " + testCode}},
	} {
		if signedIn(here, header) {
			t.Fatalf("%s was taken as this computer's sign-in", name)
		}
	}
	if signedIn("100.64.0.9:50000", http.Header{"Authorization": {"Bearer " + testSession}}) {
		t.Fatal("this computer's token was taken from another machine")
	}
	// A browser's socket: the token among the subprotocols it offers, and
	// the bridge choosing the console's own. With no daemon it opens, and
	// closes saying so; without the token it is refused.
	dial := func(protocols ...string) (*websocket.Conn, error) {
		conn, _, err := websocket.Dial(ctx, "ws://"+b.local+"/socket", &websocket.DialOptions{HTTPHeader: http.Header{"Origin": {server.URL}}, Subprotocols: protocols})
		return conn, err
	}
	conn, err := dial("nodwyn", "nodwyn.session."+testSession)
	if err != nil {
		t.Fatalf("the page's socket was refused: %v", err)
	}
	defer conn.CloseNow()
	if conn.Subprotocol() != "nodwyn" {
		t.Fatalf("the bridge chose the subprotocol %q", conn.Subprotocol())
	}
	if _, _, err := conn.Read(ctx); websocket.CloseStatus(err) != websocket.StatusTryAgainLater {
		t.Fatalf("closed with %v", err)
	}
	if conn, err := dial("nodwyn"); err == nil {
		conn.CloseNow()
		t.Fatal("a socket without the token was taken")
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
	header := http.Header{"Origin": {server.URL}, "Authorization": {"Bearer " + testSession}}
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
	phone, tablet := join(t, devices, "phone"), join(t, devices, "tablet")
	if err := devices.Revoke("tablet"); err != nil {
		t.Fatal(err)
	}
	b := &bridge{ctx: context.Background(), code: testCode, session: testSession, devices: devices, extra: []string{"desk.test:34567"}}
	b.listensAt("127.0.0.1:34567")
	for _, tt := range []struct {
		name, host, from, header, site string
		status                         int
		body                           string
	}{
		{"this computer", "127.0.0.1:34567", "127.0.0.1:50000", "Authorization=Bearer " + testSession, "same-origin", 200, `{"paired":true}`},
		{"an earlier run", "127.0.0.1:34567", "127.0.0.1:50000", "Authorization=Bearer test-only-an-earlier-run-000000000000000", "same-origin", 200, `{"paired":false}`},
		{"never paired", "127.0.0.1:34567", "127.0.0.1:50000", "", "same-origin", 200, `{"paired":false}`},
		{"a paired device", "desk.test:34567", "100.101.102.103:50000", "Cookie=" + deviceCookie + "=" + phone, "same-origin", 200, `{"paired":true}`},
		{"a revoked device", "desk.test:34567", "100.101.102.103:50000", "Cookie=" + deviceCookie + "=" + tablet, "same-origin", 200, `{"paired":false}`},
		{"this computer's token from another", "desk.test:34567", "100.101.102.103:50000", "Authorization=Bearer " + testSession, "same-origin", 200, `{"paired":false}`},
		{"another site", "127.0.0.1:34567", "127.0.0.1:50000", "Authorization=Bearer " + testSession, "cross-site", 403, ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", "http://"+tt.host+"/paired", nil)
			r.RemoteAddr = tt.from
			r.Header.Set("Sec-Fetch-Site", tt.site)
			if name, value, ok := strings.Cut(tt.header, "="); ok {
				r.Header.Set(name, value)
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
		header := http.Header{"Origin": {server.URL}, "Authorization": {"Bearer " + testSession}}
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
	header := http.Header{"Origin": {server.URL}, "Authorization": {"Bearer " + testSession}}
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
	// trust.set is the passkey's alone: this computer's console does not make
	// it (ADR 45, amended 2026-10-17); `nodwyn trust` is this computer's way.
	// host.connect and host.attach are the bridge's for a linked host's
	// socket and uploads (ADR 54): called on the console's own connection,
	// the first would make it a relay.
	terminalOnly := []string{"run.start", "run.info", "run.stop", "run.resume", "session.eval", "session.attach", "trust.set", "host.connect", "host.attach"}
	b := &bridge{}
	for _, method := range hello.Methods {
		switch allowed := b.allowed(method, caller{local: true}); {
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
	// Each pairing names a device of its own: an invitation takes no name
	// in use.
	paired := 0
	pair := func(host, origin string) *httptest.ResponseRecorder {
		code, _, err := devices.Invite(Terminal, false)
		if err != nil {
			t.Fatal(err)
		}
		paired++
		r := httptest.NewRequest("POST", "http://"+host+"/pair", strings.NewReader(`{"invite":"`+code+`","device":"phone-`+strconv.Itoa(paired)+`"}`))
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
	header := http.Header{"Origin": {server.URL}, "Authorization": {"Bearer " + testSession}}
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
