package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
)

// responsesServer is a strict Responses endpoint: like OpenAI's, it refuses a
// request to any other path and a parameter it does not know, such as the
// chat-completions messages or reasoning_effort, so a request in the wrong
// protocol fails instead of passing. Like a plan-authenticated one, it refuses
// a system message in the input, in its own {"detail": ...} shape. It records
// each body and gives reply.
func responsesServer(t *testing.T, reply string) (*httptest.Server, *[]map[string]json.RawMessage) {
	t.Helper()
	known := map[string]bool{"model": true, "instructions": true, "input": true, "tools": true, "stream": true, "store": true, "max_output_tokens": true,
		"temperature": true, "prompt_cache_key": true, "reasoning": true, "include": true}
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
		var input []struct {
			Role string `json:"role"`
		}
		json.Unmarshal(body["input"], &input)
		for _, item := range input {
			if item.Role == "system" {
				http.Error(w, `{"detail":"System messages are not allowed"}`, http.StatusBadRequest)
				return
			}
		}
		bodies = append(bodies, body)
		w.Write([]byte(reply))
	}))
	t.Cleanup(srv.Close)
	return srv, &bodies
}

// A conversation goes out as Responses input items: the system prompt as the
// request's instructions, messages, a reply's calls after its text, and each
// result as its call's output, with the tools
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
		"model":        `"gpt-6.1-sol"`,
		"instructions": `"sys"`,
		"input": `[{"type":"message","role":"user","content":[{"type":"input_text","text":"look"},{"type":"input_image","image_url":"data:image/png;base64,AQI="}]},` +
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
		"stream":            `true`,
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

// A model that keeps its reasoning asks for it encrypted, keeps it on the
// reply under its origin, and gives back what came from that origin, each
// item before the call it came before, with earlier turns' left in what the
// model reads. Reasoning from another origin, Anthropic's blocks, and
// everything without an origin stay out (ADR 9, amended 2026-10-07).
func TestResponsesKeptReasoningGoesBackWhereItCame(t *testing.T) {
	srv, bodies := responsesServer(t, `{"status":"completed","output":[
		{"type":"reasoning","id":"rs_1","summary":[{"type":"summary_text","text":"look"},{"type":"summary_text","text":"first"}],"encrypted_content":"enc-new"},
		{"type":"function_call","id":"fc_1","call_id":"c9","name":"fs-list","arguments":"{}"}],
		"usage":{"input_tokens":12,"output_tokens":3}}`)
	reply := Message{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "c1", Name: "fs-list"}, {ID: "c2", Name: "fs-read"}}, Blocks: []ThinkingBlock{
		{Type: "reasoning", Thinking: "list it", Data: "enc-1", Origin: "plan gpt"},
		{Type: "reasoning", Data: "enc-other", Origin: "api gpt"},
		{Type: "thinking", Thinking: "claude", Signature: "sig"},
		{Type: "reasoning", Data: "enc-2", Before: 1, Origin: "plan gpt"},
	}}
	req := Request{Model: "gpt-6.1-sol", Messages: []Message{
		{Role: RoleUser, Content: "look"},
		reply,
		{Role: RoleTool, Content: "a.go", ToolCallID: "c1"},
		{Role: RoleTool, Content: "package a", ToolCallID: "c2"},
	}}
	for _, origin := range []string{"plan gpt", ""} {
		o := NewOpenAI(srv.URL+"/v1", nil)
		o.Dialect = ChatResponses
		o.ReasoningOrigin = origin
		resp, err := o.Chat(context.Background(), req, nil)
		if err != nil {
			t.Fatal(err)
		}
		got := (*bodies)[len(*bodies)-1]
		calls := `{"type":"function_call","call_id":"c1","name":"fs-list","arguments":"{}"},`
		second := `{"type":"function_call","call_id":"c2","name":"fs-read","arguments":"{}"},`
		input, include, reasoning := `[{"type":"message","role":"user","content":"look"},`+calls+second, "", ""
		blocks := []ThinkingBlock(nil)
		if origin != "" {
			input = `[{"type":"message","role":"user","content":"look"},` +
				`{"type":"reasoning","summary":[{"type":"summary_text","text":"list it"}],"encrypted_content":"enc-1"},` + calls +
				`{"type":"reasoning","summary":[],"encrypted_content":"enc-2"},` + second
			include, reasoning = `["reasoning.encrypted_content"]`, `{"context":"all_turns"}`
			blocks = []ThinkingBlock{{Type: "reasoning", Thinking: "look\n\nfirst", Data: "enc-new", Origin: "plan gpt"}}
		}
		input += `{"type":"function_call_output","call_id":"c1","output":"a.go"},{"type":"function_call_output","call_id":"c2","output":"package a"}]`
		if string(got["input"]) != input || string(got["include"]) != include || string(got["reasoning"]) != reasoning {
			t.Errorf("origin %q: input %s\ninclude %s, reasoning %s", origin, got["input"], got["include"], got["reasoning"])
		}
		if !slices.Equal(resp.Message.Blocks, blocks) || resp.Message.Thinking != "look\n\nfirst" || len(resp.Message.ToolCalls) != 1 {
			t.Errorf("origin %q: reply %+v", origin, resp.Message)
		}
	}
}

