package rpc

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/fclaude/nodwyn/internal/budget"
	"github.com/fclaude/nodwyn/internal/config"
	"github.com/fclaude/nodwyn/internal/core"
	"github.com/fclaude/nodwyn/internal/log"
)

func shortDir(t *testing.T) string {
	t.Helper()
	// macOS limits Unix socket paths to 104 bytes; testing.T's temp path is longer.
	dir, err := os.MkdirTemp("/tmp", "nodwyn-rpc-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

// options are a daemon's. Writes ask (approval.writes, ADR 35), so a write
// is the prompt these tests answer.
func options(t *testing.T) core.Options {
	t.Helper()
	dir := t.TempDir()
	cfg := config.Default()
	cfg.Approval.Writes = config.ApproveAsk
	return core.Options{Config: cfg, Paths: config.PathsUnder(filepath.Join(dir, "config"), filepath.Join(dir, "data")), Version: "test"}
}
func start(t *testing.T, o core.Options, opts ...ServeOption) (string, func()) {
	t.Helper()
	path := filepath.Join(shortDir(t), "sock")
	listener, err := Listen(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Serve(ctx, listener, o, opts...) }()
	var once sync.Once
	stop := func() {
		once.Do(func() {
			cancel()
			select {
			case err := <-done:
				if err != nil {
					t.Error(err)
				}
			case <-time.After(5 * time.Second):
				t.Error("daemon did not shut down")
			}
		})
	}
	t.Cleanup(stop)
	return path, stop
}

type client struct {
	t        *testing.T
	conn     net.Conn
	enc      *json.Encoder
	dec      *json.Decoder
	notices  []reply
	observed []reply
}
type reply struct {
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
	ID     json.RawMessage `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  *rpcError       `json:"error"`
}

func connect(t *testing.T, path string) *client {
	t.Helper()
	conn, err := net.Dial("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	return &client{t: t, conn: conn, enc: json.NewEncoder(conn), dec: json.NewDecoder(conn)}
}
func (c *client) call(method string, p any) reply {
	c.t.Helper()
	c.conn.SetDeadline(time.Now().Add(5 * time.Second))
	if err := c.enc.Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": p}); err != nil {
		c.t.Fatal(err)
	}
	for {
		var r reply
		if err := c.dec.Decode(&r); err != nil {
			c.t.Fatal(err)
		}
		c.observed = append(c.observed, r)
		if r.Method != "" {
			c.notices = append(c.notices, r)
			continue
		}
		return r
	}
}
func (c *client) ok(method string, p any) json.RawMessage {
	c.t.Helper()
	r := c.call(method, p)
	if r.Error != nil {
		c.t.Fatalf("%s: %+v", method, r.Error)
	}
	return r.Result
}
func (c *client) hello() { c.t.Helper(); c.ok("hello", map[string]any{"protocol": 1}) }

func TestLifecycleAcrossConnectionsAndRestart(t *testing.T) {
	o := options(t)
	path, stop := start(t, o)
	c := connect(t, path)
	if r := c.call("session.list", struct{}{}); r.Error == nil || r.Error.Code != -32001 {
		t.Fatal("handshake was not required", r)
	}
	if r := c.call("hello", map[string]any{"protocol": 2}); r.Error == nil {
		t.Fatal("accepted unknown version")
	}
	c.hello()
	if got := string(c.ok("session.list", struct{}{})); got != "[]" {
		t.Fatal(got)
	}
	repo := t.TempDir()
	var opened sessionDetails
	if err := json.Unmarshal(c.ok("session.open", map[string]any{"repo": repo}), &opened); err != nil {
		t.Fatal(err)
	}
	if opened.ID == "" || opened.Repo != config.CanonicalPath(repo) || len(opened.Tools) == 0 || opened.Budgets != (budgetInfo{}) || opened.Limits != (budget.Task{}) {
		t.Fatalf("details: %+v", opened)
	}
	c.conn.Close()
	other := connect(t, path)
	other.hello()
	p := map[string]any{"id": opened.ID}
	other.ok("session.resume", p) // Attaches to the same owned session, no log reopen.
	var listed []sessionInfo
	json.Unmarshal(other.ok("session.list", struct{}{}), &listed)
	if len(listed) != 1 || !listed[0].Open || listed[0].Busy {
		t.Fatalf("list: %+v", listed)
	}
	// A different process cannot acquire the live session's writer lock.
	if session, err := core.Resume(o, opened.ID); err == nil {
		session.Close()
		t.Fatal("daemon did not retain writer lock")
	}
	other.ok("session.close", p)
	if r := other.call("session.info", p); r.Error == nil || r.Error.Code != -32002 {
		t.Fatal("closed session still attached")
	}
	other.ok("session.resume", p)
	stop() // Also closes still-connected clients and the open session.
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("socket was not removed: %v", err)
	}
	session, err := core.Resume(o, opened.ID)
	if err != nil {
		t.Fatal("shutdown did not release writer", err)
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	path2, _ := start(t, o)
	restarted := connect(t, path2)
	restarted.hello()
	restarted.ok("session.resume", p)
	restarted.ok("session.close", p)
	events, err := log.ReadFile(log.SessionPath(o.Paths.SessionsDir, opened.ID))
	if err != nil {
		t.Fatal(err)
	}
	starts, resumes := 0, 0
	for _, ev := range events {
		if ev.Kind == log.KindSessionStart {
			starts++
		}
		if ev.Kind == log.KindSessionResume {
			resumes++
		}
	}
	if starts != 1 || resumes != 3 {
		t.Fatalf("lifecycle duplicated: starts=%d resumes=%d", starts, resumes)
	}
}

func TestProtocolErrorsBatchesAndNotifications(t *testing.T) {
	s := &server{options: options(t), sessions: map[string]ownedSession{}}
	ready := &peer{}
	cases := []struct {
		raw  string
		code int
	}{
		{`{`, -32700}, {`[]`, -32600}, {`null`, -32600}, {`{"jsonrpc":"2.0","method":1}`, -32600},
		{`{"jsonrpc":"2.0","method":"hello","id":{},"params":{"protocol":1}}`, -32600},
		{`{"jsonrpc":"2.0","method":"hello","id":1,"params":[]}`, -32602},
		{`{"JSONRPC":"2.0","method":"hello","id":1}`, -32600},
		{`{"jsonrpc":"2.0","method":"hello","id":1,"params":{"Protocol":1}}`, -32602},
		{`{"jsonrpc":"2.0","method":"hello","id":1,"params":{"protocol":1,"typo":2}}`, -32602},
	}
	for _, test := range cases {
		got := s.frame([]byte(test.raw), ready).(response)
		if got.Error == nil || got.Error.Code != test.code {
			t.Fatalf("%s: %+v", test.raw, got)
		}
	}
	raw := `[{"jsonrpc":"2.0","method":"hello","params":{"protocol":1}},` +
		`{"jsonrpc":"2.0","method":"session.list","id":9007199254740993},` +
		`{"jsonrpc":"2.0","method":"unknown"},7]`
	got := s.frame([]byte(raw), ready).([]any)
	if len(got) != 2 || string(got[0].(response).ID) != "9007199254740993" || got[1].(response).Error.Code != -32600 {
		t.Fatalf("batch: %+v", got)
	}
	if r := s.frame([]byte(`[{"jsonrpc":"2.0","method":"session.list"}]`), ready); r != nil {
		t.Fatal("notification produced reply", r)
	}
	r := s.frame([]byte(`{"jsonrpc":"2.0","method":"session.missing","id":"not-yet"}`), ready).(response)
	if r.Error == nil || r.Error.Code != -32601 {
		t.Fatal("unimplemented method exposed")
	}
}

func TestConcurrentClientsResumeOnlyOnce(t *testing.T) {
	o := options(t)
	o.Repo = t.TempDir()
	session, err := core.New(o)
	if err != nil {
		t.Fatal(err)
	}
	id := session.ID()
	session.Close()
	path, _ := start(t, o)
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c := connect(t, path)
			c.hello()
			c.ok("session.resume", map[string]any{"id": id})
			c.conn.Close()
		}()
	}
	wg.Wait()
	events, err := log.ReadFile(log.SessionPath(o.Paths.SessionsDir, id))
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, ev := range events {
		if ev.Kind == log.KindSessionResume {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("resumed %d times", count)
	}
}

func TestSocketPermissionsAndExistingPaths(t *testing.T) {
	dir := shortDir(t)
	path := filepath.Join(dir, "sock")
	listener, err := Listen(path)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("socket permissions: %v %v", info, err)
	}
	if second, err := Listen(path); err == nil {
		second.Close()
		t.Fatal("replaced active socket")
	}
	conn, err := net.Dial("unix", path)
	if err != nil {
		t.Fatal("original listener damaged", err)
	}
	conn.Close()
	unsafe := filepath.Join(dir, "unsafe")
	os.Mkdir(unsafe, 0755)
	os.Chmod(unsafe, 0755)
	if l, err := Listen(filepath.Join(unsafe, "sock")); err == nil {
		l.Close()
		t.Fatal("accepted public parent")
	}
	link := filepath.Join(dir, "link")
	os.Symlink(dir, link)
	if l, err := Listen(filepath.Join(link, "sock2")); err == nil {
		l.Close()
		t.Fatal("accepted symlink parent")
	}
	regular := filepath.Join(dir, "file")
	os.WriteFile(regular, []byte("keep"), 0600)
	if l, err := Listen(regular); err == nil {
		l.Close()
		t.Fatal("replaced regular file")
	}
	data, _ := os.ReadFile(regular)
	if string(data) != "keep" {
		t.Fatal("damaged existing file")
	}
	if l, err := Listen("relative/sock"); err == nil {
		l.Close()
		t.Fatal("accepted relative socket")
	}
}

// Listen checks for a stale socket and binds under a lock beside it, so two
// daemons starting at once cannot both find a crashed daemon's socket stale,
// and the second unlink the socket the first has just bound.
func TestListenBindsUnderTheSocketLock(t *testing.T) {
	path := filepath.Join(shortDir(t), "sock")
	lock, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	type bound struct {
		listener *net.UnixListener
		err      error
	}
	done := make(chan bound, 1)
	go func() {
		listener, err := Listen(path)
		done <- bound{listener, err}
	}()
	select {
	case b := <-done:
		if b.listener != nil {
			b.listener.Close()
		}
		t.Fatalf("Listen did not wait for the lock: %v", b.err)
	case <-time.After(200 * time.Millisecond):
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_UN); err != nil {
		t.Fatal(err)
	}
	select {
	case b := <-done:
		if b.err != nil {
			t.Fatal(b.err)
		}
		b.listener.Close()
	case <-time.After(5 * time.Second):
		t.Fatal("Listen did not bind once the lock was free")
	}
}

func TestDefaultSocket(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", "/private/runtime")
	if path, err := DefaultSocket(); err != nil || path != "/private/runtime/nodwyn/sock" {
		t.Fatal(path, err)
	}
	t.Setenv("XDG_RUNTIME_DIR", "relative")
	if _, err := DefaultSocket(); err == nil {
		t.Fatal("accepted relative XDG runtime")
	}
	t.Setenv("XDG_RUNTIME_DIR", "")
	root, err := os.UserCacheDir()
	if err != nil {
		t.Fatal(err)
	}
	if path, err := DefaultSocket(); err != nil || path != filepath.Join(root, "nodwyn", "run", "sock") {
		t.Fatal(path, err)
	}
}

func TestWireRedactionAndOversizedFrame(t *testing.T) {
	o := options(t)
	path, _ := start(t, o)
	c := connect(t, path)
	c.hello()
	const canary = "sk-ant-abcdefgh1234"
	repo := filepath.Join(t.TempDir(), "repo "+canary+" directory")
	os.Mkdir(repo, 0700)
	raw := c.ok("session.open", map[string]any{"repo": repo})
	if strings.Contains(string(raw), canary) {
		t.Fatal("repository metadata leaked canary")
	}
	// Correlation IDs must remain opaque: do not rewrite client-selected IDs.
	fmt.Fprintf(c.conn, "{\"jsonrpc\":\"2.0\",\"method\":\"session.list\",\"id\":%q}\n", canary)
	var r reply
	if err := c.dec.Decode(&r); err != nil || string(r.ID) != fmt.Sprintf("%q", canary) {
		t.Fatal("correlation ID changed", err)
	}
	c.conn.Write([]byte(strings.Repeat(" ", maxFrame) + "\n"))
	if err := c.dec.Decode(&r); err == nil {
		t.Fatal("oversized frame did not disconnect")
	}
	other := connect(t, path)
	other.hello()
	other.ok("session.list", struct{}{})
}
