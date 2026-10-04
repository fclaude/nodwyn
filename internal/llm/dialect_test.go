package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// A response's spelling does not choose its history spelling. The second
// request must retain reasoning and tool results in the server's dialect.
func TestChatDialectReasoningRoundTrip(t *testing.T) {
	for _, tc := range []struct {
		name    string
		dialect ChatDialect
		field   string
	}{
		{"legacy", "", "reasoning_content"},
		{"compatible", ChatCompatible, "reasoning_content"},
		{"cerebras", ChatCerebras, "reasoning"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, responseField := range []string{"reasoning", "reasoning_content"} {
				t.Run(responseField, func(t *testing.T) {
					calls := 0
					srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						calls++
						var body struct {
							Messages []map[string]json.RawMessage `json:"messages"`
						}
						if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
							t.Error(err)
							http.Error(w, "invalid JSON", 400)
							return
						}
						if calls == 1 {
							if len(body.Messages) != 1 || body.Messages[0]["reasoning"] != nil || body.Messages[0]["reasoning_content"] != nil {
								t.Errorf("initial messages: %s", body.Messages)
							}
							if _, err := w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":null,"` + responseField + `":"inspect first","tool_calls":[{"id":"c1","type":"function","function":{"name":"fs-list","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`)); err != nil {
								t.Error(err)
							}
							return
						}
						if calls != 2 || len(body.Messages) != 3 {
							t.Errorf("unexpected request %d: %s", calls, body.Messages)
							http.Error(w, "unexpected history", 400)
							return
						}
						wrong := "reasoning"
						if tc.field == wrong {
							wrong = "reasoning_content"
						}
						m := body.Messages[1]
						if string(m[tc.field]) != `"inspect first"` || m[wrong] != nil || string(body.Messages[2]["tool_call_id"]) != `"c1"` {
							t.Errorf("history: %s", body.Messages)
							http.Error(w, "unsupported reasoning history", 400)
							return
						}
						if _, err := w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"done"},"finish_reason":"stop"}]}`)); err != nil {
							t.Error(err)
						}
					}))
					defer srv.Close()
					o := NewOpenAI(srv.URL, nil)
					o.Dialect = tc.dialect
					req := Request{Model: "fake", Messages: []Message{{Role: RoleUser, Content: "list"}}}
					first, err := o.Chat(context.Background(), req, nil)
					if err != nil {
						t.Fatal(err)
					}
					if first.Message.Thinking != "inspect first" || len(first.Message.ToolCalls) != 1 {
						t.Fatalf("reply: %+v", first)
					}
					req.Messages = append(req.Messages, first.Message, Message{Role: RoleTool, ToolCallID: "c1", Content: "a.go"})
					before, err := json.Marshal(req)
					if err != nil {
						t.Fatal(err)
					}
					last, err := o.Chat(context.Background(), req, nil)
					if err != nil {
						t.Fatal(err)
					}
					after, err := json.Marshal(req)
					if err != nil {
						t.Fatal(err)
					}
					if !bytes.Equal(before, after) {
						t.Fatal("encoding changed the canonical request")
					}
					if last.Message.Content != "done" || calls != 2 {
						t.Fatalf("reply: %+v, requests: %d", last, calls)
					}
				})
			}
		})
	}
}

func TestUnknownChatDialectRefusesBeforeAuthorization(t *testing.T) {
	o := NewOpenAI("http://unused.invalid", func(*http.Request) error {
		t.Error("an unknown dialect reached authorization")
		return nil
	})
	o.Dialect = "typo"
	if _, err := o.Chat(context.Background(), Request{}, nil); err == nil {
		t.Fatal("unknown dialect was accepted")
	}
}