// The system prompt is the request's instructions and never an input message,
// which a plan-authenticated server refuses (session
// 20261007T152356Z-a337f2a9); a request without one sends no instructions.
func TestResponsesSystemPromptIsInstructions(t *testing.T) {
	srv, bodies := responsesServer(t, `{"status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"hi"}]}]}`)
	o := NewOpenAI(srv.URL+"/v1", nil)
	o.Dialect = "responses"
	for _, tc := range []struct {
		name     string
		messages []Message
		want     string // the instructions sent; "" sends none
	}{
		{"one", []Message{{Role: RoleSystem, Content: "be brief"}, {Role: RoleUser, Content: "hi"}}, `"be brief"`},
		{"several are all kept", []Message{{Role: RoleSystem, Content: "a"}, {Role: RoleUser, Content: "hi"}, {Role: RoleSystem, Content: "b"}}, `"a\n\nb"`},
		{"none", []Message{{Role: RoleUser, Content: "hi"}}, ""},
	} {
		*bodies = nil
		if _, err := o.Chat(context.Background(), Request{Model: "m", Messages: tc.messages}, nil); err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if got := string((*bodies)[0]["instructions"]); got != tc.want {
			t.Errorf("%s: instructions %s, want %s", tc.name, got, tc.want)
		}
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

// A split credential stays buffered until the terminal response. Function
// calls and usage come from that completed response, rather than deltas.
func TestResponsesEventStreamBuffersUntilCompleted(t *testing.T) {
	terminal := `{"type":"response.completed","response":{"status":"completed","output":[{"type":"message","content":[{"text":"canary-secret"}]},{"type":"function_call","call_id":"c1","name":"read","arguments":"{}"}],"usage":{"input_tokens":4,"output_tokens":2}}}`
	var deltas []Delta
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		if body["stream"] != true || body["store"] != false {
			t.Error("Responses did not request a non-stored stream")
			return
		}
		w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
		fmt.Fprint(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"canary-\"}\r\n\r\n")
		w.(http.Flusher).Flush()
		fmt.Fprint(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"secret\"}\n\n")
		fmt.Fprintf(w, "event: response.completed\ndata: %s\n\n", terminal)
	}))
	defer srv.Close()
	o := NewOpenAI(srv.URL, nil)
	o.Dialect = ChatResponses
	out, err := o.Chat(context.Background(), Request{Model: "test"}, func(d Delta) { deltas = append(deltas, d) })
	if err != nil {
		t.Fatal(err)
	}
	if len(deltas) != 1 || deltas[0].Text != "canary-secret" || out.DoneReason != "tool_calls" || len(out.Message.ToolCalls) != 1 || out.Message.ToolCalls[0].ID != "c1" || out.Usage.OutputTokens != 2 {
		t.Fatalf("output: %+v deltas: %+v", out, deltas)
	}
}

// A stream is read as one whatever its Content-Type says, as a ChatGPT plan's
// server sent one that nodwyn read as JSON (session 20261007T165642Z-97ee33d1,
// "invalid character 'e'"); a reply that opens as an object is still JSON.
func TestResponsesStreamIsToldByItsBody(t *testing.T) {
	stream := "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"status\":\"in_progress\"}}\n\n" +
		"event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"content\":[{\"text\":\"ok\"}]}]}}\n\n"
	object := "\n  {\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"content\":[{\"text\":\"ok\"}]}]}"
	for _, tc := range []struct{ contentType, body string }{
		{"", stream},
		{"application/json", stream},
		{"text/plain; charset=utf-8", stream},
		{"application/json", object},
		{"", object},
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// An empty value is sent as no header at all, not sniffed.
			w.Header()["Content-Type"] = nil
			if tc.contentType != "" {
				w.Header().Set("Content-Type", tc.contentType)
			}
			fmt.Fprint(w, tc.body)
		}))
		o := NewOpenAI(srv.URL, nil)
		o.Dialect = ChatResponses
		out, err := o.Chat(context.Background(), Request{Model: "test"}, nil)
		srv.Close()
		if err != nil || out.Message.Content != "ok" {
			t.Errorf("Content-Type %q, body %.20q: %+v, %v", tc.contentType, tc.body, out, err)
		}
	}
}

