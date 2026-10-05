package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// responsesServer is a strict Responses endpoint: like OpenAI's, it refuses a
// request to any other path and a parameter it does not know, such as the
// chat-completions messages or reasoning_effort, so a request in the wrong
// protocol fails instead of passing. It records each body and gives reply.
func responsesServer(t *testing.T, reply string) (*httptest.Server, *[]map[string]json.RawMessage) {
	t.Helper()
	known := map[string]bool{"model": true, "input": true, "tools": true, "stream": true, "store": true, "max_output_tokens": true,
		"temperature": true, "prompt_cache_key": true, "reasoning": true}
	var bodies []map[string]json.RawMessage
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/responses" || r.Method != http.MethodPost {
			http.Error(w, `{"error":{"message":"Invalid URL"}}`, http.StatusNotFound)
			return
		}
		var body map[string]json.RawMessage
		b, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(b, &body); err != nil {
			http.Error(w, `{"error":{"message":"bad JSON"}}`, http.StatusBadRequest)
			return
		}
		for k := range body {
			if !known[k] {
				http.Error(w, fmt.Sprintf(`{"error":{"message":"Unknown parameter: '%s'."}}`, k), http.StatusBadRequest)
				return
			}
		}
		bodies = append(bodies, body)
		w.Write([]byte(reply))
	}))
	t.Cleanup(srv.Close)
	return srv, &bodies
}

