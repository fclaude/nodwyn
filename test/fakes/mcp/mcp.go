// Package mcp is a fake MCP server for tests and demos, answering
// initialize, tools/list and tools/call from a fixed tool list: over
// streamable HTTP (New), as JSON or as server-sent events, or over stdio
// (NewStdio and Serve), one message per line. It records every call and
// the Authorization header it saw.
package mcp

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
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

// Server is the fake. Over stdio it has no HTTP server.
type Server struct {
	*httptest.Server
	mu    sync.Mutex
	tools []Tool
	SSE   bool // answer requests as event streams
	Calls []Call
	Auth  []string // Authorization headers seen, one per request
}

// New starts a fake with tools, over HTTP.
func New(tools ...Tool) *Server {
	s := &Server{tools: tools}
	s.Server = httptest.NewServer(http.HandlerFunc(s.handle))
	return s
}

// NewStdio prepares a fake with tools for Serve.
func NewStdio(tools ...Tool) *Server { return &Server{tools: tools} }

// SetTools replaces the tool list, as a server update would.
func (s *Server) SetTools(tools ...Tool) {
	s.mu.Lock()
	s.tools = tools
	s.mu.Unlock()
}

// Recorded returns the calls so far.
func (s *Server) Recorded() []Call {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Call(nil), s.Calls...)
}

type request struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
}

// answer is the result of one request; ok is false for an unknown session.
func (s *Server) answer(req request, auth string, session bool) (any, bool) {
	s.mu.Lock()
	tools := s.tools
	s.mu.Unlock()
	switch req.Method {
	case "initialize":
		return map[string]any{"protocolVersion": "2025-06-18", "serverInfo": map[string]string{"name": "fake", "version": "1"}, "capabilities": map[string]any{"tools": map[string]any{}}}, true
	case "tools/list":
		if !session {
			return nil, false
		}
		var list []map[string]any
		for _, t := range tools {
			schema := t.Schema
			if schema == nil {
				schema = map[string]any{"type": "object", "properties": map[string]any{}}
			}
			list = append(list, map[string]any{"name": t.Name, "description": t.Description, "inputSchema": schema})
		}
		return map[string]any{"tools": list}, true
	case "tools/call":
		var p struct {
			Name      string         `json:"name"`
			Arguments map[string]any `json:"arguments"`
		}
		json.Unmarshal(req.Params, &p)
		s.mu.Lock()
		s.Calls = append(s.Calls, Call{Name: p.Name, Args: p.Arguments, Auth: auth})
		s.mu.Unlock()
		text, isError := fmt.Sprintf("no tool %s", p.Name), true
		for _, t := range tools {
			if t.Name == p.Name && t.Reply != nil {
				text, isError = t.Reply(p.Arguments)
			}
		}
		return map[string]any{"content": []map[string]any{{"type": "text", "text": text}}, "isError": isError}, true
	}
	return nil, true
}

func (s *Server) handle(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	var req request
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	s.Auth = append(s.Auth, r.Header.Get("Authorization"))
	sse := s.SSE
	s.mu.Unlock()
	if req.ID == nil { // a notification
		w.WriteHeader(http.StatusAccepted)
		return
	}
	if req.Method == "initialize" {
		w.Header().Set("Mcp-Session-Id", "fake-session")
	}
	result, ok := s.answer(req, r.Header.Get("Authorization"), r.Header.Get("Mcp-Session-Id") == "fake-session")
	if !ok {
		http.Error(w, "no session", http.StatusBadRequest)
		return
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

// Serve speaks MCP's stdio transport on in and out until in ends: one
// message per line. Before each answer it sends a notification and a ping
// of its own, as a real server may; the ping's answer is ignored.
func (s *Server) Serve(in io.Reader, out io.Writer) error {
	lines := bufio.NewScanner(in)
	lines.Buffer(make([]byte, 64<<10), 8<<20)
	initialized := false
	for lines.Scan() {
		var req request
		if err := json.Unmarshal(lines.Bytes(), &req); err != nil {
			continue
		}
		if req.ID == nil || req.Method == "" {
			continue // a notification, or the answer to the fake's ping
		}
		if req.Method == "initialize" {
			initialized = true
		}
		result, _ := s.answer(req, "", initialized)
		reply, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
		chatter := "{\"jsonrpc\":\"2.0\",\"method\":\"notifications/message\",\"params\":{\"level\":\"info\",\"data\":\"working\"}}\n{\"jsonrpc\":\"2.0\",\"id\":\"fake-ping\",\"method\":\"ping\"}\n"
		if _, err := io.WriteString(out, chatter+string(reply)+"\n"); err != nil {
			return err
		}
	}
	return lines.Err()
}
