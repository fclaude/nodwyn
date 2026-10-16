package rpc

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/fclaude/nodwyn/internal/config"
	"github.com/fclaude/nodwyn/internal/core"
	"github.com/fclaude/nodwyn/internal/llm"
	"github.com/fclaude/nodwyn/internal/log"
	"github.com/fclaude/nodwyn/internal/mcp"
	"github.com/fclaude/nodwyn/internal/schedule"
	fakemcp "github.com/fclaude/nodwyn/test/fakes/mcp"
	fake "github.com/fclaude/nodwyn/test/fakes/ollama"
)

// Closing an idle scheduled task can take a while (it stops the task's
// servers and asks git about its worktree), and every request to the daemon
// takes the lifecycle lock, so the scheduler closes outside it (P2-054). The
// task's MCP server holds the end of its session until released, which is
// what makes the close slow.
func TestIdleCloseDoesNotHoldTheLifecycleLock(t *testing.T) {
	fakeModel := fake.NewT(t, fake.Turn{Content: "done"})
	defer fakeModel.Close()
	o := options(t)
	o.Provider = llm.NewOllama(fakeModel.URL)
	repo := filepath.Join(t.TempDir(), "work")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	server := fakemcp.New(fakemcp.Tool{Name: "search", Description: "Search", Reply: func(map[string]any) (string, bool) { return "found", false }})
	defer server.Close()
	probe, _ := mcp.New(server.URL, nil)
	probe.Initialize(context.Background())
	tools, _ := probe.Tools(context.Background())
	if err := config.SaveMCPServer(config.MCPFile(o.Paths), config.MCPServer{Name: "chat", URL: server.URL, Manifest: mcp.Manifest(tools),
		Workspaces: []string{repo}, Tools: map[string]config.MCPTool{"search": {Effect: config.EffectRead, On: true}}}); err != nil {
		t.Fatal(err)
	}
	s := schedulerServer(t, o)
	first := o
	first.Repo = repo
	session, err := core.New(first)
	if err != nil {
		t.Fatal(err)
	}
	id := session.ID()
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	now := time.Now().Truncate(time.Minute)
	def := log.SessionSchedule{Definition: schedule.Definition{Message: "check", Repeat: "hourly", At: now.Add(-time.Hour).Format(schedule.Layout), Cost: 1}}
	if err := s.store().SetSchedule(id, def); err != nil {
		t.Fatal(err)
	}
	tickSchedule(s, now)
	scheduleIdle(t, s, id)

	// From here the server holds the DELETE that ends the task's session.
	ending, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	next := server.Config.Handler
	server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			once.Do(func() { close(ending) })
			<-release
		}
		next.ServeHTTP(w, r)
	})
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })
	ticked := make(chan struct{})
	go func() {
		tickSchedule(s, now.Add(16*time.Minute))
		close(ticked)
	}()
	select {
	case <-ending:
	case <-time.After(10 * time.Second):
		t.Fatal("the idle task was not closed")
	}
	if !s.mu.TryLock() {
		t.Fatal("the lifecycle lock is held while the idle task closes")
	}
	s.mu.Unlock()
	releaseOnce.Do(func() { close(release) })
	select {
	case <-ticked:
	case <-time.After(10 * time.Second):
		t.Fatal("the tick did not finish")
	}
	if _, open := s.sessions[id]; open {
		t.Fatal("idle session stayed open")
	}
}
