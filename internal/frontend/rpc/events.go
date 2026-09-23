package rpc

import (
	"sync"

	"github.com/fclaude/nibb/internal/core"
	"github.com/fclaude/nibb/internal/event"
)

// eventStream owns only live subscriptions. Durable catch-up comes from the
// session log; pending prompt snapshots come from core.
// Its lock is independent of the lifecycle lock: Close must keep draining.
type eventStream struct {
	mu          sync.Mutex
	session     *core.Session
	subscribers map[*peer]uint64 // ignore queued durable events through this seq
}
type notification struct {
	JSONRPC string `json:"jsonrpc"`
	Method  string `json:"method"`
	Params  any    `json:"params"`
}
type sessionEvent struct {
	ID      string `json:"id"`
	Kind    string `json:"kind"`
	Seq     uint64 `json:"seq"`
	Payload any    `json:"payload"`
}

func (stream *eventStream) publish(ev event.Event) {
	stream.mu.Lock()
	defer stream.mu.Unlock()
	for client, through := range stream.subscribers {
		if ev.Seq != 0 && ev.Seq <= through {
			continue
		}
		if !client.enqueue(notification{"2.0", "session.event", sessionEvent{stream.session.ID(), ev.Kind, ev.Seq, ev.Payload}}) {
			delete(stream.subscribers, client)
		}
	}
}
func (stream *eventStream) closed() {
	stream.mu.Lock()
	defer stream.mu.Unlock()
	for client := range stream.subscribers {
		client.enqueue(notification{"2.0", "session.closed", struct {
			ID string `json:"id"`
		}{stream.session.ID()}})
	}
	clear(stream.subscribers)
}

type subscription struct {
	Page    core.LogPage   `json:"page"`
	Busy    bool           `json:"busy"`
	Pending []sessionEvent `json:"pending"`
}

func (stream *eventStream) subscribe(client *peer, store core.Store, after uint64, limit int) (subscription, error) {
	stream.mu.Lock()
	defer stream.mu.Unlock()
	// Record appends before remembering and emitting. Take the committed history
	// watermark while delivery is paused, then read only that prefix from disk.
	// Later events are queued for the subscriber, including those not yet emitted.
	history := stream.session.EventsSoFar()
	var through uint64
	if len(history) > 0 {
		through = history[len(history)-1].Seq
	}
	page, err := store.ReadLog(stream.session.ID(), after, through, limit)
	if err != nil {
		return subscription{}, err
	}
	pending, err := stream.session.PendingPrompts()
	if err != nil {
		return subscription{}, err
	}
	stream.subscribers[client] = through
	result := subscription{Page: page, Busy: stream.session.Busy(), Pending: []sessionEvent{}}
	for _, ev := range pending {
		result.Pending = append(result.Pending, sessionEvent{stream.session.ID(), ev.Kind, ev.Seq, ev.Payload})
	}

	return result, nil
}
func (s *server) unsubscribe(client *peer) {
	s.watch.unsubscribe(client)
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, owned := range s.sessions {
		owned.stream.mu.Lock()
		delete(owned.stream.subscribers, client)
		owned.stream.mu.Unlock()
	}
}
