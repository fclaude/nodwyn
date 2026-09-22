// Package tui presents sessions without owning interpreter or policy state.
package tui

import (
	"context"
	"encoding/json"
	"sync"

	"github.com/fclaude/nibb/internal/core"
	"github.com/fclaude/nibb/internal/frontend/rpc"
	"github.com/fclaude/nibb/internal/log"
)

type snapshot struct {
	ID, Repo string
	Busy     bool
	History  []log.Event
	Pending  []rpc.ClientEvent
}

// backend is the small set of operations these views need. Core and RPC retain
// ownership of sessions, prompts and authorization. Only the RPC adapter retries
// a connection; neither adapter retries a submitted operation.
type backend interface {
	List(context.Context) ([]rpc.SessionInfo, error)
	Attach(context.Context, string, string) (snapshot, error)
	Next(context.Context) (rpc.ClientEvent, error)
	Send(context.Context, string, string, bool) error
	Answer(context.Context, string, string, string) error
	Grant(context.Context, string, string, string) error
	Secret(context.Context, string, string, []byte) error
	Abort(context.Context, string) error
	Close()
}

type local struct {
	mu      sync.Mutex
	options core.Options
	session *core.Session
	events  chan rpc.ClientEvent
	stop    chan struct{}
	once    sync.Once
}

func newLocal(o core.Options) *local {
	return &local{options: o, events: make(chan rpc.ClientEvent, 256), stop: make(chan struct{})}
}
func (l *local) List(context.Context) ([]rpc.SessionInfo, error) {
	items, err := (core.Store{Dir: l.options.Paths.SessionsDir}).List()
	var out []rpc.SessionInfo
	for _, s := range items {
		out = append(out, rpc.SessionInfo{ID: s.ID, Repo: s.Repo, FirstMessage: s.FirstMessage, LastEvent: s.LastEvent, InFlight: s.InFlight, Truncated: s.Truncated})
	}
	return out, err
}
func (l *local) Attach(ctx context.Context, repo, id string) (snapshot, error) {
	if err := ctx.Err(); err != nil {
		return snapshot{}, err
	}
	select {
	case <-l.stop:
		return snapshot{}, context.Canceled
	default:
	}
	o := l.options
	o.Repo = repo
	var s *core.Session
	var err error
	if id == "" {
		s, err = core.New(o)
	} else {
		o.Repo = ""
		s, err = core.Resume(o, id)
	}
	if err != nil {
		return snapshot{}, err
	}
	go func() {
		defer close(l.events)
		for ev := range s.Events() {
			raw, err := json.Marshal(ev.Payload)
			if err != nil {
				continue
			}
			select {
			case l.events <- rpc.ClientEvent{ID: s.ID(), Kind: ev.Kind, Seq: ev.Seq, Payload: raw}:
			case <-l.stop:
			}
		}
	}()

	l.mu.Lock()
	select {
	case <-l.stop:
		l.mu.Unlock()
		s.Close()
		return snapshot{}, context.Canceled
	default:
		l.session = s
		l.mu.Unlock()
	}
	return snapshot{ID: s.ID(), Repo: s.Repo(), History: s.EventsSoFar(), Busy: s.Busy()}, nil
}
func (l *local) Next(ctx context.Context) (rpc.ClientEvent, error) {
	select {
	case ev, ok := <-l.events:
		if !ok {
			return rpc.ClientEvent{}, context.Canceled
		}
		return ev, nil
	case <-ctx.Done():
		return rpc.ClientEvent{}, ctx.Err()
	}
}
func (l *local) Send(_ context.Context, _, text string, eval bool) error {
	if eval {
		return l.session.StartEval(text, "terminal")
	}
	return l.session.Send(text)
}
func (l *local) Answer(_ context.Context, _, prompt, reply string) error {
	return l.session.AnswerFrom(prompt, reply, "terminal")
}
func (l *local) Grant(_ context.Context, _, prompt, scope string) error {
	return l.session.CreateApproval(prompt, scope, "terminal")
}
func (l *local) Secret(_ context.Context, _, prompt string, value []byte) error {
	return l.session.SecretReplyFrom(prompt, value, "terminal")
}
func (l *local) Abort(context.Context, string) error { l.session.Abort(); return nil }
func (l *local) Close() {
	l.once.Do(func() {
		l.mu.Lock()
		defer l.mu.Unlock()
		close(l.stop)
		if l.session != nil {
			l.session.Close()
		}
	})
}