// A stream whose terminal response has an empty output, as a ChatGPT plan's
// server ends one, is the items it finished one by one, in their order: every
// reply in session 20261007T170623Z-89018e45 read as empty. A terminal output
// that has items is the reply, whatever the stream finished before it.
func TestResponsesStreamItemsStandInForAnEmptyOutput(t *testing.T) {
	event := func(v string) string { return "data: " + v + "\n\n" }
	items := event(`{"type":"response.created","response":{"status":"in_progress","output":[]}}`) +
		event(`{"type":"response.output_item.added","output_index":0,"item":{"type":"reasoning","summary":[]}}`) +
		event(`{"type":"response.output_item.done","output_index":0,"item":{"type":"reasoning","summary":[{"type":"summary_text","text":"look first"}]}}`) +
		event(`{"type":"response.output_item.done","output_index":2,"item":{"type":"function_call","call_id":"c1","name":"fs-list","arguments":"{\"path\":\".\"}"}}`) +
		event(`{"type":"response.output_text.delta","output_index":1,"delta":"listing"}`) +
		event(`{"type":"response.output_item.done","output_index":1,"item":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"listing"}]}}`)
	for _, tc := range []struct {
		name, terminal, content, thinking string
		calls                             int
	}{
		{"empty output", `{"type":"response.completed","response":{"status":"completed","output":[],"usage":{"input_tokens":9,"output_tokens":5}}}`, "listing", "look first", 1},
		{"output given", `{"type":"response.completed","response":{"status":"completed","output":[{"type":"message","content":[{"text":"final"}]}],"usage":{"input_tokens":9,"output_tokens":5}}}`, "final", "", 0},
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, items+event(tc.terminal))
		}))
		o := NewOpenAI(srv.URL, nil)
		o.Dialect = ChatResponses
		out, err := o.Chat(context.Background(), Request{Model: "test"}, nil)
		srv.Close()
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		m := out.Message
		if m.Content != tc.content || m.Thinking != tc.thinking || len(m.ToolCalls) != tc.calls || out.Usage.OutputTokens != 5 {
			t.Errorf("%s: %+v", tc.name, out)
		}
		if tc.calls > 0 && len(m.ToolCalls) > 0 && (m.ToolCalls[0].ID != "c1" || m.ToolCalls[0].Name != "fs-list" || string(m.ToolCalls[0].Args) != `{"path":"."}` || out.DoneReason != "tool_calls") {
			t.Errorf("%s: call %+v, done %q", tc.name, m.ToolCalls, out.DoneReason)
		}
	}
}

func TestResponsesEventStreamRejectsMissingOrFailedTerminal(t *testing.T) {
	for _, body := range []string{
		"data: {\"type\":\"response.output_text.delta\",\"delta\":\"partial\"}\n\n",
		"data: [DONE]\n\n",
		"data: {\"type\":\"response.failed\",\"response\":{\"status\":\"failed\",\"error\":{\"message\":\"quota refused\"}}}\n\n",
		"data: {\"type\":\"error\"}\n\n",
		"data: invalid\n\n",
		"data: {\"type\":\"response.completed\",\"response\":{\"status\":\"in_progress\"}}\n\n",
	} {
		t.Run(body, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, body)
			}))
			defer srv.Close()
			o := NewOpenAI(srv.URL, nil)
			o.Dialect = ChatResponses
			want := "Responses stream ended without a terminal response"
			switch {
			case strings.Contains(body, "response.failed"):
				want = "the response failed: quota refused"
			case strings.Contains(body, `"type":"error"`):
				want = "Responses stream reported an error"
			case strings.Contains(body, "invalid"):
				want = "malformed Responses stream event"
			case strings.Contains(body, "in_progress"):
				want = "Responses terminal event has the wrong status"
			}
			_, err := o.Chat(context.Background(), Request{Model: "test"}, func(Delta) { t.Error("partial output emitted") })
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("want %q, got %v", want, err)
			}
		})
	}
}
