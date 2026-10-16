package llm

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// opaqueBlob stands in for the encrypted reasoning a provider sends: n's own
// 4 KB of base64, with the + / = a real blob has.
func opaqueBlob(n int) string {
	sum := sha256.Sum256([]byte{byte(n)})
	var b []byte
	for len(b) < 3000 {
		b = append(b, sum[:]...)
		sum = sha256.Sum256(sum[:])
	}
	return base64.StdEncoding.EncodeToString(b)
}

// detailsServer answers each chat completion with the next of replies, each an
// assistant message as JSON, and keeps every request's body as it came.
func detailsServer(t *testing.T, replies ...string) (*httptest.Server, func() [][]byte) {
	t.Helper()
	var mu sync.Mutex
	var bodies [][]byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		defer mu.Unlock()
		bodies = append(bodies, b)
		if len(bodies) > len(replies) {
			http.Error(w, `{"error":{"message":"script exhausted"}}`, http.StatusInternalServerError)
			return
		}
		w.Write([]byte(`{"choices":[{"message":` + replies[len(bodies)-1] + `,"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`))
	}))
	t.Cleanup(srv.Close)
	return srv, func() [][]byte {
		mu.Lock()
		defer mu.Unlock()
		return append([][]byte(nil), bodies...)
	}
}

