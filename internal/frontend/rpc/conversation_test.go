package rpc

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/fclaude/nodwyn/internal/core"
	"github.com/fclaude/nodwyn/internal/log"
)

// This uses only the old wire client and log types, so running the test on
// unchanged code can demonstrate the missing methods rather than a compile error.
func TestPeopleSearchConversationsAndReadContextWithoutResuming(t *testing.T) {
	o := options(t)
	repo := t.TempDir()
	seed := func(text, project string) string {
		opts := o
		opts.Repo = project
		s, err := core.New(opts)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = s.Record(log.KindUserMsg, log.UserMsg{Text: text}); err != nil {
			t.Fatal(err)
		}
		if _, err = s.Record(log.KindLLMResp, log.LLMResp{Content: "needle matching reply"}); err != nil {
			t.Fatal(err)
		}
		if _, err = s.Record(log.KindTurnEnd, log.TurnEnd{Outcome: "completed"}); err != nil {
			t.Fatal(err)
		}
		id := s.ID()
		if err = s.Close(); err != nil {
			t.Fatal(err)
		}
		return id
	}
	anchor := seed("first needle", repo)
	other := seed("second needle", repo)
	unrelated := seed("third needle", t.TempDir())
	path, _ := start(t, o)
	c := connect(t, path)
	c.hello()
	phone := connect(t, path)
	phone.ok("hello", map[string]any{"protocol": 1, "via": "web:phone"})
	for _, client := range []*client{c, phone} {
		var result struct {
			Tasks []struct {
				ID    string `json:"id"`
				Turns []struct {
					Matches []struct {
						Seq uint64 `json:"seq"`
					} `json:"matches"`
				} `json:"turns"`
			} `json:"tasks"`
		}
		into(t, client.ok("conversation.search", map[string]any{"id": anchor, "query": "needle", "scope": "earlier"}), &result)
		if len(result.Tasks) != 1 || result.Tasks[0].ID != other || result.Tasks[0].ID == unrelated {
			t.Fatalf("%+v", result)
		}
		seq := result.Tasks[0].Turns[0].Matches[0].Seq
		var context struct {
			From, Through uint64
			Page          struct {
				Events []log.Event `json:"events"`
			} `json:"page"`
		}
		into(t, client.ok("log.context", map[string]any{"id": other, "seq": seq}), &context)
		if len(context.Page.Events) == 0 || context.From > seq || context.Through < seq {
			t.Fatalf("%+v", context)
		}
	}
	listed := c.ok("session.list", map[string]any{})
	var sessions []struct {
		Open bool `json:"open"`
	}
	if err := json.Unmarshal(listed, &sessions); err != nil {
		t.Fatal(err)
	}
	for _, s := range sessions {
		if s.Open {
			t.Fatal("a read resumed a task")
		}
	}
	if strings.Contains(string(c.ok("workspace.list", map[string]any{})), `"earlier_tasks":true`) {
		t.Fatal("a human search changed model authority")
	}
	for _, p := range []map[string]any{{"id": anchor, "query": "needle", "repo": repo}, {"id": anchor, "query": "needle", "scope": "repo"}, {"id": anchor, "query": "needle", "limit": 21}, {"id": anchor, "query": "---"}, {"id": "../x", "query": "needle"}} {
		if r := c.call("conversation.search", p); r.Error == nil {
			t.Fatalf("accepted %+v", p)
		}
	}
	for _, p := range []map[string]any{{"id": anchor, "seq": 0}, {"id": anchor, "seq": 999999}, {"id": anchor, "seq": 1, "after": 0}} {
		if r := c.call("log.context", p); r.Error == nil {
			t.Fatalf("accepted %+v", p)
		}
	}
}
