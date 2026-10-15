// Package tui presents sessions without owning interpreter or policy state.
package tui

import (
	"context"
	"encoding/json"
	"sync"

	"github.com/fclaude/nodwyn/internal/budget"
	"github.com/fclaude/nodwyn/internal/core"
	"github.com/fclaude/nodwyn/internal/frontend/rpc"
	"github.com/fclaude/nodwyn/internal/llm"
	"github.com/fclaude/nodwyn/internal/log"
)

type snapshot struct {
	ID, Repo string
	// Project is the folder the session's notes, repository index and
	// earlier tasks belong to: the repository of a worktree's session.
	Project string
	// Branch is the session's own branch when it works in a git worktree
	// (ADR 34), which the title shows.
	Branch  string
	Busy    bool
	History []log.Event
	Pending []rpc.ClientEvent
}

// backend is the small set of operations these views need. Core and RPC retain
// ownership of sessions, prompts and authorization. Only the RPC adapter retries
// a connection; neither adapter retries a submitted operation.
type backend interface {
	List(context.Context) ([]rpc.SessionInfo, error)
	// Attach opens a session: the stored one id names, or with id "" a new
	// one in repo. OpenWorktree opens a new one in a git worktree of repo, on
	// a branch of its own (ADR 34).
	Attach(context.Context, string, string) (snapshot, error)
	OpenWorktree(context.Context, string) (snapshot, error)
	Next(context.Context) (rpc.ClientEvent, error)
	// Models says which model the conversation talks to and which it may be
	// moved to; SetModel moves it, by a choice's name and an effort (ADR 41).
	Models(context.Context, string) (modelView, error)
	SetModel(context.Context, string, string, string) (core.ModelState, error)
	// Judges says which judge the task asks and which it may ask instead;
	// SetJudge moves it, by a judge's name or "" for none (ADR 47).
	Judges(context.Context, string) (judgeView, error)
	SetJudge(context.Context, string, string) (core.JudgeState, error)
	// AttachFile adds a file the person named to the conversation (ADR 22).
	// An image's confirm arrives as a prompt while it waits.
	AttachFile(context.Context, string, string) (log.Attach, error)
	Send(context.Context, string, string, bool) error
	Answer(context.Context, string, string, string) error
	Grant(context.Context, string, string, string) error
	Bind(context.Context, string, string) error
	Secret(context.Context, string, string, []byte) error
	Abort(context.Context, string) error
	History(context.Context, string) ([]log.Event, error)
	Policy(context.Context, string) (policyView, error)
	Tools(context.Context, string) ([]llm.Tool, error)
	Budgets(context.Context, string) (budget.Limits, error)
	// Retrieval says what a project keeps for the model's searches (ADR
	// 50): its repository index, and whether its earlier tasks are
	// searched, which SetEarlierTasks changes; ForgetIndex deletes the index.
	Retrieval(context.Context, string) (retrievalView, error)
	SetEarlierTasks(context.Context, string, bool) error
	ForgetIndex(context.Context, string) error
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
		out = append(out, rpc.SessionInfo{ID: s.ID, Repo: s.Repo, FirstMessage: s.FirstMessage, LastEvent: s.LastEvent, InFlight: s.InFlight, Truncated: s.Truncated, Worktree: s.Worktree})
	}
	return out, err
}
func (l *local) Attach(ctx context.Context, repo, id string) (snapshot, error) {
	o := l.options
	o.Repo = repo
	if id == "" {
		return l.start(ctx, func() (*core.Session, error) { return core.New(o) })
	}
	o.Repo = ""
	return l.start(ctx, func() (*core.Session, error) { return core.Resume(o, id) })
}
func (l *local) OpenWorktree(ctx context.Context, repo string) (snapshot, error) {
	o := l.options
	o.Repo, o.Worktree = repo, true
	return l.start(ctx, func() (*core.Session, error) { return core.New(o) })
}

// start makes the session this process owns and forwards its events.
func (l *local) start(ctx context.Context, open func() (*core.Session, error)) (snapshot, error) {
	if err := ctx.Err(); err != nil {
		return snapshot{}, err
	}
	select {
	case <-l.stop:
		return snapshot{}, context.Canceled
	default:
	}
	s, err := open()
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
	// The whole conversation, from the log: a resumed session holds only
	// its end (ADR 49 §10).
	history, err := s.History()
	if err != nil {
		s.Close()
		return snapshot{}, err
	}

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
	result := snapshot{ID: s.ID(), Repo: s.Repo(), Project: s.Project(), History: history, Busy: s.Busy()}
	if w := s.Worktree(); w != nil {
		result.Branch = w.Branch
	}
	return result, nil
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

// AttachFile waits for an image's confirm, which reaches this view as a
// prompt and is answered through Answer meanwhile.
func (l *local) AttachFile(_ context.Context, _, path string) (log.Attach, error) {
	return l.session.Attach(path, "", "terminal")
}
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

func (l *local) Bind(_ context.Context, _, prompt string) error {
	return l.session.CreateBinding(prompt, "terminal")
}