// wireMessages are a request body's messages, each field as the JSON it was
// sent as.
func wireMessages(t *testing.T, body []byte) []map[string]json.RawMessage {
	t.Helper()
	var req struct {
		Messages []map[string]json.RawMessage `json:"messages"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		t.Fatal(err)
	}
	return req.Messages
}

// OpenRouter gives a reasoning model's reasoning as reasoning_details on the
// reply, and wants them back unchanged on that reply in every later request,
// or the model loses its reasoning between tool rounds. Over three rounds of
// calls, each request carries every earlier reply's array byte for byte, in
// order, on that reply and no other, escapes and all, and not its thinking
// text as well. A request to another model, or on a dialect that does not
// keep them, carries none, and its replies' thinking goes as it did before
// (ADR 9, amended 2026-10-15).
func TestOpenAIReasoningDetailsGoBackOnTheirReply(t *testing.T) {
	details := []string{
		`[{"type":"reasoning.summary","summary":"List the files first.","id":"rs_1","format":"unknown","index":0},{"type":"reasoning.encrypted","data":"` + opaqueBlob(1) + `","id":"rs_1","format":"unknown","index":1}]`,
		`[{"type":"reasoning.encrypted","data":"` + opaqueBlob(2) + `","format":"unknown","index":0}]`,
		`[{"type":"reasoning.text","text":"Line one\nline \u00e9 two, café","signature":"` + opaqueBlob(3)[:64] + `","format":"anthropic-claude-v1","index":0},{"type":"reasoning.encrypted","data":"` + opaqueBlob(4) + `","index":1}]`,
	}
	var replies []string
	for i, d := range details {
		replies = append(replies, fmt.Sprintf(`{"role":"assistant","content":null,"reasoning":"round %d","reasoning_details":%s,"tool_calls":[{"id":"c%d","type":"function","function":{"name":"fs-list","arguments":"{}"}}]}`, i+1, d, i+1))
	}
	replies = append(replies, `{"role":"assistant","content":"done"}`, `{"role":"assistant","content":"other"}`, `{"role":"assistant","content":"cerebras"}`)
	srv, bodies := detailsServer(t, replies...)
	o := NewOpenAI(srv.URL+"/v1", nil)
	o.ReasoningOrigin = "openrouter m"
	msgs := []Message{{Role: RoleSystem, Content: "sys"}, {Role: RoleUser, Content: "list the files"}}
	for round := 0; round <= len(details); round++ {
		resp, err := o.Chat(context.Background(), Request{Model: "m", Messages: msgs}, nil)
		if err != nil {
			t.Fatal(err)
		}
		msgs = append(msgs, resp.Message)
		for _, tc := range resp.Message.ToolCalls {
			msgs = append(msgs, Message{Role: RoleTool, Content: "a.go", ToolName: tc.Name, ToolCallID: tc.ID})
		}
	}
	sent := bodies()
	if len(sent) != len(details)+1 {
		t.Fatalf("%d requests", len(sent))
	}
	for i, body := range sent {
		replies := 0
		for j, m := range wireMessages(t, body) {
			got, thinking := m["reasoning_details"], m["reasoning_content"]
			if string(m["role"]) != `"assistant"` {
				if got != nil {
					t.Errorf("request %d: message %d (%s) carries reasoning_details %s", i, j, m["role"], got)
				}
				continue
			}
			if string(got) != details[replies] {
				t.Errorf("request %d: reply %d carries reasoning_details\n%s\nwant\n%s", i, replies, got, details[replies])
			}
			if thinking != nil {
				t.Errorf("request %d: reply %d carries its thinking %s beside its reasoning_details", i, replies, thinking)
			}
			replies++
		}
		if replies != i {
			t.Errorf("request %d carried %d replies, want %d", i, replies, i)
		}
	}

	// The same conversation to another model, and on Cerebras's dialect: no
	// reasoning_details, and each reply's thinking as that dialect sends it.
	other := NewOpenAI(srv.URL+"/v1", nil)
	other.ReasoningOrigin = "openrouter other"
	cerebras := NewOpenAI(srv.URL+"/v1", nil)
	cerebras.Dialect, cerebras.ReasoningOrigin = ChatCerebras, "openrouter m"
	for _, tc := range []struct {
		o     *OpenAI
		field string
	}{{other, "reasoning_content"}, {cerebras, "reasoning"}} {
		if _, err := tc.o.Chat(context.Background(), Request{Model: "m", Messages: msgs}, nil); err != nil {
			t.Fatal(err)
		}
		all := bodies()
		body := all[len(all)-1]
		if strings.Contains(string(body), "reasoning_details") {
			t.Errorf("origin %q, dialect %q: reasoning_details were sent:\n%s", tc.o.ReasoningOrigin, tc.o.Dialect, body)
		}
		replies := 0
		for _, m := range wireMessages(t, body) {
			if string(m["role"]) == `"assistant"` && replies < len(details) {
				replies++
				if want := fmt.Sprintf(`"round %d"`, replies); string(m[tc.field]) != want {
					t.Errorf("origin %q, dialect %q: reply %d's %s is %s, want %s", tc.o.ReasoningOrigin, tc.o.Dialect, replies, tc.field, m[tc.field], want)
				}
			}
		}
	}
}

// A reply without reasoning_details, or with null or an empty array, keeps
// nothing, so a conversation without them is sent exactly as before; one
// reply that carried them changes only that reply in the request: its array
// where its thinking was.
func TestOpenAIRequestWithoutReasoningDetailsIsUnchanged(t *testing.T) {
	srv, bodies := detailsServer(t,
		`{"role":"assistant","content":"a","reasoning":"r"}`,
		`{"role":"assistant","content":"b","reasoning":"r","reasoning_details":null}`,
		`{"role":"assistant","content":"c","reasoning":"r","reasoning_details":[]}`,
		`{"role":"assistant","content":"ok"}`,
		`{"role":"assistant","content":"ok"}`,
	)
	o := NewOpenAI(srv.URL+"/v1", nil)
	o.ReasoningOrigin = "openrouter m"
	for i := 0; i < 3; i++ {
		resp, err := o.Chat(context.Background(), Request{Model: "m", Messages: []Message{{Role: RoleUser, Content: "hi"}}}, nil)
		if err != nil {
			t.Fatal(err)
		}
		if len(resp.Message.Blocks) != 0 || resp.Message.Thinking != "r" {
			t.Errorf("reply %d kept %+v", i, resp.Message)
		}
	}
	reply := Message{Role: RoleAssistant, Thinking: "list first", ToolCalls: []ToolCall{{ID: "c1", Name: "fs-list", Args: json.RawMessage(`{"path":"."}`)}}}
	conversation := func(reply Message) []Message {
		return []Message{{Role: RoleSystem, Content: "sys"}, {Role: RoleUser, Content: "list"}, reply, {Role: RoleTool, Content: "a.go", ToolName: "fs-list", ToolCallID: "c1"}}
	}
	if _, err := o.Chat(context.Background(), Request{Model: "m", Messages: conversation(reply), MaxTokens: 100}, nil); err != nil {
		t.Fatal(err)
	}
	const before = `{"model":"m","messages":[{"role":"system","content":"sys"},{"role":"user","content":"list"},` +
		`{"role":"assistant","content":"","tool_calls":[{"id":"c1","type":"function","function":{"name":"fs-list","arguments":"{\"path\":\".\"}"}}],"reasoning_content":"list first"},` +
		`{"role":"tool","content":"a.go","tool_call_id":"c1"}],"stream":false,"max_completion_tokens":100}`
	sent := bodies()
	if got := string(sent[3]); got != before {
		t.Errorf("a request without reasoning_details changed:\n%s\nwant\n%s", got, before)
	}
	details := `[{"type":"reasoning.encrypted","data":"` + opaqueBlob(5) + `","format":"unknown","index":0}]`
	reply.Blocks = []ThinkingBlock{{Type: "reasoning_details", Data: details, Origin: "openrouter m"}}
	if _, err := o.Chat(context.Background(), Request{Model: "m", Messages: conversation(reply), MaxTokens: 100}, nil); err != nil {
		t.Fatal(err)
	}
	want := strings.Replace(before, `"reasoning_content":"list first"`, `"reasoning_details":`+details, 1)
	if got := string(bodies()[4]); got != want {
		t.Errorf("a reply's reasoning_details went as\n%s\nwant\n%s", got, want)
	}
}
