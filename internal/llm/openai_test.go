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
			{Role: RoleAssistant, Thinking: "list first", ToolCalls: []ToolCall{{ID: "c1", Name: "fs-list", Args: json.RawMessage(`{"path":"."}`)}}},
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
	// A reply's thinking goes back on it, as reasoning_content.
	if msgs[1].(map[string]any)["reasoning_content"] != "list first" || msgs[0].(map[string]any)["reasoning_content"] != nil {
		t.Fatalf("thinking sent as %v", msgs)
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

// A server that only knows the older field gets the cap as max_tokens, and
// never both fields.
func TestOpenAIMaxTokensField(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		json.Unmarshal(b, &got)
		w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer srv.Close()
	o := NewOpenAI(srv.URL+"/v1", nil)
	o.MaxTokensField = "max_tokens"
	if _, err := o.Chat(context.Background(), Request{Model: "m", Messages: []Message{{Role: RoleUser, Content: "hi"}}, MaxTokens: 4096}, nil); err != nil {
		t.Fatal(err)
	}
	if got["max_tokens"] != float64(4096) || got["max_completion_tokens"] != nil {
		t.Fatalf("request: %v", got)
	}
}

// Sampling settings are chat-completions fields when set, absent otherwise.
func TestOpenAISampling(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got = nil
		json.Unmarshal(b, &got)
		w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer srv.Close()
	o := NewOpenAI(srv.URL+"/v1", nil)
	temperature, seed := 0.2, int64(7)
	msgs := []Message{{Role: RoleUser, Content: "hi"}}
	if _, err := o.Chat(context.Background(), Request{Model: "m", Messages: msgs, Temperature: &temperature, Seed: &seed}, nil); err != nil {
		t.Fatal(err)
	}
	if got["temperature"] != 0.2 || got["seed"] != float64(7) {
		t.Fatalf("request: %v", got)
	}
	if _, err := o.Chat(context.Background(), Request{Model: "m", Messages: msgs}, nil); err != nil {
		t.Fatal(err)
	}
	if _, ok := got["temperature"]; ok {
		t.Fatalf("unset temperature sent: %v", got)
	}
}

// A cache key goes with every chat request as X-Session-Id (OpenRouter's) and
// X-Session-Affinity (Baseten's) and, unless the server is to get it only as a
// header, as prompt_cache_key; without one, none is sent. The model list is
// asked without it.
func TestOpenAICacheKey(t *testing.T) {
	var got map[string]any
	var header, affinity string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		header, affinity = r.Header.Get("X-Session-Id"), r.Header.Get("X-Session-Affinity")
		if r.Method == http.MethodGet {
			w.Write([]byte(`{"data":[]}`))
			return
		}
		b, _ := io.ReadAll(r.Body)
		got = nil
		json.Unmarshal(b, &got)
		w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer srv.Close()
	msgs := []Message{{Role: RoleUser, Content: "hi"}}
	for _, tc := range []struct {
		key        string
		headerOnly bool
		wantBody   any
	}{
		{"k1", false, "k1"},
		{"k1", true, nil},
		{"", false, nil},
	} {
		o := NewOpenAI(srv.URL+"/v1", nil)
		o.CacheKey, o.CacheKeyHeaderOnly = tc.key, tc.headerOnly
		if _, err := o.Chat(context.Background(), Request{Model: "m", Messages: msgs}, nil); err != nil {
			t.Fatal(err)
		}
		if header != tc.key || affinity != tc.key || got["prompt_cache_key"] != tc.wantBody {
			t.Errorf("key %q, header only %v: sent headers %q and %q, body %v", tc.key, tc.headerOnly, header, affinity, got)
		}
		if _, err := o.Ping(context.Background()); err != nil {
			t.Fatal(err)
		}
		if header != "" || affinity != "" {
			t.Errorf("the model list was asked with X-Session-Id %q and X-Session-Affinity %q", header, affinity)
		}
	}
}

// The model list is asked with the key, as OpenAI and Cerebras list their
// models only to one: sessions 20261005T042752Z-871dfe24 and
// 20261004T182412Z-747045aa were answered 401 and 403 when it was not.
func TestOpenAIContextLength(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Authorization") != "Bearer k" {
			http.Error(w, `{"error":{"message":"Missing bearer authentication in header"}}`, http.StatusUnauthorized)
			return
		}
		w.Write([]byte(`{"data":[
			{"id":"routed","context_length":1048576,"top_provider":{"context_length":262144}},
			{"id":"listed","context_length":1048576},
			{"id":"vllm","max_model_len":32768},
			{"id":"silent"}]}`))
	}))
	defer srv.Close()
	o := NewOpenAI(srv.URL+"/v1", func(r *http.Request) error { r.Header.Set("Authorization", "Bearer k"); return nil })
	for model, want := range map[string]int{"routed": 262144, "listed": 1048576, "vllm": 32768} {
		if n, err := o.ContextLength(context.Background(), model); err != nil || n != want {
			t.Errorf("%s: %d, %v; want %d", model, n, err, want)
		}
	}
	for _, model := range []string{"silent", "absent"} {
		if n, err := o.ContextLength(context.Background(), model); err == nil || n != 0 {
			t.Errorf("%s: %d, %v; want an error", model, n, err)
		}
	}
	// A key its binding refuses is not sent, and the list is not asked.
	refused := NewOpenAI(srv.URL+"/v1", func(*http.Request) error { return errors.New("method GET is not bound") })
	if _, err := refused.ContextLength(context.Background(), "listed"); err == nil || !strings.Contains(err.Error(), "not bound") {
		t.Fatalf("a refused key: %v", err)
	}
}
