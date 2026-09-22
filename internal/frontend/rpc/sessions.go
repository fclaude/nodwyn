package rpc

import (
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/fclaude/nibb/internal/budget"
	"github.com/fclaude/nibb/internal/core"
	"github.com/fclaude/nibb/internal/llm"
)

type ownedSession struct {
	session *core.Session
	drained chan struct{}
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
	ID      string     `json:"id"`
	Repo    string     `json:"repo"`
	Busy    bool       `json:"busy"`
	Tools   []llm.Tool `json:"tools"`
	Budgets budgetInfo `json:"budgets"`
}

func (s *server) details(session *core.Session) sessionDetails {
	limits, _ := s.options.Config.Budget.Limits(s.options.Config.Loop.MaxToolCalls)
	return sessionDetails{session.ID(), session.Repo(), session.Busy(), session.Tools(), budgetInfo{limits.ToolCalls, limits.Tokens, limits.Actions, limits.Wall}}
}
func (s *server) dispatch(req request, ready *bool) (any, *rpcError) {
	if req.Method == "hello" {
		var p struct {
			Protocol int `json:"protocol"`
		}
		if err := params(req.Params, &p, "protocol"); err != nil {
			return nil, err
		}
		if p.Protocol != ProtocolVersion {
			return nil, failure(-32001, "Unsupported protocol version")
		}
		*ready = true
		return struct {
			Protocol int      `json:"protocol"`
			Version  string   `json:"version"`
			Methods  []string `json:"methods"`
		}{ProtocolVersion, s.options.Version, methods}, nil
	}
	if !*ready {
		return nil, failure(-32001, "hello required before session methods")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	store := core.Store{Dir: s.options.Paths.SessionsDir}
	switch req.Method {
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
		var p struct {
			Repo string `json:"repo"`
		}
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
	case "session.resume", "session.info", "session.close":
		var p struct {
			ID string `json:"id"`
		}
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
	default:
		return nil, failure(-32601, "Method not found")
	}
}
func (s *server) own(session *core.Session) {
	drained := make(chan struct{})
	s.sessions[session.ID()] = ownedSession{session, drained}
	// Lifecycle-only protocol: drain initialization records so a session never
	// depends on a connected client. Durable records remain in its log. Event
	// delivery and reconnect catch-up belong to the next protocol slice.
	go func() {
		defer close(drained)
		for range session.Events() {
		}
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
