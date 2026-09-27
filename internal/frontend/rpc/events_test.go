package rpc

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fclaude/nibb/internal/core"
	"github.com/fclaude/nibb/internal/event"
	"github.com/fclaude/nibb/internal/llm"
	"github.com/fclaude/nibb/internal/log"
	fake "github.com/fclaude/nibb/test/fakes/ollama"
)

func (c *client) notice() reply {
	c.t.Helper()
	if len(c.notices) > 0 {
		r := c.notices[0]
		c.notices = c.notices[1:]
		return r
	}
	c.conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	var r reply
	if err := c.dec.Decode(&r); err != nil {
		c.t.Fatal(err)
	}
	c.observed = append(c.observed, r)
	if r.Method == "" {
		c.t.Fatal("expected notification", string(r.Result))
	}
	return r
}
func (c *client) until(kind string) []sessionEvent {
	c.t.Helper()
	var result []sessionEvent
	for {
		r := c.notice()
		if r.Method != "session.event" {
			c.t.Fatal("unexpected notification", r.Method)
		}
		var ev sessionEvent
		if err := json.Unmarshal(r.Params, &ev); err != nil {
			c.t.Fatal(err)
		}
		result = append(result, ev)
		if ev.Kind == kind {
			return result
		}
	}
}
func opened(t *testing.T, c *client, repo string) string {
	t.Helper()
	var info sessionDetails
	if err := json.Unmarshal(c.ok("session.open", map[string]any{"repo": repo}), &info); err != nil {
		t.Fatal(err)
	}
	return info.ID
}
func subscribed(t *testing.T, c *client, id string, after uint64, limit int) subscription {
	t.Helper()
	var result subscription
	if err := json.Unmarshal(c.ok("session.subscribe", map[string]any{"id": id, "after": after, "limit": limit}), &result); err != nil {
		t.Fatal(err)
	}
	return result
}
func page(t *testing.T, c *client, id string, after, through uint64, limit int) core.LogPage {
	t.Helper()
	var result core.LogPage
	if err := json.Unmarshal(c.ok("log.read", map[string]any{"id": id, "after": after, "through": through, "limit": limit}), &result); err != nil {
		t.Fatal(err)
	}
	return result
}

