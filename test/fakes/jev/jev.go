// Package jev is a scriptable fake of TypeSafe's System One API
// (POST /v1/systemone, docs.typesafe.ai/api) for tests. Each request
// consumes the next scripted answer, rendered in the documented shape for
// the question type the request carried. Requests are recorded so tests can
// assert on what was sent (redaction, model, auth header).
package jev

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
)

// Answer is one scripted answer; the fields used depend on the question
// type the request asks.
type Answer struct {
	Noul   float64            // noul: probability of yes
	Score  float64            // score: raw position on the rubric
	Choice string             // choice: the option
	Probs  map[string]float64 // choice: probabilities (optional)
	Status int                // if non-zero, an HTTP error instead
}

// Request is what the fake saw.
type Request struct {
	State     string
	Model     string
	Questions map[string]map[string]any
	Auth      string
	Raw       []byte
}

// Server is the fake.
type Server struct {
	*httptest.Server
	mu       sync.Mutex
	script   []Answer
	next     int
	Requests []Request
}

// New starts a fake with the given script.
func New(script ...Answer) *Server {
	s := &Server{script: script}
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/systemone", s.handle)
	s.Server = httptest.NewServer(mux)
	return s
}

// Script replaces the remaining answers.
func (s *Server) Script(script ...Answer) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.script = script
	s.next = 0
}

// LastRequest returns the most recent request, or nil.
func (s *Server) LastRequest() *Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.Requests) == 0 {
		return nil
	}
	r := s.Requests[len(s.Requests)-1]
	return &r
}

func (s *Server) handle(w http.ResponseWriter, r *http.Request) {
	var body struct {
		State     json.RawMessage           `json:"state"`
		Model     string                    `json:"model"`
		Questions map[string]map[string]any `json:"questions"`
	}
	raw, _ := readAll(r)
	_ = json.Unmarshal(raw, &body)
	var state string
	_ = json.Unmarshal(body.State, &state)
	req := Request{State: state, Model: body.Model, Questions: body.Questions, Auth: r.Header.Get("Authorization"), Raw: raw}

	s.mu.Lock()
	s.Requests = append(s.Requests, req)
	if s.next >= len(s.script) {
		s.mu.Unlock()
		http.Error(w, `{"error":"fake jev: script exhausted"}`, 500)
		return
	}
	a := s.script[s.next]
	s.next++
	s.mu.Unlock()

	if a.Status != 0 {
		http.Error(w, `{"error":"scripted"}`, a.Status)
		return
	}
	answers := map[string]any{}
	for id, q := range body.Questions {
		switch q["type"] {
		case "noul":
			answers[id] = map[string]any{"type": "noul", "noul": a.Noul}
		case "score":
			answers[id] = map[string]any{"type": "score", "score": a.Score, "confidence": 0.9}
		case "choice":
			probs := a.Probs
			if probs == nil {
				probs = map[string]float64{a.Choice: 1}
			}
			answers[id] = map[string]any{"type": "choice", "choice": a.Choice, "confidence": 0.9, "probabilities": probs}
		}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"model":   body.Model,
		"answers": answers,
		"usage":   map[string]int{"input_tokens": len(raw) / 4, "output_tokens": 8},
	})
}

func readAll(r *http.Request) ([]byte, error) {
	var buf []byte
	tmp := make([]byte, 4096)
	for {
		n, err := r.Body.Read(tmp)
		buf = append(buf, tmp[:n]...)
		if err != nil {
			return buf, nil
		}
	}
}
