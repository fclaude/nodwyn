// Package chat is a fake chat-completions provider for tests and the browser
// demo (ADR 41): the shape OpenAI, OpenRouter, Cerebras and vLLM share. It
// lists its models, checks the bearer key, and refuses what a real provider
// refuses: a wrong key (401), a model it does not have (404), and a
// reasoning_effort for a model that does not take one, or a word it does not
// know (400), so a test shows that a request carried exactly the effort
// chosen, and none where none was meant. Every request is recorded; replies
// echo the model and effort that asked, so a conversation shows which one
// answered. Hold keeps replies waiting until Release, to look at a response
// that is still running.
//
// It caches the way a Claude route does (ADR 9, amended 2026-10-05): a prompt
// is read from the cache only if the request asks for it with a top-level
// cache_control, and then the part of it that a request that also asked had
// in front is reported as cached_tokens, the rest as cache_write_tokens;
// without cache_control nothing is cached, however stable the prefix. Prompts
// under a thousand tokens are not cached, as Claude's are not. A token is a
// quarter of a byte of a message's JSON, which is only a count: this fake
// proves what the request asks for, not what any provider does with it.
//
// The debug paths /_fake/requests, /_fake/hold and /_fake/release give a
// program in another process (a browser check) the same view.
package chat

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
)

// Model is a model the fake offers.
type Model struct {
	ID      string
	Context int      // context_length in the list; 0 leaves it out, as a provider that does not say
	Efforts []string // the reasoning_effort values it accepts; none: the field is refused
	// Caches says it is a route that caches when asked (a Claude model): a
	// cache_control for any other model is refused, as a provider refuses a
	// field it does not know.
	Caches bool
}

// Request is one chat completion the fake saw.
type Request struct {
	Model string `json:"model"`
	// Effort is the reasoning_effort sent; HasEffort tells "none" from "".
	Effort    string `json:"effort"`
	HasEffort bool   `json:"has_effort"`
	// Authenticated says the right key came with it; the key is not kept.
	Authenticated bool `json:"authenticated"`
	Messages      int  `json:"messages"`
	// CacheControl is the top-level cache_control sent, as JSON; empty when
	// the request sent none.
	CacheControl string `json:"cache_control"`
	// PromptTokens, CachedTokens and CacheWriteTokens are what the reply
	// reported of its prompt.
	PromptTokens     int `json:"prompt_tokens"`
	CachedTokens     int `json:"cached_tokens"`
	CacheWriteTokens int `json:"cache_write_tokens"`
	// Answered is the status the fake gave.
	Answered int `json:"answered"`
}

// minCache is the fewest prompt tokens Claude caches.
const minCache = 1024

// Server is the fake provider.
type Server struct {
	*httptest.Server
	// Key is the bearer key every request needs; empty needs none.
	Key string
	// ListNeedsKey makes GET /v1/models refuse a request without the key, as
	// OpenAI does; OpenRouter's list is public.
	ListNeedsKey bool

	mu       sync.Mutex
	models   []Model
	requests []Request
	hold     chan struct{} // replies wait on it while it is not nil
	waiting  int
	// cached is the last cache entry written: the messages of the last
	// request that asked for caching, as JSON.
	cached []string
}

// New starts a fake provider offering models, whose requests need key.
func New(key string, models ...Model) *Server {
	s := &Server{Key: key, models: models}
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/models", s.list)
	mux.HandleFunc("/v1/chat/completions", s.complete)
	mux.HandleFunc("/_fake/requests", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(s.Requests())
	})
	mux.HandleFunc("/_fake/hold", func(w http.ResponseWriter, _ *http.Request) { s.Hold(); w.WriteHeader(http.StatusNoContent) })
	mux.HandleFunc("/_fake/release", func(w http.ResponseWriter, _ *http.Request) { s.Release(); w.WriteHeader(http.StatusNoContent) })
	mux.HandleFunc("/_fake/waiting", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(s.Waiting())
	})
	s.Server = httptest.NewServer(mux)
	return s
}

// V1 is the base URL a configuration names, including the version.
func (s *Server) V1() string { return s.URL + "/v1" }

// Requests are the chat completions seen so far, in order.
func (s *Server) Requests() []Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Request(nil), s.requests...)
}

// Hold makes replies wait until Release.
func (s *Server) Hold() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.hold == nil {
		s.hold = make(chan struct{})
	}
}

// Release lets the replies waiting, and those to come, through.
func (s *Server) Release() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.hold != nil {
		close(s.hold)
		s.hold = nil
	}
}

// Waiting is how many requests are held.
func (s *Server) Waiting() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.waiting
}

func (s *Server) authenticated(r *http.Request) bool {
	return s.Key == "" || r.Header.Get("Authorization") == "Bearer "+s.Key
}

