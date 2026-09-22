// Package jev is a scriptable fake of the Jev API (/v1/systemone) for tests.
// Each request consumes the next scripted response. Requests are recorded so
// tests can assert on what was sent (redaction, binding, content level).
package jev

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
)

// Response is one scripted Jev response.
type Response struct {
	// Verdict is returned for jev-noul calls ("plan" or "proceed").
	Verdict string
	// Score is returned for jev-score calls (0.0–1.0).
	Score float64
	// Choice is returned for jev-choice calls.
	Choice string
	// Status, if non-zero, is returned as an HTTP error.
	Status int
}

// Request is what the fake saw.
type Request struct {
	Raw       map[string]any
	AuthToken string
}

// Server is the fake.
type Server struct {
	*httptest.Server
	mu       sync.Mutex
	turns    []Response
	next     int
	Requests []Request
}

// New starts a fake with the given script.
func New(turns ...Response) *Server {
	s := &Server{turns: turns}
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/systemone", s.handle)
	s.Server = httptest.NewServer(mux)
	return s
}

// Script replaces the remaining responses.
func (s *Server) Script(turns ...Response) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.turns = turns
	s.next = 0
}

// LastRequest returns the most recently received request.
func (s *Server) LastRequest() *Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.Requests) == 0 {
		return nil
	}
	return &s.Requests[len(s.Requests)-1]
}

func (s *Server) handle(w http.ResponseWriter, r *http.Request) {
	var raw json.RawMessage
	_ = json.NewDecoder(r.Body).Decode(&raw)
	var body map[string]any
	_ = json.Unmarshal(raw, &body)
	auth := r.Header.Get("Authorization")
	req := Request{Raw: body, AuthToken: auth}

	s.mu.Lock()
	s.Requests = append(s.Requests, req)
	if s.next >= len(s.turns) {
		s.mu.Unlock()
		http.Error(w, `{"error":"fake jev: script exhausted"}`, 500)
		return
	}
	t := s.turns[s.next]
	s.next++
	s.mu.Unlock()

	if t.Status != 0 {
		http.Error(w, "fake jev: scripted status", t.Status)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	resp := map[string]any{}
	if t.Verdict != "" {
		resp["verdict"] = t.Verdict
	}
	if t.Score > 0 {
		resp["score"] = t.Score
	}
	if t.Choice != "" {
		resp["choice"] = t.Choice
	}
	_ = json.NewEncoder(w).Encode(resp)
}
