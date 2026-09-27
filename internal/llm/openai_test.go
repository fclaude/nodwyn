package llm

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestOpenAIRoundTrip(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer k" {
			http.Error(w, "wrong request", http.StatusBadRequest)
			return
		}
		b, _ := io.ReadAll(r.Body)
		json.Unmarshal(b, &got)
		w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"reading","reasoning_content":"need it",
			"tool_calls":[{"id":"c9","type":"function","function":{"name":"fs-read","arguments":"{\"path\":\"a.go\"}"}}]},
			"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":12,"completion_tokens":3}}`))
	}))
	defer srv.Close()
	o := NewOpenAI(srv.URL+"/v1", func(r *http.Request) error { r.Header.Set("Authorization", "Bearer k"); return nil })
	resp, err := o.Chat(context.Background(), Request{
		Model: "m",
		Messages: []Message{
			{Role: RoleSystem, Content: "sys"},
			{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "c1", Name: "fs-list", Args: json.RawMessage(`{"path":"."}`)}}},
			{Role: RoleTool, Content: "a.go", ToolName: "fs-list", ToolCallID: "c1"},
		},
		Tools:     []Tool{{Name: "fs-read", Description: "read", Parameters: map[string]any{"type": "object"}}},
		MaxTokens: 16384,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	// Arguments go out as JSON text, as the protocol wants.
	msgs := got["messages"].([]any)
	call := msgs[1].(map[string]any)["tool_calls"].([]any)[0].(map[string]any)["function"].(map[string]any)
	if call["arguments"] != `{"path":"."}` {
		t.Fatalf("arguments sent as %v", call["arguments"])
	}
	if msgs[2].(map[string]any)["tool_call_id"] != "c1" || got["stream"] != false || got["max_completion_tokens"] != float64(16384) || got["max_tokens"] != nil {
		t.Fatalf("request: %v", got)
	}
	if resp.Message.Content != "reading" || resp.Message.Thinking != "need it" || resp.DoneReason != "tool_calls" {
		t.Fatalf("response: %+v", resp)
	}
	if tc := resp.Message.ToolCalls[0]; tc.ID != "c9" || tc.Name != "fs-read" || string(tc.Args) != `{"path":"a.go"}` {
		t.Fatalf("tool call: %+v", tc)
	}
	if resp.Usage.PromptTokens != 12 || resp.Usage.OutputTokens != 3 {
		t.Fatalf("usage: %+v", resp.Usage)
	}
}

// A request the core will not authorize is never sent.
func TestOpenAIAuthorizeRefuses(t *testing.T) {
	sent := false
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { sent = true }))
	defer srv.Close()
	o := NewOpenAI(srv.URL, func(*http.Request) error { return errors.New("key not bound for this host") })
	_, err := o.Chat(context.Background(), Request{Model: "m", Messages: []Message{{Role: RoleUser, Content: "hi"}}}, nil)
	if err == nil || !strings.Contains(err.Error(), "not bound") || sent {
		t.Fatalf("err %v, sent %v", err, sent)
	}
}

func TestOpenAIErrorStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"error":"model not found"}`, http.StatusNotFound)
	}))
	defer srv.Close()
	_, err := NewOpenAI(srv.URL, nil).Chat(context.Background(), Request{Model: "m"}, nil)
	if err == nil || !strings.Contains(err.Error(), "model not found") {
		t.Fatalf("err %v", err)
	}
}