type heldProvider struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (*heldProvider) Name() string                         { return "held" }
func (*heldProvider) Ping(context.Context) (string, error) { return "held", nil }
func (p *heldProvider) Chat(ctx context.Context, _ llm.Request, onDelta func(llm.Delta)) (*llm.Response, error) {
	p.once.Do(func() { close(p.started) })
	select {
	case <-p.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	text := "done sk-ant-abcdefgh1234 safely"
	if onDelta != nil {
		onDelta(llm.Delta{Text: text})
	}
	return &llm.Response{Message: llm.Message{Role: llm.RoleAssistant, Content: text}}, nil
}
func waitStarted(t *testing.T, p *heldProvider) {
	t.Helper()
	select {
	case <-p.started:
	case <-time.After(5 * time.Second):
		t.Fatal("model did not start")
	}
}

func TestReconnectCombinesFixedLogPrefixAndLiveEvents(t *testing.T) {
	o := options(t)
	model := &heldProvider{started: make(chan struct{}), release: make(chan struct{})}
	o.Provider = model
	path, _ := start(t, o)
	first := connect(t, path)
	first.hello()
	id := opened(t, first, t.TempDir())
	initial := subscribed(t, first, id, 0, 1)
	first.ok("session.send", map[string]any{"id": id, "text": "hello"})
	waitStarted(t, model)
	if r := first.call("session.send", map[string]any{"id": id, "text": "overlap"}); r.Error == nil {
		t.Fatal("accepted overlapping turn")
	}
	first.conn.Close()
	second := connect(t, path)
	second.hello()
	sub := subscribed(t, second, id, initial.Page.Through, 1)
	if !sub.Busy || !sub.Page.More {
		t.Fatalf("catch-up: %+v", sub)
	}
	close(model.release)
	live := second.until(event.Status)
	// A reconnect may first see non-idle status events; drain until the durable
	// turn-end plus its final idle event, not just the first status update.
	for !hasKind(live, string(log.KindTurnEnd)) {
		live = append(live, second.until(event.Status)...)
	}
	seen := map[uint64]int{}
	for _, ev := range initial.Page.Events {
		seen[ev.Seq]++
	}
	gap := sub.Page
	for {
		for _, ev := range gap.Events {
			seen[ev.Seq]++
		}
		if !gap.More {
			break
		}
		gap = page(t, second, id, gap.Next, gap.Through, 1)
	}
	for _, ev := range live {
		if ev.ID != id {
			t.Fatal("wrong session identity")
		}
		if ev.Seq != 0 {
			seen[ev.Seq]++
		}
		raw, _ := json.Marshal(ev)
		if strings.Contains(string(raw), "sk-ant-abcdefgh1234") {
			t.Fatal("live response leaked canary")
		}
	}
	all := page(t, second, id, 0, 0, 1000)
	for _, ev := range all.Events {
		if seen[ev.Seq] != 1 {
			t.Fatalf("sequence %d delivered %d times; gap through=%d", ev.Seq, seen[ev.Seq], sub.Page.Through)
		}
		if strings.Contains(string(ev.Payload), "sk-ant-abcdefgh1234") {
			t.Fatal("log read leaked canary")
		}
	}
	// Unsubscribe stops future delivery without releasing the owned session.
	second.ok("session.unsubscribe", map[string]any{"id": id})
	second.ok("session.close", map[string]any{"id": id})
	if len(second.notices) != 0 {
		t.Fatal("notification after unsubscribe")
	}
	closed := page(t, second, id, all.Through, 0, 100)
	if len(closed.Events) != 1 || closed.Events[0].Kind != log.KindSessionEnd {
		t.Fatal("closed log missing final record", closed)
	}
}
func hasKind(events []sessionEvent, kind string) bool {
	for _, ev := range events {
		if ev.Kind == kind {
			return true
		}
	}
	return false
}

func TestPromptSurvivesReconnectAndAbortDoesNotApprove(t *testing.T) {
	model := fake.New(fake.Turn{ToolCalls: []fake.ToolCall{{ID: "write", Name: "fs-write", Args: map[string]any{"path": "blocked.txt", "content": "must not appear"}}}})
	defer model.Close()
	o := options(t)
	o.Config.Control.URL = model.URL
	o.Config.Control.Model = "test-model"
	path, _ := start(t, o)
	c := connect(t, path)
	c.hello()
	repo := t.TempDir()
	id := opened(t, c, repo)
	subscribed(t, c, id, 0, 100)
	c.ok("session.send", map[string]any{"id": id, "text": "write a file"})
	emitted := c.until(event.PromptConfirm)
	prompt := emitted[len(emitted)-1]
	c.conn.Close()
	other := connect(t, path)
	other.hello()
	sub := subscribed(t, other, id, 0, 100)
	if !sub.Busy || len(sub.Pending) != 1 || sub.Pending[0].Kind != event.PromptConfirm {
		t.Fatalf("lost pending prompt: %+v", sub)
	}
	want, _ := json.Marshal(prompt.Payload)
	got, _ := json.Marshal(sub.Pending[0].Payload)
	if string(want) != string(got) {
		t.Fatal("pending prompt changed")
	}
	if r := other.call("session.answer", map[string]any{"id": id, "prompt_id": "missing", "reply": "y"}); r.Error == nil || r.Error.Code != -32000 {
		t.Fatal("accepted stale approval")
	}
	other.ok("session.abort", map[string]any{"id": id})
	events := other.until(event.Status)
	if !hasKind(events, string(log.KindTurnEnd)) {
		t.Fatal("abort did not finish turn")
	}
	if _, err := os.Stat(filepath.Join(repo, "blocked.txt")); !os.IsNotExist(err) {
		t.Fatal("abort approved a write", err)
	}
	after := subscribed(t, other, id, 0, 100)
	if after.Busy || len(after.Pending) != 0 {
		t.Fatal("abort left a prompt", after)
	}
	other.ok("session.close", map[string]any{"id": id})
	if other.notice().Method != "session.closed" {
		t.Fatal("missing close notification")
	}
}

func TestCloseCancelsBlockedTurnAndReleasesLog(t *testing.T) {
	for range 10 {
		o := options(t)
		model := &heldProvider{started: make(chan struct{}), release: make(chan struct{})}
		o.Provider = model
		path, stop := start(t, o)
		c := connect(t, path)
		c.hello()
		id := opened(t, c, t.TempDir())
		subscribed(t, c, id, 0, 100)
		c.ok("session.send", map[string]any{"id": id, "text": "wait"})
		waitStarted(t, model)
		c.ok("session.close", map[string]any{"id": id})
		for c.notice().Method != "session.closed" {
		}
		last := page(t, c, id, 0, 0, 100)
		if last.Events[len(last.Events)-1].Kind != log.KindSessionEnd {
			t.Fatal("close returned before durable end")
		}
		resumed, err := core.Resume(o, id)
		if err != nil {
			t.Fatal(err)
		}
		resumed.Close()
		stop()
	}
}

func TestSlowSubscriberCannotBlockOtherClients(t *testing.T) {
	o := options(t)
	o.Repo = t.TempDir()
	session, err := core.New(o)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	left, right := net.Pipe()
	defer right.Close()
	slow := newPeer(left)
	slow.out = make(chan any, 1)
	go slow.write()
	fast := newPeer(nil)
	stream := &eventStream{session: session, subscribers: map[*peer]uint64{slow: 0, fast: 0}}
	done := make(chan struct{})
	go func() {
		for i := uint64(1); i <= 50; i++ {
			stream.publish(event.Event{Kind: string(log.KindNote), Seq: i, Payload: log.Note{Text: "message"}})
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("slow client blocked delivery")
	}
	select {
	case <-slow.done:
	default:
		t.Fatal("overflow did not disconnect client")
	}
	for i := uint64(1); i <= 50; i++ {
		n := (<-fast.out).(notification)
		if n.Params.(sessionEvent).Seq != i {
			t.Fatal("healthy client lost order")
		}
	}
	select {
	case <-fast.done:
		t.Fatal("healthy subscriber was disconnected")
	default:
	}
}

func TestHalfCloseStillReceivesQueuedReplies(t *testing.T) {
	path, _ := start(t, options(t))
	c := connect(t, path)
	if _, err := c.conn.Write([]byte(`[{"jsonrpc":"2.0","id":1,"method":"hello","params":{"protocol":1}},{"jsonrpc":"2.0","id":2,"method":"session.list"}]` + "\n")); err != nil {
		t.Fatal(err)
	}
	c.conn.(*net.UnixConn).CloseWrite()
	c.conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	var replies []reply
	if err := c.dec.Decode(&replies); err != nil {
		t.Fatal("half-close discarded response", err)
	}
	if len(replies) != 2 || replies[0].Error != nil || replies[1].Error != nil {
		t.Fatal("bad responses", replies)
	}
	var extra any
	if err := c.dec.Decode(&extra); err == nil {
		t.Fatal("connection did not end")
	}
}
