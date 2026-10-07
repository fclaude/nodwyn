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

// anthropicServer answers every request with reply, recording the last
// request's path, headers and JSON body.
type anthropicServer struct {
	*httptest.Server
	path   string
	header http.Header
	body   map[string]any
}

func newAnthropicServer(t *testing.T, status int, reply string) *anthropicServer {
	t.Helper()
	s := &anthropicServer{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.path, s.header = r.URL.RequestURI(), r.Header.Clone()
		if b, _ := io.ReadAll(r.Body); len(b) > 0 {
			s.body = nil
			if err := json.Unmarshal(b, &s.body); err != nil {
				t.Errorf("request body is not JSON: %v", err)
			}
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(reply))
	}))
	t.Cleanup(s.Close)
	return s
}

func withKey(key string) func(*http.Request) error {
	return func(r *http.Request) error { r.Header.Set("x-api-key", key); return nil }
}

func TestAnthropicRoundTrip(t *testing.T) {
	srv := newAnthropicServer(t, 200, `{"content":[
		{"type":"thinking","thinking":"need the file","signature":"abc"},
		{"type":"text","text":"reading"},
		{"type":"tool_use","id":"toolu_9","name":"fs-read","input":{"path":"a.go"}}],
		"stop_reason":"tool_use","usage":{"input_tokens":12,"output_tokens":3}}`)
	a := NewAnthropic(srv.URL+"/v1", withKey("sk-ant-test"))
	resp, err := a.Chat(context.Background(), Request{
		Model: "claude-x",
		Messages: []Message{
			{Role: RoleSystem, Content: "sys"},
			{Role: RoleUser, Content: "list the files, then read two"},
			{Role: RoleAssistant, Content: "listing", Thinking: "list first", ToolCalls: []ToolCall{
				{ID: "c1", Name: "fs-list", Args: json.RawMessage(`{"path":"."}`)},
				{ID: "c2", Name: "fs-list", Args: json.RawMessage(`{"path":"src"}`)},
			}},
			{Role: RoleTool, Content: "a.go", ToolName: "fs-list", ToolCallID: "c1"},
			{Role: RoleTool, Content: "b.go", ToolName: "fs-list", ToolCallID: "c2"},
			{Role: RoleUser, Content: "now read them"},
		},
		Tools:     []Tool{{Name: "fs-read", Description: "read", Parameters: map[string]any{"type": "object"}}},
		MaxTokens: 4096,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if srv.path != "/v1/messages" || srv.header.Get("x-api-key") != "sk-ant-test" || srv.header.Get("anthropic-version") != "2023-06-01" || srv.header.Get("Content-Type") != "application/json" {
		t.Fatalf("request: %s %v", srv.path, srv.header)
	}
	if srv.body["system"] != "sys" || srv.body["max_tokens"] != float64(4096) || srv.body["stream"] != nil || srv.body["thinking"] != nil || srv.body["output_config"] != nil || srv.body["cache_control"] != nil {
		t.Fatalf("request fields: %v", srv.body)
	}
	tools := srv.body["tools"].([]any)
	if tool := tools[0].(map[string]any); tool["name"] != "fs-read" || tool["input_schema"].(map[string]any)["type"] != "object" {
		t.Fatalf("tool: %v", tool)
	}
	// Both results of the turn go in one user message, ahead of the text that
	// follows them; the call is an object, and the thinking stays out.
	msgs := srv.body["messages"].([]any)
	if len(msgs) != 3 {
		t.Fatalf("%d messages sent: %v", len(msgs), msgs)
	}
	assistant := msgs[1].(map[string]any)
	if assistant["role"] != "assistant" {
		t.Fatalf("message 1: %v", assistant)
	}
	blocks := assistant["content"].([]any)
	if len(blocks) != 3 || blocks[0].(map[string]any)["type"] != "text" {
		t.Fatalf("assistant blocks (thinking must not go back): %v", blocks)
	}
	call := blocks[1].(map[string]any)
	if call["type"] != "tool_use" || call["id"] != "c1" || call["input"].(map[string]any)["path"] != "." {
		t.Fatalf("tool_use: %v", call)
	}
	user := msgs[2].(map[string]any)["content"].([]any)
	if len(user) != 3 {
		t.Fatalf("results and text: %v", user)
	}
	for i, want := range []struct{ id, content string }{{"c1", "a.go"}, {"c2", "b.go"}} {
		b := user[i].(map[string]any)
		if b["type"] != "tool_result" || b["tool_use_id"] != want.id || b["content"] != want.content {
			t.Fatalf("tool_result %d: %v", i, b)
		}
	}
	if b := user[2].(map[string]any); b["type"] != "text" || b["text"] != "now read them" {
		t.Fatalf("text after the results: %v", b)
	}
	if resp.Message.Content != "reading" || resp.Message.Thinking != "need the file" || resp.DoneReason != "tool_calls" {
		t.Fatalf("response: %+v", resp)
	}
	if tc := resp.Message.ToolCalls[0]; tc.ID != "toolu_9" || tc.Name != "fs-read" || string(tc.Args) != `{"path":"a.go"}` {
		t.Fatalf("tool call: %+v", tc)
	}
	if resp.Usage.PromptTokens != 12 || resp.Usage.OutputTokens != 3 || resp.Usage.Unreported || resp.Usage.CachedTokens != nil {
		t.Fatalf("usage: %+v", resp.Usage)
	}
}

func TestAnthropicEffortAsksForAdaptiveThinking(t *testing.T) {
	srv := newAnthropicServer(t, 200, `{"content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`)
	a := NewAnthropic(srv.URL+"/v1", nil)
	ask := func(effort string) {
		t.Helper()
		if _, err := a.Chat(context.Background(), Request{Model: "m", Messages: []Message{{Role: RoleUser, Content: "hi"}}, MaxTokens: 100, Effort: effort}, nil); err != nil {
			t.Fatal(err)
		}
	}
	ask("high")
	if got := srv.body["output_config"]; got == nil || got.(map[string]any)["effort"] != "high" {
		t.Fatalf("output_config: %v", srv.body)
	}
	if got := srv.body["thinking"].(map[string]any); got["type"] != "adaptive" || got["display"] != "summarized" {
		t.Fatalf("thinking: %v", got)
	}
	// No effort asks for neither: a model nobody declared efforts for may
	// refuse the fields.
	ask("")
	if srv.body["output_config"] != nil || srv.body["thinking"] != nil {
		t.Fatalf("effort-less request carried %v", srv.body)
	}
}

func TestAnthropicCacheControlAndMaxTokens(t *testing.T) {
	srv := newAnthropicServer(t, 200, `{"content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{}}`)
	a := NewAnthropic(srv.URL+"/v1", nil)
	a.CacheControl = "1h"
	if _, err := a.Chat(context.Background(), Request{Model: "m", Messages: []Message{{Role: RoleUser, Content: "hi"}}}, nil); err != nil {
		t.Fatal(err)
	}
	cc := srv.body["cache_control"].(map[string]any)
	if cc["type"] != "ephemeral" || cc["ttl"] != "1h" {
		t.Fatalf("cache_control: %v", cc)
	}
	// The API refuses a request without max_tokens, so one that names none
	// gets the default.
	if srv.body["max_tokens"] != float64(anthropicDefaultMaxTokens) {
		t.Fatalf("max_tokens: %v", srv.body["max_tokens"])
	}
	a.CacheControl = "ephemeral"
	if _, err := a.Chat(context.Background(), Request{Model: "m", Messages: []Message{{Role: RoleUser, Content: "hi"}}, MaxTokens: 50}, nil); err != nil {
		t.Fatal(err)
	}
	cc = srv.body["cache_control"].(map[string]any)
	if cc["type"] != "ephemeral" || cc["ttl"] != nil || srv.body["max_tokens"] != float64(50) {
		t.Fatalf("five minute ask: %v, max_tokens %v", cc, srv.body["max_tokens"])
	}
}

func TestAnthropicImagesAndTemperature(t *testing.T) {
	srv := newAnthropicServer(t, 200, `{"content":[{"type":"text","text":"a cat"}],"stop_reason":"end_turn","usage":{}}`)
	a := NewAnthropic(srv.URL+"/v1", nil)
	temp := 0.5
	_, err := a.Chat(context.Background(), Request{Model: "m", Temperature: &temp, MaxTokens: 10, Messages: []Message{
		{Role: RoleUser, Content: "what is this", Images: []Image{{MediaType: "image/png", Data: []byte("png"), SHA256: "x"}, {MediaType: "image/png"}}},
	}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	blocks := srv.body["messages"].([]any)[0].(map[string]any)["content"].([]any)
	if len(blocks) != 2 {
		t.Fatalf("blocks (an image without data is left out): %v", blocks)
	}
	img := blocks[1].(map[string]any)
	src := img["source"].(map[string]any)
	if img["type"] != "image" || src["type"] != "base64" || src["media_type"] != "image/png" || src["data"] != "cG5n" {
		t.Fatalf("image block: %v", img)
	}
	if srv.body["temperature"] != 0.5 {
		t.Fatalf("temperature: %v", srv.body["temperature"])
	}
}

func TestAnthropicHistoryFromAnotherProvider(t *testing.T) {
	srv := newAnthropicServer(t, 200, `{"content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{}}`)
	a := NewAnthropic(srv.URL+"/v1", nil)
	_, err := a.Chat(context.Background(), Request{Model: "m", MaxTokens: 10, Messages: []Message{
		{Role: RoleUser, Content: "go"},
		// An id with characters the API refuses, and arguments that are text,
		// as a model that sent bad JSON leaves them.
		{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "call.1:a", Name: "fs-read", Args: json.RawMessage(`"not an object"`)}}},
		{Role: RoleTool, Content: "bad arguments", ToolCallID: "call.1:a"},
		// A reply with nothing in it, and a message with nothing to say.
		{Role: RoleAssistant},
		{Role: RoleUser},
		{Role: RoleUser, Content: "again"},
	}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	msgs := srv.body["messages"].([]any)
	if len(msgs) != 3 {
		t.Fatalf("%d messages: %v", len(msgs), msgs)
	}
	call := msgs[1].(map[string]any)["content"].([]any)[0].(map[string]any)
	result := msgs[2].(map[string]any)["content"].([]any)
	if call["id"] != "call_1_a" || len(call["input"].(map[string]any)) != 0 {
		t.Fatalf("tool_use: %v", call)
	}
	if first := result[0].(map[string]any); first["tool_use_id"] != "call_1_a" {
		t.Fatalf("tool_result does not pair with its call: %v", result)
	}
	if len(result) != 2 || result[1].(map[string]any)["text"] != "again" {
		t.Fatalf("the empty messages should leave only the result and the text: %v", result)
	}
}

func TestAnthropicStopReasons(t *testing.T) {
	for name, tc := range map[string]struct {
		reply, done, content string
	}{
		"cut at the cap": {`{"content":[{"type":"text","text":"half"}],"stop_reason":"max_tokens","usage":{}}`, "length", "half"},
		"stop":           {`{"content":[{"type":"text","text":"done"}],"stop_reason":"end_turn","usage":{}}`, "stop", "done"},
		"refusal with a reason": {`{"content":[],"stop_reason":"refusal","stop_details":{"type":"refusal","category":"cyber","explanation":"Not something I can help with."},"usage":{}}`,
			"refusal", "The model declined to answer this request. Not something I can help with."},
		"refusal by category": {`{"content":[],"stop_reason":"refusal","stop_details":{"category":"bio"},"usage":{}}`, "refusal", "The model declined to answer this request. (bio)"},
	} {
		t.Run(name, func(t *testing.T) {
			srv := newAnthropicServer(t, 200, tc.reply)
			a := NewAnthropic(srv.URL+"/v1", nil)
			resp, err := a.Chat(context.Background(), Request{Model: "m", Messages: []Message{{Role: RoleUser, Content: "hi"}}, MaxTokens: 5}, nil)
			if err != nil {
				t.Fatal(err)
			}
			if resp.DoneReason != tc.done || resp.Message.Content != tc.content {
				t.Fatalf("done %q, content %q", resp.DoneReason, resp.Message.Content)
			}
		})
	}
}

func TestAnthropicUsage(t *testing.T) {
	// input_tokens is only what came after the last cache breakpoint: the
	// prompt is that and what was read from and written to the cache.
	u := anthropicUsage(json.RawMessage(`{"input_tokens":50,"cache_read_input_tokens":1000,"cache_creation_input_tokens":200,"output_tokens":30,"output_tokens_details":{"thinking_tokens":12}}`))
	if u.PromptTokens != 1250 || u.OutputTokens != 30 || u.CachedTokens == nil || *u.CachedTokens != 1000 || u.CacheWriteTokens != 200 || u.ReasoningTokens != 12 || u.Unreported {
		t.Fatalf("usage: %+v", u)
	}
	// A cache that read nothing is a reported 0; one the API said nothing of
	// is not.
	u = anthropicUsage(json.RawMessage(`{"input_tokens":7,"cache_read_input_tokens":0,"output_tokens":1}`))
	if u.CachedTokens == nil || *u.CachedTokens != 0 || u.PromptTokens != 7 {
		t.Fatalf("usage: %+v", u)
	}
	if u := anthropicUsage(json.RawMessage(`{}`)); !u.Unreported || u.CachedTokens != nil {
		t.Fatalf("empty usage: %+v", u)
	}
	if u := anthropicUsage(json.RawMessage(`[1]`)); !u.Unreported {
		t.Fatalf("malformed usage: %+v", u)
	}
}

func TestAnthropicRefusals(t *testing.T) {
	body := `{"type":"error","error":{"type":"authentication_error","message":"invalid x-api-key"}}`
	srv := newAnthropicServer(t, http.StatusUnauthorized, body)
	a := NewAnthropic(srv.URL+"/v1", nil)
	_, err := a.Chat(context.Background(), Request{Model: "m", Messages: []Message{{Role: RoleUser, Content: "hi"}}, MaxTokens: 5}, nil)
	var se *StatusError
	var permanent permanentError
	if !errors.As(err, &se) || se.Status != 401 || se.Provider != "anthropic" || !strings.Contains(se.Body, "invalid x-api-key") {
		t.Fatalf("err: %v", err)
	}
	// A key the API does not accept is not accepted the second time either.
	if !errors.As(err, &permanent) {
		t.Fatalf("a 401 should not be retried: %v", err)
	}
	if hint := Hint(err); !strings.Contains(hint, "did not accept the key") {
		t.Fatalf("hint: %q", hint)
	}
	// Overloaded (529) is the API's to recover from: sent again.
	srv = newAnthropicServer(t, 529, `{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`)
	a = NewAnthropic(srv.URL+"/v1", nil)
	_, err = a.Chat(context.Background(), Request{Model: "m", Messages: []Message{{Role: RoleUser, Content: "hi"}}, MaxTokens: 5}, nil)
	if err == nil || errors.As(err, &permanent) {
		t.Fatalf("a 529 should be retried: %v", err)
	}
}

func TestAnthropicKeyNotSentIsRefusedBeforeTheRequest(t *testing.T) {
	srv := newAnthropicServer(t, 200, `{}`)
	a := NewAnthropic(srv.URL+"/v1", func(*http.Request) error { return Permanent(errors.New("not bound")) })
	_, err := a.Chat(context.Background(), Request{Model: "m", Messages: []Message{{Role: RoleUser, Content: "hi"}}, MaxTokens: 5}, nil)
	if err == nil || !strings.Contains(err.Error(), "not bound") || srv.path != "" {
		t.Fatalf("err %v, request %q", err, srv.path)
	}
}

func TestAnthropicDoesNotFollowRedirects(t *testing.T) {
	target := newAnthropicServer(t, 200, `{}`)
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/v1/messages", http.StatusTemporaryRedirect)
	}))
	defer redirector.Close()
	a := NewAnthropic(redirector.URL+"/v1", withKey("sk-ant-secret"))
	_, err := a.Chat(context.Background(), Request{Model: "m", Messages: []Message{{Role: RoleUser, Content: "hi"}}, MaxTokens: 5}, nil)
	var permanent permanentError
	if err == nil || !errors.As(err, &permanent) || target.path != "" {
		t.Fatalf("err %v, the redirect target saw %q", err, target.path)
	}
}

func TestAnthropicModelList(t *testing.T) {
	var asked []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked = append(asked, r.URL.RequestURI())
		if r.Header.Get("x-api-key") != "k" || r.Header.Get("anthropic-version") != "2023-06-01" {
			http.Error(w, "no key", http.StatusUnauthorized)
			return
		}
		if r.URL.Query().Get("after_id") == "" {
			_, _ = w.Write([]byte(`{"data":[{"id":"claude-a","max_input_tokens":1000000},{"id":"claude-b","max_input_tokens":null}],"has_more":true,"last_id":"claude-b"}`))
			return
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"claude-c","max_input_tokens":200000}],"has_more":false,"last_id":"claude-c"}`))
	}))
	defer srv.Close()
	a := NewAnthropic(srv.URL+"/v1", withKey("k"))
	models, err := a.Models(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 3 || models[0] != (ListedModel{"claude-a", 1000000}) || models[1] != (ListedModel{"claude-b", 0}) || models[2] != (ListedModel{"claude-c", 200000}) {
		t.Fatalf("models: %+v", models)
	}
	if len(asked) != 2 || asked[0] != "/v1/models?limit=1000" || asked[1] != "/v1/models?limit=1000&after_id=claude-b" {
		t.Fatalf("pages asked: %v", asked)
	}
	if n, err := a.ContextLength(context.Background(), "claude-c"); err != nil || n != 200000 {
		t.Fatalf("context length %d, %v", n, err)
	}
	if _, err := a.ContextLength(context.Background(), "claude-b"); err == nil || !strings.Contains(err.Error(), "does not give") {
		t.Fatalf("a model whose list entry has no window: %v", err)
	}
	if _, err := a.ContextLength(context.Background(), "claude-z"); err == nil || !strings.Contains(err.Error(), "not in the server's model list") {
		t.Fatalf("a model not listed: %v", err)
	}
	if version, err := a.Ping(context.Background()); err != nil || version == "" {
		t.Fatalf("ping: %q, %v", version, err)
	}
}

func TestAnthropicVision(t *testing.T) {
	a := NewAnthropic("https://api.anthropic.com/v1/", nil)
	if a.URL != "https://api.anthropic.com/v1" || a.Name() != "anthropic" {
		t.Fatalf("%s %s", a.URL, a.Name())
	}
	if ok, _ := a.Vision(context.Background(), "m"); ok {
		t.Fatal("a client with Images unset takes none")
	}
	a.Images = true
	if ok, _ := a.Vision(context.Background(), "m"); !ok {
		t.Fatal("a client with Images set takes them")
	}
}

// A 400 from Anthropic names what to change when it says what it refused: an
// effort asks for adaptive thinking, which older models do not have, and the
// newest models take no temperature; any other 400 is the generic advice.
func TestAnthropicHints(t *testing.T) {
	refusal := func(body string) error {
		return &StatusError{Provider: "anthropic", Status: http.StatusBadRequest, Text: "400 Bad Request", Body: body}
	}
	for name, tc := range map[string]struct{ body, want string }{
		"adaptive thinking": {`{"type":"error","error":{"type":"invalid_request_error","message":"\"thinking.type.adaptive\" is not supported for this model"}}`, "does not take adaptive thinking"},
		"temperature":       {`{"type":"error","error":{"type":"invalid_request_error","message":"temperature is deprecated for this model"}}`, "takes no temperature"},
		"anything else":     {`{"type":"error","error":{"type":"invalid_request_error","message":"messages.0.content: bad"}}`, "refused the request"},
	} {
		if got := Hint(refusal(tc.body)); !strings.Contains(got, tc.want) {
			t.Errorf("%s: hint %q, want it to say %q", name, got, tc.want)
		}
	}
	// Another provider's 400 that mentions temperature is not Anthropic's.
	other := &StatusError{Provider: "openai", Status: http.StatusBadRequest, Text: "400 Bad Request", Body: `{"error":{"message":"temperature not supported"}}`}
	if got := Hint(other); strings.Contains(got, "temperature") {
		t.Errorf("hint for another provider: %q", got)
	}
}

// A reply's signed thinking goes back with it, each block where it was
// among the calls, so the model reads the reasoning behind it again rather
// than re-deriving its design at every call (session
// 20261005T151325Z-d9519026: 14 calls in a row at about 19,000 reasoning
// tokens each). A request that carries blocks asks the API to drop, not
// refuse, one whose conversation changed (drop_block, with its beta), and
// asks for thinking as the API runs it, so it reads them. A reply keeps its
// blocks, signature and all.
func TestAnthropicThinkingGoesBackSigned(t *testing.T) {
	srv := newAnthropicServer(t, 200, `{"content":[
		{"type":"thinking","thinking":"read it first","signature":"sig-1"},
		{"type":"tool_use","id":"toolu_1","name":"fs-read","input":{"path":"a.go"}},
		{"type":"redacted_thinking","data":"opaque"},
		{"type":"tool_use","id":"toolu_2","name":"fs-read","input":{"path":"b.go"}}],
		"stop_reason":"tool_use","usage":{"input_tokens":12,"output_tokens":3}}`)
	a := NewAnthropic(srv.URL+"/v1", withKey("sk-ant-test"))
	resp, err := a.Chat(context.Background(), Request{Model: "claude-x", Messages: []Message{{Role: RoleUser, Content: "read them"}}, MaxTokens: 100}, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := []ThinkingBlock{{Type: "thinking", Thinking: "read it first", Signature: "sig-1"}, {Type: "redacted_thinking", Data: "opaque", Before: 1}}
	if len(resp.Message.Blocks) != 2 || resp.Message.Blocks[0] != want[0] || resp.Message.Blocks[1] != want[1] {
		t.Fatalf("blocks: %+v", resp.Message.Blocks)
	}
	if srv.header.Get("anthropic-beta") != "" || srv.body["thinking"] != nil {
		t.Fatalf("a request with no blocks to give back asked for %q %v", srv.header.Get("anthropic-beta"), srv.body["thinking"])
	}
	// The reply goes back in the next request, its blocks where they were.
	msgs := []Message{{Role: RoleUser, Content: "read them"}, resp.Message,
		{Role: RoleTool, Content: "a", ToolCallID: "toolu_1"}, {Role: RoleTool, Content: "b", ToolCallID: "toolu_2"}}
	if _, err := a.Chat(context.Background(), Request{Model: "claude-x", Messages: msgs, MaxTokens: 100}, nil); err != nil {
		t.Fatal(err)
	}
	if srv.header.Get("anthropic-beta") != "thinking-binding-controls-2026-08-01" {
		t.Fatalf("beta: %q", srv.header.Get("anthropic-beta"))
	}
	th := srv.body["thinking"].(map[string]any)
	if th["type"] != "adaptive" || th["block_binding"].(map[string]any)["prefix_mismatch_behavior"] != "drop_block" {
		t.Fatalf("thinking: %v", th)
	}
	blocks := srv.body["messages"].([]any)[1].(map[string]any)["content"].([]any)
	var types []string
	for _, b := range blocks {
		types = append(types, b.(map[string]any)["type"].(string))
	}
	if strings.Join(types, ",") != "thinking,tool_use,redacted_thinking,tool_use" {
		t.Fatalf("assistant blocks: %v", types)
	}
	if b := blocks[0].(map[string]any); b["thinking"] != "read it first" || b["signature"] != "sig-1" {
		t.Fatalf("thinking block: %v", b)
	}
	if b := blocks[2].(map[string]any); b["data"] != "opaque" || len(b) != 2 {
		t.Fatalf("redacted block: %v", b)
	}
	// An empty summary still goes back with its thinking field.
	msgs[1].Blocks = []ThinkingBlock{{Type: "thinking", Signature: "sig-2"}}
	if _, err := a.Chat(context.Background(), Request{Model: "claude-x", Messages: msgs, MaxTokens: 100}, nil); err != nil {
		t.Fatal(err)
	}
	b := srv.body["messages"].([]any)[1].(map[string]any)["content"].([]any)[0].(map[string]any)
	if _, ok := b["thinking"]; !ok || b["signature"] != "sig-2" {
		t.Fatalf("an empty summary: %v", b)
	}
	// A Responses model's kept reasoning is not Anthropic's to read, as when
	// a session moves from one to the other: the reply goes without it.
	msgs[1].Blocks = []ThinkingBlock{{Type: "reasoning", Data: "enc-1"}}
	if _, err := a.Chat(context.Background(), Request{Model: "claude-x", Messages: msgs, MaxTokens: 100}, nil); err != nil {
		t.Fatal(err)
	}
	if b := srv.body["messages"].([]any)[1].(map[string]any)["content"].([]any)[0].(map[string]any); b["type"] != "tool_use" || srv.body["thinking"] != nil {
		t.Fatalf("a reasoning item went to Anthropic: %v, thinking %v", b, srv.body["thinking"])
	}
}
