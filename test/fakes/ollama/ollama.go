// Package ollama is a scriptable fake of Ollama's native /api/chat for tests:
// each request consumes the next scripted turn and streams it back in the
// same chunked NDJSON shape the real server uses. Requests are recorded so
// tests can assert on what the loop sent (prefix stability, tool results).
package ollama

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
)

// Turn is one scripted model output.
type Turn struct {
	Thinking  string
	Content   string
	ToolCalls []ToolCall
	// Chunk splits Content into fragments of this many bytes (0 = one chunk).
	Chunk int
	// Error, if set, is returned as an error chunk instead of the message.
	Error string
	// Status, if non-zero, is returned as a plain HTTP error.
	Status int
	// DoneReason, if set, replaces the stop reason ("stop" or "tool_calls"),
	// such as "length" for a reply cut at the output limit.
	DoneReason string
}

// ToolCall is a scripted tool request.
type ToolCall struct {
	ID   string
	Name string
	Args map[string]any
}

// Request is what the fake saw.
type Request struct {
	Model    string            `json:"model"`
	Messages []json.RawMessage `json:"messages"`
	Tools    []json.RawMessage `json:"tools"`
	Options  map[string]any    `json:"options"`
	Raw      []byte            `json:"-"`
}

// Server is the fake.
type Server struct {
	*httptest.Server
	mu       sync.Mutex
	turns    []Turn
	next     int
	respond  func(Request) Turn
	Requests []Request
}

// NewFunc starts a fake whose every reply is chosen by respond from the
// request, for demos and tests that do not know how many requests a turn
// makes (a planner request, for instance).
func NewFunc(respond func(Request) Turn) *Server {
	s := New()
	s.respond = respond
	return s
}

// New starts a fake with the given script.
func New(turns ...Turn) *Server {
	s := &Server{turns: turns}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/version", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"version": "fake"})
	})
	mux.HandleFunc("/api/chat", s.chat)
	mux.HandleFunc("/api/tags", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"models": []map[string]string{{"name": "fake"}, {"name": "demo"}}})
	})
	s.Server = httptest.NewServer(mux)
	return s
}

// Script replaces the remaining turns.
func (s *Server) Script(turns ...Turn) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.turns = turns
	s.next = 0
}

func (s *Server) chat(w http.ResponseWriter, r *http.Request) {
	var req Request
	dec := json.NewDecoder(r.Body)
	var raw json.RawMessage
	if err := dec.Decode(&raw); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	_ = json.Unmarshal(raw, &req)
	req.Raw = raw
	s.mu.Lock()
	s.Requests = append(s.Requests, req)
	var t Turn
	if s.respond != nil {
		s.mu.Unlock()
		t = s.respond(req)
	} else if s.next >= len(s.turns) {
		s.mu.Unlock()
		http.Error(w, `{"error":"fake ollama: script exhausted"}`, 500)
		return
	} else {
		t = s.turns[s.next]
		s.next++
		s.mu.Unlock()
	}

	if t.Status != 0 {
		http.Error(w, "fake ollama: scripted status", t.Status)
		return
	}
	w.Header().Set("Content-Type", "application/x-ndjson")
	enc := json.NewEncoder(w)
	flush := func() {
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	}
	if t.Error != "" {
		_ = enc.Encode(map[string]any{"error": t.Error})
		return
	}
	msg := func(content, thinking string) map[string]any {
		return map[string]any{"role": "assistant", "content": content, "thinking": thinking}
	}
	if t.Thinking != "" {
		_ = enc.Encode(map[string]any{"model": req.Model, "message": msg("", t.Thinking), "done": false})
		flush()
	}
	content := t.Content
	for len(content) > 0 {
		n := len(content)
		if t.Chunk > 0 && t.Chunk < n {
			n = t.Chunk
		}
		_ = enc.Encode(map[string]any{"model": req.Model, "message": msg(content[:n], ""), "done": false})
		flush()
		content = content[n:]
	}
	if len(t.ToolCalls) > 0 {
		var calls []map[string]any
		for i, tc := range t.ToolCalls {
			args := tc.Args
			if args == nil {
				args = map[string]any{}
			}
			calls = append(calls, map[string]any{
				"id":       tc.ID,
				"function": map[string]any{"index": i, "name": tc.Name, "arguments": args},
			})
		}
		m := msg("", "")
		m["tool_calls"] = calls
		_ = enc.Encode(map[string]any{"model": req.Model, "message": m, "done": false})
		flush()
	}
	reason := "stop"
	if len(t.ToolCalls) > 0 {
		reason = "tool_calls"
	}
	if t.DoneReason != "" {
		reason = t.DoneReason
	}
	_ = enc.Encode(map[string]any{
		"model": req.Model, "message": msg("", ""), "done": true, "done_reason": reason,
		"total_duration": int64(1500e6), "load_duration": int64(10e6),
		"prompt_eval_count": 279 + 10*len(req.Messages), "prompt_eval_duration": int64(120e6),
		"eval_count": 44, "eval_duration": int64(1400e6),
	})
}
