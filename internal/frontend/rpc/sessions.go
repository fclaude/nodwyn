package rpc

import (
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/fclaude/nibb/internal/binding"
	"github.com/fclaude/nibb/internal/budget"
	"github.com/fclaude/nibb/internal/core"
	"github.com/fclaude/nibb/internal/llm"
)

type ownedSession struct {
	session *core.Session
	drained chan struct{}
	stream  *eventStream
}

// Lifecycle operations are serialized. This keeps construction, ownership and
// closing atomic across clients without teaching core about connections.
type server struct {
	options  core.Options
	mu       sync.Mutex
	sessions map[string]ownedSession
}
type sessionInfo struct {
	ID           string    `json:"id"`
	Repo         string    `json:"repo"`
	Open         bool      `json:"open"`
	Busy         bool      `json:"busy"`
	FirstMessage string    `json:"first_message"`
	LastEvent    time.Time `json:"last_event"`
	InFlight     bool      `json:"in_flight"`
	Truncated    bool      `json:"truncated"`
}
type budgetInfo struct {
	ToolCalls budget.Limit `json:"tool_calls"`
	Tokens    budget.Limit `json:"tokens"`
	Actions   budget.Limit `json:"actions"`
	Wall      budget.Limit `json:"wall_ns"`
}
type sessionDetails struct {
	Bindings   []binding.Binding `json:"bindings"`
	ID         string            `json:"id"`
	Repo       string            `json:"repo"`
	Busy       bool              `json:"busy"`
	Tools      []llm.Tool        `json:"tools"`
	Budgets    budgetInfo        `json:"budgets"`
	Commands   []string          `json:"allowed_commands"`
	Writes     []string          `json:"allowed_writes"`
	TurnWrites bool              `json:"turn_writes"`
	TurnActor  string            `json:"turn_actor,omitempty"`
}

