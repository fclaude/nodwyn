// Package mcp is a fake remote MCP server for tests and demos: streamable
// HTTP, answering initialize, tools/list and tools/call from a fixed tool
// list, as JSON or as server-sent events. It records every call and the
// Authorization header it saw.
package mcp

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
)

// Tool is a tool the fake offers; Reply builds its text result.
type Tool struct {
	Name        string
	Description string
	Schema      map[string]any
	Reply       func(args map[string]any) (string, bool)
}

// Call is a tools/call the fake received.
type Call struct {
	Name string
	Args map[string]any
	Auth string
}

// Server is the fake.
type Server struct {
	*httptest.Server
	mu    sync.Mutex
	tools []Tool
	SSE   bool // answer requests as event streams
	Calls []Call
	Auth  []string // Authorization headers seen, one per request
}

// New starts a fake with tools.
func New(tools ...Tool) *Server {
	s := &Server{tools: tools}
	s.Server = httptest.NewServer(http.HandlerFunc(s.handle))
	return s
}

// SetTools replaces the tool list, as a server update would.
func (s *Server) SetTools(tools ...Tool) {
	s.mu.Lock()
	s.tools = tools
	s.mu.Unlock()
}

func (s *Server) handle(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	s.Auth = append(s.Auth, r.Header.Get("Authorization"))
	tools := s.tools
	sse := s.SSE
	s.mu.Unlock()
	if req.ID == nil { // a notification
		w.WriteHeader(http.StatusAccepted)
		return
	}
	var result any
	switch req.Method {
	case "initialize":
		w.Header().Set("Mcp-Session-Id", "fake-session")
		result = map[string]any{"protocolVersion": "2025-06-18", "serverInfo": map[string]string{"name": "fake", "version": "1"}, "capabilities": map[string]any{"tools": map[string]any{}}}
	case "tools/list":
		if r.Header.Get("Mcp-Session-Id") != "fake-session" {
			http.Error(w, "no session", http.StatusBadRequest)
			return
		}
		var list []map[string]any
		for _, t := range tools {
			schema := t.Schema
			if schema == nil {
				schema = map[string]any{"type": "object", "properties": map[string]any{}}
			}
			list = append(list, map[string]any{"name": t.Name, "description": t.Description, "inputSchema": schema})
		}
		result = map[string]any{"tools": list}
	case "tools/call":
		var p struct {
			Name      string         `json:"name"`
			Arguments map[string]any `json:"arguments"`
		}
		json.Unmarshal(req.Params, &p)
		s.mu.Lock()
		s.Calls = append(s.Calls, Call{Name: p.Name, Args: p.Arguments, Auth: r.Header.Get("Authorization")})
		s.mu.Unlock()
		text, isError := fmt.Sprintf("no tool %s", p.Name), true
		for _, t := range tools {
			if t.Name == p.Name && t.Reply != nil {
				text, isError = t.Reply(p.Arguments)
			}
		}
		result = map[string]any{"content": []map[string]any{{"type": "text", "text": text}}, "isError": isError}
	default:
		result = nil
	}
	reply, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
	if sse {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "event: message\ndata: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/progress\",\"params\":{}}\n\n")
		fmt.Fprintf(w, "event: message\ndata: %s\n\n", reply)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(reply)
}
