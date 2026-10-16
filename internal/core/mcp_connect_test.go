package core

import (
	"bytes"
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fclaude/nodwyn/internal/config"
	"github.com/fclaude/nodwyn/internal/llm"
	"github.com/fclaude/nodwyn/internal/mcp"
	fakemcp "github.com/fclaude/nodwyn/test/fakes/mcp"
	fake "github.com/fclaude/nodwyn/test/fakes/ollama"
)

// meeting holds each server's first request until every server has received
// one: servers connected one after another never meet, and the first one
// answers 503 instead (P2-055).
type meeting struct {
	mu      sync.Mutex
	arrived int
	all     chan struct{}
	want    int
}

func (m *meeting) wrap(next http.Handler) http.Handler {
	var once sync.Once
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		once.Do(func() {
			m.mu.Lock()
			m.arrived++
			if m.arrived == m.want {
				close(m.all)
			}
			m.mu.Unlock()
			select {
			case <-m.all:
			case <-time.After(3 * time.Second):
				http.Error(w, "the other server was never contacted", http.StatusServiceUnavailable)
				return
			}
		})
		next.ServeHTTP(w, r)
	})
}

// A session connects its MCP servers at once, so a task waits for the
// slowest and not for the sum, and offers their tools in the order mcp.toml
// lists them whichever answered first.
func TestMCPServersConnectAtOnceAndRegisterInConfigOrder(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	repo := filepath.Join(root, "work")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	paths := config.PathsUnder(filepath.Join(root, "config"), filepath.Join(root, "data"))
	if err := os.MkdirAll(paths.ConfigDir, 0o700); err != nil {
		t.Fatal(err)
	}
	m := &meeting{all: make(chan struct{}), want: 2}
	for _, name := range []string{"first", "second"} {
		server := fakemcp.New(mcpTools()...)
		defer server.Close()
		// The manifest is read before the meeting is set up.
		c, _ := mcp.New(server.URL, nil)
		c.Initialize(context.Background())
		tools, _ := c.Tools(context.Background())
		server.Config.Handler = m.wrap(server.Config.Handler)
		if err := config.SaveMCPServer(config.MCPFile(paths), config.MCPServer{Name: name, URL: server.URL, Manifest: mcp.Manifest(tools),
			Workspaces: []string{repo}, Tools: map[string]config.MCPTool{"search": {Effect: config.EffectRead, On: true}}}); err != nil {
			t.Fatal(err)
		}
	}
	srv := fake.NewT(t)
	defer srv.Close()
	cfg := config.Default()
	cfg.Control.URL, cfg.Control.Model = srv.URL, "test-model"
	s, err := New(Options{Config: cfg, Repo: repo, Paths: paths, Version: "test", Provider: llm.NewOllama(srv.URL), LogWriter: &bytes.Buffer{}})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var got []string
	for _, status := range s.MCP() {
		got = append(got, status.Name+":"+status.State+":"+strings.Join(status.Tools, ","))
	}
	if want := "first:ready:first__search,second:ready:second__search"; strings.Join(got, ",") != want {
		t.Fatalf("servers: %v", got)
	}
}

// A contained server approved with every tool off has nothing to offer, so
// nodwyn stops it at once instead of holding its memory and proxy for the
// whole task (P2-055).
func TestContainedServerWithNoToolOnIsStopped(t *testing.T) {
	w := newContainedWorld(t)
	server := w.approve(w.server())
	server.Tools = map[string]config.MCPTool{"search": {Effect: config.EffectRead}, "write_note": {}}
	w.save(server)
	r := w.session()
	status := r.s.MCP()
	if len(status) != 1 || status[0].State != "ready" || len(status[0].Tools) != 0 || !strings.Contains(status[0].Detail, "none of its tools is on") {
		t.Fatalf("status: %+v", status)
	}
	if running := w.fake.Running(); len(running) != 0 {
		t.Fatalf("a server with no tool on is still running: %v", running)
	}
}