func (s *server) details(session *core.Session) sessionDetails {
	limits, _ := s.options.Config.Budget.Limits(s.options.Config.Loop.MaxToolCalls)
	trust, actor := session.TurnApproval()
	return sessionDetails{Bindings: session.Bindings(), ID: session.ID(), Repo: session.Repo(), Busy: session.Busy(), Tools: session.Tools(), Budgets: budgetInfo{limits.ToolCalls, limits.Tokens, limits.Actions, limits.Wall}, Commands: session.Allowed(), Writes: session.AllowedWrites(), TurnWrites: trust, TurnActor: actor}
}
func (s *server) dispatch(req request, client *peer) (any, *rpcError) {
	if req.Method == "hello" {
		var p helloParams
		if err := params(req.Params, &p, "protocol"); err != nil {
			return nil, err
		}
		if p.Protocol != ProtocolVersion {
			return nil, failure(-32001, "Unsupported protocol version")
		}
		client.ready = true
		return helloResult{ProtocolVersion, s.options.Version, methods, client.actor}, nil
	}
	if !client.ready {
		return nil, failure(-32001, "hello required before session methods")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	store := core.Store{Dir: s.options.Paths.SessionsDir}
	switch req.Method {
	case "session.policy.read", "session.policy.write":
		return s.policy(req, client)
	case "session.answer", "session.secret", "session.approval.create", "session.binding.create":
		return s.answer(req, client)
	case "session.list":
		if err := params(req.Params, &struct{}{}); err != nil {
			return nil, err
		}
		infos, err := store.List()
		if err != nil {
			return nil, failure(-32000, err.Error())
		}
		result := make([]sessionInfo, 0, len(infos))
		for _, i := range infos {
			owned, open := s.sessions[i.ID]
			busy := open && owned.session.Busy()
			result = append(result, sessionInfo{i.ID, i.Repo, open, busy, i.FirstMessage, i.LastEvent, i.InFlight, i.Truncated})
		}
		return result, nil
	case "session.open":
		var p openParams
		if err := params(req.Params, &p, "repo"); err != nil {
			return nil, err
		}
		if !filepath.IsAbs(p.Repo) {
			return nil, failure(-32602, "repo must be an absolute directory")
		}
		info, err := os.Stat(p.Repo)
		if err != nil || !info.IsDir() {
			return nil, failure(-32602, "repo must be an existing directory")
		}
		options := s.options
		options.Repo = p.Repo
		session, err := core.New(options)
		if err != nil {
			return nil, failure(-32000, err.Error())
		}
		s.own(session)
		return s.details(session), nil
	case "session.resume", "session.info", "session.close", "session.abort", "session.unsubscribe", "session.friction":
		var p idParams
		if err := params(req.Params, &p, "id"); err != nil {
			return nil, err
		}
		if p.ID == "" {
			return nil, failure(-32602, "id is required")
		}
		owned, open := s.sessions[p.ID]
		if req.Method == "session.resume" && !open {
			options := s.options
			options.Repo = ""
			session, err := core.Resume(options, p.ID)
			if err != nil {
				return nil, failure(-32000, err.Error())
			}
			s.own(session)
			return s.details(session), nil
		}
		if !open {
			return nil, failure(-32002, "Session is not open in this daemon")
		}
		if req.Method == "session.friction" {
			operation, err := owned.session.ReadFriction(client.actor)
			if err != nil {
				return nil, failure(-32000, err.Error())
			}
			return inspectionStarted{operation}, nil
		}
		if req.Method == "session.abort" {
			owned.session.Abort()
			return struct{}{}, nil
		}
		if req.Method == "session.unsubscribe" {
			owned.stream.mu.Lock()
			delete(owned.stream.subscribers, client)
			owned.stream.mu.Unlock()
			return struct{}{}, nil
		}
		if req.Method == "session.close" {
			err := owned.session.Close()
			<-owned.drained
			delete(s.sessions, p.ID)
			if err != nil {
				return nil, failure(-32000, err.Error())
			}
			return struct{}{}, nil
		}
		return s.details(owned.session), nil
	case "session.eval":
		var p evalParams
		if err := params(req.Params, &p, "id", "source"); err != nil {
			return nil, err
		}
		if p.ID == "" || p.Source == "" {
			return nil, failure(-32602, "id and nonempty source are required")
		}
		owned, ok := s.sessions[p.ID]
		if !ok {
			return nil, failure(-32002, "Session is not open in this daemon")
		}
		if err := owned.session.StartEval(p.Source, client.actor); err != nil {
			return nil, failure(-32000, err.Error())
		}
		return struct{}{}, nil
	case "session.send":
		var p sendParams
		if err := params(req.Params, &p, "id", "text"); err != nil {
			return nil, err
		}
		if p.ID == "" || p.Text == "" {
			return nil, failure(-32602, "id and nonempty text are required")
		}
		owned, ok := s.sessions[p.ID]
		if !ok {
			return nil, failure(-32002, "Session is not open in this daemon")
		}
		if err := owned.session.Send(p.Text); err != nil {
			return nil, failure(-32000, err.Error())
		}
		return struct{}{}, nil
	case "log.read", "session.subscribe":
		var p readParams
		names := []string{"id", "after", "limit"}
		if req.Method == "log.read" {
			names = append(names, "through")
		}
		if err := params(req.Params, &p, names...); err != nil {
			return nil, err
		}
		if p.ID == "" {
			return nil, failure(-32602, "id is required")
		}
		if p.Limit == 0 {
			p.Limit = 100
		}
		if p.Limit < 1 || p.Limit > 1000 {
			return nil, failure(-32602, "limit must be between 1 and 1000")
		}
		if req.Method == "session.subscribe" {
			owned, ok := s.sessions[p.ID]
			if !ok {
				return nil, failure(-32002, "Session is not open in this daemon")
			}
			result, err := owned.stream.subscribe(client, store, p.After, p.Limit)
			if err != nil {
				return nil, failure(-32000, err.Error())
			}
			return result, nil
		}
		page, err := store.ReadLog(p.ID, p.After, p.Through, p.Limit)
		if err != nil {
			return nil, failure(-32000, err.Error())
		}
		return page, nil
	default:
		return nil, failure(-32601, "Method not found")
	}
}
func (s *server) own(session *core.Session) {
	drained := make(chan struct{})
	stream := &eventStream{session: session, subscribers: make(map[*peer]uint64)}
	s.sessions[session.ID()] = ownedSession{session: session, drained: drained, stream: stream}
	go func() {
		defer close(drained)
		for ev := range session.Events() {
			stream.publish(ev)
		}
		stream.closed()
	}()
}
func (s *server) close() error {
	var first error
	for id, owned := range s.sessions {
		if err := owned.session.Close(); err != nil && first == nil {
			first = err
		}
		<-owned.drained
		delete(s.sessions, id)
	}
	return first
}
