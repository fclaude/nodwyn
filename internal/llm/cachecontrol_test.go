package llm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// A Claude model reached through a chat-completions server caches a prompt
// only when the request asks (ADR 9, 2026-10-05): the ask is the request's
// top-level cache_control, in the shapes OpenRouter documents, and a request
// from a model that did not set it carries no such field, since a server that
// does not know it may refuse the request.
func TestOpenAIAsksTheProviderToCacheOnlyWhenTold(t *testing.T) {
	for _, tc := range []struct {
		setting string
		want    string // the cache_control field as JSON; "" when it is not sent
	}{
		{"", ""},
		{"ephemeral", `{"type":"ephemeral"}`},
		{"1h", `{"type":"ephemeral","ttl":"1h"}`},
	} {
		var body map[string]json.RawMessage
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			raw, _ := io.ReadAll(r.Body)
			if err := json.Unmarshal(raw, &body); err != nil {
				t.Error(err)
			}
			_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
		}))
		o := NewOpenAI(srv.URL, nil)
		o.CacheControl = tc.setting
		if _, err := o.Chat(context.Background(), Request{Model: "m", Messages: []Message{{Role: RoleUser, Content: "hi"}}}, nil); err != nil {
			t.Fatal(err)
		}
		srv.Close()
		got, sent := body["cache_control"]
		if tc.want == "" && sent {
			t.Errorf("%q: cache_control sent as %s", tc.setting, got)
		}
		if tc.want != "" && string(got) != tc.want {
			t.Errorf("%q: cache_control is %s, want %s", tc.setting, got, tc.want)
		}
	}
}

// What the ask changes is the wire, not the request: its hash, and so its
// replay, are the same with and without it.
func TestCacheControlIsNotPartOfTheRequestsHash(t *testing.T) {
	raw, err := json.Marshal(Request{Model: "m", Messages: []Message{{Role: RoleUser, Content: "hi"}}})
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	for name := range fields {
		if name == "CacheControl" || name == "cache_control" {
			t.Fatalf("the provider-neutral request carries %s: %s", name, raw)
		}
	}
}
