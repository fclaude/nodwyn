package plain

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fclaude/nodwyn/internal/config"
	"github.com/fclaude/nodwyn/internal/core"
	"github.com/fclaude/nodwyn/internal/frontend/rpc"
	"github.com/fclaude/nodwyn/internal/llm"
	"github.com/fclaude/nodwyn/internal/log"
	"github.com/fclaude/nodwyn/internal/schedule"
	"github.com/fclaude/nodwyn/test/fakes/home"
	"github.com/fclaude/nodwyn/test/fakes/must"
	fake "github.com/fclaude/nodwyn/test/fakes/ollama"
)

// The tests run in a home of their own (test/fakes/home): nodwyn's
// denylist, git's configuration and nodwyn's own read from it.
func TestMain(m *testing.M) {
	done := home.Isolate()
	code := m.Run()
	done()
	os.Exit(code)
}

// testOptions are a daemon's options over a task folder beside its config
// and data, never around them: a daemon refuses a root that holds its own
// directories. Writes ask, so a write is the prompt a test answers.
func testOptions(t *testing.T, model string) core.Options {
	t.Helper()
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	must.MkdirAll(t, repo, 0o700)
	cfg := config.Default()
	cfg.Approval.Writes = config.ApproveAsk
	cfg.Control.URL, cfg.Control.Model = model, "test-model"
	return core.Options{Config: cfg, Repo: repo, Paths: config.PathsUnder(filepath.Join(root, "config"), filepath.Join(root, "data")), Version: "test", Provider: llm.NewOllama(model)}
}

// testDaemon serves o's tasks on a socket of its own and connects to it.
func testDaemon(t *testing.T, o core.Options) (*rpc.Client, string) {
	t.Helper()
	// In /tmp: a socket's path is short.
	dir, err := os.MkdirTemp("/tmp", "nodwyn-plain-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	socket := filepath.Join(dir, "sock")
	listener, err := rpc.Listen(socket)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- rpc.Serve(ctx, listener, o) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(10 * time.Second):
			t.Error("the daemon did not stop")
		}
	})
	return dial(t, socket), socket
}

func dial(t *testing.T, socket string) *rpc.Client {
	t.Helper()
	c, err := rpc.Dial(context.Background(), socket)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Close)
	return c
}

// openTask opens a new task in repo and says its id.
func openTask(t *testing.T, c *rpc.Client, repo string) string {
	t.Helper()
	var info rpc.SessionDetails
	if err := c.Call(context.Background(), "session.open", map[string]any{"repo": repo}, &info); err != nil {
		t.Fatal(err)
	}
	return info.ID
}

// runChat runs line mode over the task to the end of its input.
func runChat(t *testing.T, chat *Chat) string {
	t.Helper()
	var out bytes.Buffer
	chat.Out = &out
	done := make(chan error, 1)
	go func() { done <- chat.Run(context.Background()) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("%v\n%s", err, out.String())
		}
	case <-time.After(30 * time.Second):
		t.Fatalf("line mode did not finish:\n%s", out.String())
	}
	return out.String()
}

// history is the task's log, read over the socket as any client reads it.
func history(t *testing.T, c *rpc.Client, id string) []log.Event {
	t.Helper()
	var page core.LogPage
	if err := c.Call(context.Background(), "log.read", map[string]any{"id": id, "limit": 1000}, &page); err != nil {
		t.Fatal(err)
	}
	return page.Events
}

// screen is what line mode wrote, read while it still writes.
type screen struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *screen) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

func (s *screen) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}

// waitFor waits until the screen shows want n times.
func (s *screen) waitFor(t *testing.T, want string, n int) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for strings.Count(s.String(), want) < n {
		if time.Now().After(deadline) {
			t.Fatalf("line mode never showed %q %d times:\n%s", want, n, s.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// A task open in line mode is open in the daemon, so its schedule runs there
// as it would for a closed task, and line mode shows the run when it next
// reads. When line mode hosted the task in its own process, that process
// held the task's log, and the daemon could neither run the schedule nor
// record that it skipped it. schedule.run is the scheduler's own path
// (executeSchedule), taken now instead of at the schedule's time.
func TestATaskOpenInLineModeStillRunsOnSchedule(t *testing.T) {
	model := fake.NewT(t, fake.Turn{Content: "the scheduled check is done"}, fake.Turn{Content: "nothing else ran"})
	o := testOptions(t, model.URL)
	client, socket := testDaemon(t, o)
	id := openTask(t, client, o.Repo)
	input, typing := io.Pipe()
	out := &screen{}
	chat := &Chat{Client: client, ID: id, In: input, Out: out}
	done := make(chan error, 1)
	go func() { done <- chat.Run(context.Background()) }()
	out.waitFor(t, "you> ", 1)

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	console := dial(t, socket)
	at := time.Now().Add(48 * time.Hour).Format(schedule.Layout)
	if err := console.Call(ctx, "schedule.set", map[string]any{"id": id, "message": "check the build", "repeat": "daily", "at": at}, nil); err != nil {
		t.Fatal(err)
	}
	if err := console.Call(ctx, "schedule.run", map[string]any{"id": id}, nil); err != nil {
		t.Fatalf("the scheduler could not run the task line mode has open: %v", err)
	}
	var state core.ScheduleInfo
	for state.LastRun == nil {
		if err := console.Call(ctx, "schedule.read", map[string]any{"id": id}, &state); err != nil {
			t.Fatal(err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if state.LastSkip != nil {
		t.Fatalf("the run is recorded as skipped: %+v", state.LastSkip)
	}
	// Line mode was waiting for a line; the next one shows the run, then
	// is sent once it has ended.
	for {
		var info rpc.SessionDetails
		if err := console.Call(ctx, "session.info", map[string]any{"id": id}, &info); err != nil {
			t.Fatal(err)
		}
		if !info.Busy {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := typing.Write([]byte("did anything else run?\n")); err != nil {
		t.Fatal(err)
	}
	out.waitFor(t, "nothing else ran", 1)
	typing.Close()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	got := out.String()
	for _, want := range []string{"── a scheduled run, started by hand ──", "you> check the build", "the scheduled check is done"} {
		if !strings.Contains(got, want) {
			t.Fatalf("line mode does not show %q:\n%s", want, got)
		}
	}
	if strings.Index(got, "the scheduled check is done") > strings.Index(got, "nothing else ran") {
		t.Fatalf("the scheduled run is shown after the turn that followed it:\n%s", got)
	}
}

// --no-confirm answers a confirm with no without reading a line, so the
// script's next line is the next message, not the confirm's answer.
func TestNoConfirmDeclinesWithoutReadingALine(t *testing.T) {
	model := fake.NewT(t,
		fake.Turn{ToolCalls: []fake.ToolCall{{ID: "w", Name: "fs-write", Args: map[string]any{"path": "a.txt", "content": "a"}}}},
		fake.Turn{Content: "declined, so nothing was written"},
		fake.Turn{Content: "the second message"})
	o := testOptions(t, model.URL)
	client, _ := testDaemon(t, o)
	id := openTask(t, client, o.Repo)
	got := runChat(t, &Chat{Client: client, ID: id, NoConfirm: true, In: strings.NewReader("write a\ny\n")})
	if _, err := os.Stat(filepath.Join(o.Repo, "a.txt")); err == nil {
		t.Fatalf("the write was allowed:\n%s", got)
	}
	if !strings.Contains(got, "allow? [y/N/a=always under a.txt/t=all writes this turn] n (--no-confirm)") || !strings.Contains(got, "the second message") {
		t.Fatalf("the confirm read a line, or was not said:\n%s", got)
	}
}
