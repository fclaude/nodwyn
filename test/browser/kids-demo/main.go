// kids-demo is a synthetic browser fixture. It uses neither accounts nor models.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"

	"github.com/fclaude/nodwyn/internal/frontend/kids"
	"github.com/fclaude/nodwyn/internal/frontend/web"
	"github.com/fclaude/nodwyn/internal/llm"
)

type fake struct {
	mu sync.Mutex
	n  int
}

func (*fake) Name() string                         { return "ollama" }
func (*fake) Ping(context.Context) (string, error) { return "fixture", nil }
func (f *fake) Chat(_ context.Context, _ llm.Request, _ func(llm.Delta)) (*llm.Response, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.n++
	m := llm.Message{Role: "assistant", Content: "I made your space game. Use See app to try it."}
	if f.n%2 == 1 {
		m.Content = ""
		m.ToolCalls = []llm.ToolCall{{ID: fmt.Sprint(f.n), Name: "fs-write", Args: json.RawMessage(`{"path":"index.html","content":"<!doctype html><title>Space</title><h1>Space game</h1>"}`)}}
	}
	return &llm.Response{Message: m, Usage: llm.Usage{PromptTokens: 100, OutputTokens: 100}}, nil
}
func run() error {
	dir, err := os.MkdirTemp("", "kids-browser-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	p := kids.Profile{Child: "fixture", Host: "https://child.test", Preview: "https://child.test:8443", Public: "https://child.example", Endpoint: "http://model:11434", Model: "local-model", State: filepath.Join(dir, "state"), Workspace: filepath.Join(dir, "work"), Export: filepath.Join(dir, "export"), Socket: filepath.Join(dir, "http.sock"), DailyTokens: 2000000, DailyMinutes: 60}
	s, err := kids.New(p, &fake{})
	if err != nil {
		return err
	}
	devices := web.OpenDevices(filepath.Join(dir, "devices.toml"))
	invite, _, err := devices.Invite(web.Terminal, false)
	if err != nil {
		return err
	}
	automatic, _, err := devices.Invite(web.Terminal, false)
	if err != nil {
		return err
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	defer listener.Close()
	code, err := web.PairingCode()
	if err != nil {
		return err
	}
	if err = json.NewEncoder(os.Stdout).Encode(map[string]string{"url": "http://" + listener.Addr().String(), "invite": invite, "automatic": automatic, "workspace": p.Workspace}); err != nil {
		return err
	}
	return web.Serve(context.Background(), listener, web.Options{PairingCode: code, Devices: devices, Managed: s})
}
func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
