package core

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/fclaude/nodwyn/internal/llm"
	"github.com/fclaude/nodwyn/internal/log"
	"github.com/fclaude/nodwyn/internal/window"
	fake "github.com/fclaude/nodwyn/test/fakes/ollama"
)

type captureRequests struct {
	llm.Provider
	mu       sync.Mutex
	requests [][]byte
}

func (p *captureRequests) Chat(ctx context.Context, req llm.Request, onDelta func(llm.Delta)) (*llm.Response, error) {
	raw, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	p.mu.Lock()
	p.requests = append(p.requests, raw)
	p.mu.Unlock()
	return p.Provider.Chat(ctx, req, onDelta)
}

func TestRequestReplayMatchesActualProviderAcrossResume(t *testing.T) {
	srv := fake.New(fake.Turn{ToolCalls: []fake.ToolCall{{ID: "read", Name: "fs-read", Args: map[string]any{"path": "note"}}}}, fake.Turn{Content: "first answer"}, fake.Turn{Content: "resumed answer"})
	defer srv.Close()
	capture := &captureRequests{Provider: llm.NewOllama(srv.URL)}
	o := diskOptions(t)
	o.Provider = capture
	o.Config.Window.MaxResultTokens = 16
	const canary = "sk-ant-abcdefgh1234"
	if err := os.WriteFile(filepath.Join(o.Repo, "note"), []byte(strings.Repeat("ordinary file text ", 200)+canary), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Send("read note"); err != nil {
		t.Fatal(err)
	}
	(&rig{t: t, s: s}).drain("n")
	id := s.ID()
	s.Close()
	// A resumed runtime may use a different catalog and window policy. Replay
	// must select the recorded recipe, never the current configuration/init file.
	if err := os.MkdirAll(filepath.Join(o.Repo, ".nodwyn"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(o.Repo, ".nodwyn", "init.scm"), []byte(`(register-tool "replay-test" (lambda (name) "ok") '((doc . "Replay test") (params . (("name" "string" "name")))))`), 0o600); err != nil {
		t.Fatal(err)
	}
	// Both model options and the tool catalog change on resume.
	o.Config.Control.Model = "changed-model"
	o.Config.Control.KeepAlive = "5m"
	o.Config.Window.MaxResultTokens = 32
	resumed, err := Resume(o, id)
	if err != nil {
		t.Fatal(err)
	}
	if err := resumed.Send("continue"); err != nil {
		t.Fatal(err)
	}
	(&rig{t: t, s: resumed}).drain("n")
	resumed.Close()
	events, err := log.ReadFile(log.SessionPath(o.Paths.SessionsDir, id))
	if err != nil {
		t.Fatal(err)
	}
	capture.mu.Lock()
	defer capture.mu.Unlock()
	request := 0
	for _, ev := range events {
		if ev.Kind != log.KindLLMReq {
			continue
		}
		rebuilt, err := window.ReplayRequest(events, ev.Seq)
		if err != nil {
			t.Fatalf("seq %d: %v", ev.Seq, err)
		}
		raw, err := json.Marshal(rebuilt)
		if err != nil {
			t.Fatal(err)
		}
		if request >= len(capture.requests) || !bytes.Equal(raw, capture.requests[request]) {
			t.Fatalf("seq %d differs from actual provider request", ev.Seq)
		}
		if bytes.Contains(raw, []byte(canary)) {
			t.Fatal("canary in replay")
		}
		request++
	}
	if request != 3 || request != len(capture.requests) {
		t.Fatal("missing requests", request)
	}
}
