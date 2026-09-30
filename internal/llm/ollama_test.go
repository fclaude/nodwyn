package llm

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	fake "github.com/fclaude/nodwyn/test/fakes/ollama"
)

func TestOllamaChatStreamsAndAssembles(t *testing.T) {
	srv := fake.New(fake.Turn{
		Thinking:  "let me think",
		Content:   "Hello, world.",
		Chunk:     5,
		ToolCalls: []fake.ToolCall{{ID: "call_x", Name: "fs-read", Args: map[string]any{"path": "a.go"}}},
	})
	defer srv.Close()
	o := NewOllama(srv.URL)
	if v, err := o.Ping(context.Background()); err != nil || v != "fake" {
		t.Fatalf("ping: %v %q", err, v)
	}
	var deltas []Delta
	resp, err := o.Chat(context.Background(), Request{
		Model:     "m",
		Messages:  []Message{{Role: RoleSystem, Content: "sys"}, {Role: RoleUser, Content: "hi"}},
		Tools:     []Tool{{Name: "fs-read", Description: "read", Parameters: map[string]any{"type": "object"}}},
		NumCtx:    1234,
		KeepAlive: "30m",
	}, func(d Delta) { deltas = append(deltas, d) })
	if err != nil {
		t.Fatal(err)
	}
	if resp.Message.Content != "Hello, world." || resp.Message.Thinking != "let me think" {
		t.Fatalf("assembled: %+v", resp.Message)
	}
	if len(deltas) != 4 || deltas[0].Thinking != "let me think" || deltas[1].Text != "Hello" {
		t.Fatalf("deltas: %+v", deltas)
	}
	if len(resp.Message.ToolCalls) != 1 || resp.Message.ToolCalls[0].Name != "fs-read" || string(resp.Message.ToolCalls[0].Args) != `{"path":"a.go"}` {
		t.Fatalf("tool calls: %+v", resp.Message.ToolCalls)
	}
	if resp.DoneReason != "tool_calls" || resp.Usage.PromptTokens != 299 || resp.Usage.EvalMS != 1400 {
		t.Fatalf("done/usage: %s %+v", resp.DoneReason, resp.Usage)
	}
	// What went over the wire.
	req := srv.Requests[0]
	if req.Model != "m" || req.Options["num_ctx"] != float64(1234) || len(req.Tools) != 1 {
		t.Fatalf("wire request: %+v", req)
	}
	if !strings.Contains(string(req.Raw), `"keep_alive":"30m"`) || !strings.Contains(string(req.Raw), `"stream":true`) {
		t.Fatalf("wire: %s", req.Raw)
	}
	var tool map[string]any
	_ = json.Unmarshal(req.Tools[0], &tool)
	if tool["type"] != "function" {
		t.Fatalf("tool wire shape: %v", tool)
	}
}

func TestOllamaHistoryWithToolMessages(t *testing.T) {
	srv := fake.New(fake.Turn{Content: "ok"})
	defer srv.Close()
	o := NewOllama(srv.URL)
	_, err := o.Chat(context.Background(), Request{Model: "m", Messages: []Message{
		{Role: RoleUser, Content: "read it"},
		{Role: RoleAssistant, Content: "", ToolCalls: []ToolCall{{ID: "c1", Name: "fs-read", Args: json.RawMessage(`{"path":"x"}`)}}},
		{Role: RoleTool, Content: "file body", ToolName: "fs-read", ToolCallID: "c1"},
	}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	raw := string(srv.Requests[0].Raw)
	for _, want := range []string{`"tool_calls":[{"id":"c1","function":{"name":"fs-read","arguments":{"path":"x"}}}]`, `"role":"tool","content":"file body","tool_name":"fs-read","tool_call_id":"c1"`} {
		if !strings.Contains(raw, want) {
			t.Fatalf("wire lacks %s:\n%s", want, raw)
		}
	}
	if strings.Contains(raw, `"thinking"`) {
		t.Fatal("thinking must never be sent")
	}
}

func TestOllamaErrors(t *testing.T) {
	srv := fake.New(fake.Turn{Error: "model not found"}, fake.Turn{Status: 503})
	defer srv.Close()
	o := NewOllama(srv.URL)
	if _, err := o.Chat(context.Background(), Request{Model: "m"}, nil); err == nil || !strings.Contains(err.Error(), "model not found") {
		t.Fatalf("error chunk: %v", err)
	}
	if _, err := o.Chat(context.Background(), Request{Model: "m"}, nil); err == nil || !strings.Contains(err.Error(), "503") {
		t.Fatalf("http error: %v", err)
	}
	if _, err := o.Chat(context.Background(), Request{Model: "m"}, nil); err == nil || !strings.Contains(err.Error(), "exhausted") {
		t.Fatalf("exhausted: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := o.Chat(ctx, Request{Model: "m"}, nil); err == nil {
		t.Fatal("cancelled ctx should fail")
	}
}
