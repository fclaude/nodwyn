package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/fclaude/nibb/internal/config"
	"github.com/fclaude/nibb/internal/core"
	"github.com/fclaude/nibb/internal/frontend/rpc"
	"github.com/fclaude/nibb/internal/llm"
	fake "github.com/fclaude/nibb/test/fakes/ollama"
)

const testCode = "test-only-browser-pairing-capability-000000"

func TestBrowserAuthorityBoundary(t *testing.T) {
	b := &bridge{ctx: context.Background(), host: "127.0.0.1:34567", code: testCode}
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
	for _, method := range []string{"session.open", "session.resume", "session.send", "session.eval", "session.answer", "session.secret", "session.binding.create", "session.approval.create", "session.abort", "session.close"} {
		if readOnly(method) {
			t.Fatal(method)
		}
	}
}

func TestWebSocketUsesDaemonAndRefusesMutations(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	root, err := os.MkdirTemp("/tmp", "nibb-web-")
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
	b := &bridge{ctx: ctx, socket: socket, code: testCode}
	server := httptest.NewUnstartedServer(b)
	b.host = server.Listener.Addr().String()
	server.Start()
	defer server.Close()
	header := http.Header{"Origin": {server.URL}, "Cookie": {b.cookieName() + "=" + testCode}}
	conn, _, err := websocket.Dial(ctx, "ws://"+b.host+"/socket", &websocket.DialOptions{HTTPHeader: header})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
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
	send(1, "session.list", map[string]any{})
	if raw := receive(); !strings.Contains(string(raw["result"]), info.ID) {
		t.Fatal(raw)
	}
	send(2, "session.send", map[string]any{"id": info.ID, "text": "must not run"})
	if raw := receive(); !strings.Contains(string(raw["error"]), "read-only") {
		t.Fatal(raw)
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