func refuse(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"message": message, "type": "invalid_request_error", "code": code}})
}

func (s *Server) list(w http.ResponseWriter, r *http.Request) {
	if s.ListNeedsKey && !s.authenticated(r) {
		refuse(w, http.StatusUnauthorized, "invalid_api_key", "You must provide an API key to list models.")
		return
	}
	type entry struct {
		ID            string `json:"id"`
		ContextLength int    `json:"context_length,omitempty"`
	}
	var data []entry
	s.mu.Lock()
	for _, m := range s.models {
		data = append(data, entry{m.ID, m.Context})
	}
	s.mu.Unlock()
	_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
}

func (s *Server) complete(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Model        string            `json:"model"`
		Messages     []json.RawMessage `json:"messages"`
		Effort       *string           `json:"reasoning_effort"`
		CacheControl json.RawMessage   `json:"cache_control"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		refuse(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	req := Request{Model: body.Model, Authenticated: s.authenticated(r), Messages: len(body.Messages)}
	if body.Effort != nil {
		req.Effort, req.HasEffort = *body.Effort, true
	}
	if len(body.CacheControl) > 0 && string(body.CacheControl) != "null" {
		req.CacheControl = string(body.CacheControl)
	}
	var model *Model
	s.mu.Lock()
	for i := range s.models {
		if s.models[i].ID == body.Model {
			model = &s.models[i]
		}
	}
	hold := s.hold
	s.mu.Unlock()
	status, code, message := http.StatusOK, "", ""
	switch {
	case !req.Authenticated:
		status, code, message = http.StatusUnauthorized, "invalid_api_key", "Incorrect API key provided."
	case model == nil:
		status, code, message = http.StatusNotFound, "model_not_found", fmt.Sprintf("The model `%s` does not exist or you do not have access to it.", body.Model)
	case req.CacheControl != "" && !model.Caches:
		status, code, message = http.StatusBadRequest, "unsupported_parameter", fmt.Sprintf("Unrecognized request argument supplied: cache_control (%s does not support it)", body.Model)
	case req.HasEffort && len(model.Efforts) == 0:
		status, code, message = http.StatusBadRequest, "unsupported_parameter", fmt.Sprintf("Unrecognized request argument supplied: reasoning_effort (%s does not support it)", body.Model)
	case req.HasEffort && !slices.Contains(model.Efforts, req.Effort):
		status, code, message = http.StatusBadRequest, "invalid_value", fmt.Sprintf("Invalid value: '%s'. Supported values are: %s.", req.Effort, strings.Join(model.Efforts, ", "))
	}
	if status == http.StatusOK && hold != nil {
		s.mu.Lock()
		s.waiting++
		s.mu.Unlock()
		select {
		case <-hold:
		case <-r.Context().Done():
		}
		s.mu.Lock()
		s.waiting--
		s.mu.Unlock()
	}
	req.Answered = status
	s.mu.Lock()
	if status == http.StatusOK {
		s.account(&req, body.Messages)
	}
	s.requests = append(s.requests, req)
	s.mu.Unlock()
	if status != http.StatusOK {
		refuse(w, status, code, message)
		return
	}
	effort := req.Effort
	if !req.HasEffort {
		effort = "default"
	}
	_ = json.NewEncoder(w).Encode(map[string]any{
		"choices": []any{map[string]any{"finish_reason": "stop", "message": map[string]any{"role": "assistant", "content": fmt.Sprintf("Answered by %s with %s effort.", body.Model, effort)}}},
		"usage": map[string]any{
			"prompt_tokens": req.PromptTokens, "completion_tokens": 8,
			"prompt_tokens_details": map[string]any{"cached_tokens": req.CachedTokens, "cache_write_tokens": req.CacheWriteTokens},
		},
	})
}

// account counts the request's prompt and what the route would read from, and
// write to, its cache; s.mu is held.
func (s *Server) account(req *Request, messages []json.RawMessage) {
	tokens := make([]int, len(messages))
	for i, m := range messages {
		tokens[i] = len(m)/4 + 1
		req.PromptTokens += tokens[i]
	}
	if req.CacheControl == "" {
		// Nothing was asked for: nothing is read or written, whatever an
		// earlier request left in the cache.
		return
	}
	shared := 0
	for i := 0; i < len(messages) && i < len(s.cached) && string(messages[i]) == s.cached[i]; i++ {
		shared += tokens[i]
	}
	s.cached = make([]string, len(messages))
	for i, m := range messages {
		s.cached[i] = string(m)
	}
	if req.PromptTokens < minCache {
		return
	}
	req.CachedTokens = shared
	req.CacheWriteTokens = req.PromptTokens - shared
}
