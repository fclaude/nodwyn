package rpc

import (
	"os"
	"testing"

	"github.com/fclaude/nodwyn/internal/event"
	"github.com/fclaude/nodwyn/internal/log"
)

// The wire-only assertions run on the unchanged daemon too, where latest_run
// must be rejected rather than silently returning history from the beginning.
func TestLatestRunSubscriptionKeepsItsWatermarkAndLiveEvents(t *testing.T) {
	o := options(t)
	model := &heldProvider{started: make(chan struct{}), release: make(chan struct{})}
	o.Provider = model
	if err := os.MkdirAll(o.Paths.SessionsDir, 0700); err != nil {
		t.Fatal(err)
	}
	id := "run-history-rpc"
	f, err := os.Create(log.SessionPath(o.Paths.SessionsDir, id))
	if err != nil {
		t.Fatal(err)
	}
	w := log.New(f, id)
	add := func(k log.Kind, p any) uint64 {
		t.Helper()
		ev, err := w.Append(k, p)
		if err != nil {
			t.Fatal(err)
		}
		return ev.Seq
	}
	add(log.KindSessionStart, log.SessionStart{Repo: t.TempDir()})
	var boundary uint64
	for i := 0; i < 2; i++ {
		boundary = add(log.KindRunState, log.RunState{Status: "running"})
		add(log.KindRunTurn, log.RunTurn{Number: 1})
		add(log.KindUserMsg, log.UserMsg{Text: "old request"})
		add(log.KindLLMResp, log.LLMResp{Content: "old reply"})
		add(log.KindTurnEnd, log.TurnEnd{Outcome: "completed"})
		add(log.KindRunState, log.RunState{Status: "completed", Turns: 1})
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	path, _ := start(t, o)
	c := connect(t, path)
	c.hello()
	c.ok("session.resume", map[string]any{"id": id})
	var sub struct {
		Page struct {
			Events  []log.Event `json:"events"`
			Next    uint64      `json:"next"`
			Through uint64      `json:"through"`
			Run     struct {
				Seq uint64 `json:"seq"`
			} `json:"run"`
			Earlier bool `json:"earlier"`
		}
	}
	into(t, c.ok("session.subscribe", map[string]any{"id": id, "latest_run": true, "limit": 1}), &sub)
	if sub.Page.Run.Seq != boundary || len(sub.Page.Events) != 1 || sub.Page.Events[0].Seq != boundary || !sub.Page.Earlier {
		t.Fatalf("latest subscription: %+v", sub)
	}
	c.ok("session.send", map[string]any{"id": id, "text": "one more"})
	waitStarted(t, model)
	tail := page(t, c, id, sub.Page.Next, sub.Page.Through, 100)
	if tail.Next != sub.Page.Through || tail.More {
		t.Fatalf("catchup moved its end: %+v", tail)
	}
	close(model.release)
	live := c.until(string(log.KindTurnEnd))
	expected := sub.Page.Through + 1
	for _, ev := range live {
		if ev.Seq == 0 {
			continue
		}
		if ev.Seq != expected {
			t.Fatalf("live gap or duplicate: %d want %d", ev.Seq, expected)
		}
		expected++
	}
	if expected == sub.Page.Through+1 {
		t.Fatal("no live history received")
	}
	c.until(event.Status)
	c.ok("session.close", map[string]any{"id": id})
	var closed map[string]any
	into(t, c.ok("log.read", map[string]any{"id": id, "latest_run": true}), &closed)
	if closed["run"].(map[string]any)["seq"] != float64(boundary) {
		t.Fatalf("closed history: %+v", closed)
	}
	var earlier map[string]any
	into(t, c.ok("log.runs", map[string]any{"id": id, "before": boundary, "through": sub.Page.Through, "limit": 1}), &earlier)
	if len(earlier["runs"].([]any)) != 1 || earlier["more"] != false {
		t.Fatalf("earlier: %+v", earlier)
	}
	for _, p := range []map[string]any{{"id": id, "latest_run": true, "after": 1}, {"id": id, "latest_run": "yes"}} {
		if c.call("log.read", p).Error == nil {
			t.Fatalf("accepted bad latest parameters: %+v", p)
		}
	}
	for _, p := range []map[string]any{{}, {"id": id, "limit": 101}, {"id": id, "before": 2}, {"id": "../secret"}} {
		if c.call("log.runs", p).Error == nil {
			t.Fatalf("accepted bad run cursor: %+v", p)
		}
	}
}