// A conversation goes out as Responses input items: messages, a reply's
// calls after its text, and each result as its call's output, with the tools
// flat and not strict, the effort as reasoning.effort, the cap as
// max_output_tokens, and nothing stored. Thinking stays out. The reply's
// reasoning summary is its thinking, its text its content, its call a tool
// call, and its usage is read.
func TestResponsesRoundTrip(t *testing.T) {
	srv, bodies := responsesServer(t, `{"status":"completed","output":[
		{"type":"reasoning","id":"rs_1","summary":[{"type":"summary_text","text":"need it"}]},
		{"type":"message","id":"msg_1","role":"assistant","content":[{"type":"output_text","text":"reading","annotations":[]}]},
		{"type":"function_call","id":"fc_1","call_id":"c9","name":"fs-read","arguments":"{\"path\":\"a.go\"}"}],
		"usage":{"input_tokens":12,"input_tokens_details":{"cached_tokens":4},"output_tokens":3,"output_tokens_details":{"reasoning_tokens":2}}}`)
	o := NewOpenAI(srv.URL+"/v1", func(r *http.Request) error { r.Header.Set("Authorization", "Bearer k"); return nil })
	o.Dialect = "responses"
	o.MaxTokensField = "max_tokens"
	o.CacheKey = "s1"
	png := Image{MediaType: "image/png", SHA256: "x", Data: []byte{1, 2}}
	resp, err := o.Chat(context.Background(), Request{
		Model: "gpt-6.1-sol",
		Messages: []Message{
			{Role: RoleSystem, Content: "sys"},
			{Role: RoleUser, Content: "look", Images: []Image{png}},
			{Role: RoleAssistant, Thinking: "list first", ToolCalls: []ToolCall{{ID: "c1", Name: "fs-list", Args: json.RawMessage(`{"path":"."}`)}, {ID: "c2", Name: "fs-list"}}},
			{Role: RoleTool, Content: "a.go", ToolName: "fs-list", ToolCallID: "c1"},
			{Role: RoleTool, Content: "", ToolName: "fs-list", ToolCallID: "c2"},
			{Role: RoleAssistant, Content: "now the file", ToolCalls: []ToolCall{{ID: "c3", Name: "fs-read", Args: json.RawMessage(`{"path":"a.go"}`)}}},
			{Role: RoleTool, Content: "package a", ToolCallID: "c3"},
			{Role: RoleAssistant, Content: "done"},
			{Role: RoleUser, Content: "again"},
		},
		Tools:     []Tool{{Name: "fs-read", Description: "read", Parameters: map[string]any{"type": "object"}}},
		MaxTokens: 16384,
		Effort:    "high",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	got := (*bodies)[0]
	want := map[string]string{
		"model": `"gpt-6.1-sol"`,
		"input": `[{"type":"message","role":"system","content":"sys"},` +
			`{"type":"message","role":"user","content":[{"type":"input_text","text":"look"},{"type":"input_image","image_url":"data:image/png;base64,AQI="}]},` +
			`{"type":"function_call","call_id":"c1","name":"fs-list","arguments":"{\"path\":\".\"}"},` +
			`{"type":"function_call","call_id":"c2","name":"fs-list","arguments":"{}"},` +
			`{"type":"function_call_output","call_id":"c1","output":"a.go"},` +
			`{"type":"function_call_output","call_id":"c2","output":""},` +
			`{"type":"message","role":"assistant","content":"now the file"},` +
			`{"type":"function_call","call_id":"c3","name":"fs-read","arguments":"{\"path\":\"a.go\"}"},` +
			`{"type":"function_call_output","call_id":"c3","output":"package a"},` +
			`{"type":"message","role":"assistant","content":"done"},` +
			`{"type":"message","role":"user","content":"again"}]`,
		"tools":             `[{"type":"function","name":"fs-read","description":"read","parameters":{"type":"object"},"strict":false}]`,
		"reasoning":         `{"effort":"high"}`,
		"max_output_tokens": `16384`,
		"store":             `false`,
		"stream":            `false`,
		"prompt_cache_key":  `"s1"`,
	}
	for k, v := range want {
		if string(got[k]) != v {
			t.Errorf("%s:\n got %s\nwant %s", k, got[k], v)
		}
	}
	if len(got) != len(want) {
		t.Errorf("request has %d fields, want %d: %v", len(got), len(want), got)
	}
	if resp.Message.Content != "reading" || resp.Message.Thinking != "need it" || resp.DoneReason != "tool_calls" {
		t.Fatalf("response: %+v", resp)
	}
	if len(resp.Message.ToolCalls) != 1 {
		t.Fatalf("tool calls: %+v", resp.Message.ToolCalls)
	}
	if tc := resp.Message.ToolCalls[0]; tc.ID != "c9" || tc.Name != "fs-read" || string(tc.Args) != `{"path":"a.go"}` {
		t.Fatalf("tool call: %+v", tc)
	}
	u := resp.Usage
	if u.PromptTokens != 12 || u.OutputTokens != 3 || u.CachedTokens == nil || *u.CachedTokens != 4 || u.ReasoningTokens != 2 || u.Unreported {
		t.Fatalf("usage: %+v", u)
	}
}

// A request without an effort, sampling or cache key sends none of them.
func TestResponsesSendsOnlyWhatWasChosen(t *testing.T) {
	srv, bodies := responsesServer(t, `{"status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"hi"}]}]}`)
	o := NewOpenAI(srv.URL+"/v1", nil)
	o.Dialect = "responses"
	resp, err := o.Chat(context.Background(), Request{Model: "m", Messages: []Message{{Role: RoleUser, Content: "hi"}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"reasoning", "temperature", "prompt_cache_key", "max_output_tokens", "tools"} {
		if v, ok := (*bodies)[0][k]; ok {
			t.Errorf("%s sent as %s", k, v)
		}
	}
	if resp.Message.Content != "hi" || resp.DoneReason != "stop" || !resp.Usage.Unreported {
		t.Fatalf("response: %+v", resp)
	}
}

// A reply cut short by the cap is "length", as a chat completion's is, so the
// loop says so; a failed response is an error, not an empty reply.
func TestResponsesIncompleteAndFailed(t *testing.T) {
	for _, tc := range []struct {
		reply, done, err string
	}{
		{`{"status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"output":[{"type":"message","content":[{"type":"output_text","text":"par"}]}]}`, "length", ""},
		{`{"status":"incomplete","incomplete_details":{"reason":"content_filter"},"output":[]}`, "content_filter", ""},
		{`{"status":"failed","error":{"code":"server_error","message":"The model failed"},"output":[]}`, "", "The model failed"},
	} {
		srv, _ := responsesServer(t, tc.reply)
		o := NewOpenAI(srv.URL+"/v1", nil)
		o.Dialect = "responses"
		resp, err := o.Chat(context.Background(), Request{Model: "m", Messages: []Message{{Role: RoleUser, Content: "hi"}}}, nil)
		if tc.err != "" {
			if err == nil || !strings.Contains(err.Error(), tc.err) {
				t.Errorf("%s: err %v, want one saying %q", tc.reply, err, tc.err)
			}
			continue
		}
		if err != nil || resp.DoneReason != tc.done {
			t.Errorf("%s: %+v, %v; want done %q", tc.reply, resp, err, tc.done)
		}
	}
}

// OpenAI's refusal of an effort beside tools on /chat/completions names the
// Responses API; the advice says to choose it, not another effort.
func TestARefusalNamingTheResponsesAPISaysToChooseIt(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":{"message":"Function tools with reasoning_effort are not supported for gpt-6.1-sol in /v1/chat/completions. To use function tools, use /v1/responses or set reasoning_effort to 'none'."}}`, http.StatusBadRequest)
	}))
	defer srv.Close()
	_, err := NewOpenAI(srv.URL, nil).Chat(context.Background(), Request{Model: "gpt-6.1-sol", Effort: "high"}, nil)
	var se *StatusError
	if !errors.As(err, &se) {
		t.Fatalf("err %v", err)
	}
	if hint := Hint(err); !strings.Contains(hint, "OpenAI Responses") || strings.Contains(hint, "effort") {
		t.Fatalf("hint %q", hint)
	}
}
