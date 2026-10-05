// Package chat is a fake chat-completions provider for tests and the browser
// demo (ADR 41): the shape OpenAI, OpenRouter, Cerebras and vLLM share, and
// OpenAI's Responses API beside it. It lists its models, checks the bearer
// key, and refuses what a real provider refuses: a wrong key (401), a model it
// does not have (404), a reasoning_effort for a model that does not take one,
// or a word it does not know (400), an effort beside tools on
// /chat/completions for a model that takes them together only on /responses
// (400, as OpenAI refuses gpt-6.1-sol), and a parameter /responses does not
// know (400), so a test shows that a request carried exactly the effort
// chosen, and none where none was meant, in the protocol it was meant for.
// Anthropic's Messages API is beside them at /v1/messages: it takes its key in
// x-api-key (and refuses a bearer one), wants an anthropic-version, needs
// max_tokens, takes an effort only as output_config.effort beside adaptive
// thinking and only for a model that lists efforts, and answers in content
// blocks with usage in its own fields, input_tokens being only the prompt after
// the last cache breakpoint. Every request is recorded; replies echo the model
// and effort that asked, so a conversation shows which one answered. Hold keeps replies waiting until
// Release, to look at a response that is still running.
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
	// ToolsNeedResponses refuses an effort beside tools on
	// /chat/completions, with OpenAI's words, and takes them on /responses.
	ToolsNeedResponses bool
}

// Request is one chat completion the fake saw.
type Request struct {
	// Path is where it was sent: /v1/chat/completions, /v1/responses or
	// /v1/messages.
	Path  string `json:"path"`
	Model string `json:"model"`
	// Effort is the reasoning_effort sent; HasEffort tells "none" from "".
	Effort    string `json:"effort"`
	HasEffort bool   `json:"has_effort"`
	// Authenticated says the right key came with it; the key is not kept.
	// KeyHeader says where it came: "bearer" (Authorization), "x-api-key", or
	// "" for no key at all.
	Authenticated bool   `json:"authenticated"`
	KeyHeader     string `json:"key_header"`
	// Thinking is the thinking parameter of a Messages request, as JSON; empty
	// when it sent none.
	Thinking string `json:"thinking"`
	Messages int    `json:"messages"`
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
	mux.HandleFunc("/v1/responses", s.respond)
	mux.HandleFunc("/v1/messages", s.message)
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

// authenticated says whether r carries the key where the protocol it is sent
// in reads it: a bearer token for chat completions and Responses, x-api-key
// for the Messages API. The model list is read by either kind of client.
func (s *Server) authenticated(r *http.Request) bool {
	if s.Key == "" {
		return true
	}
	bearer := r.Header.Get("Authorization") == "Bearer "+s.Key
	header := r.Header.Get("x-api-key") == s.Key
	switch r.URL.Path {
	case "/v1/messages":
		return header
	case "/v1/models":
		return bearer || header
	}
	return bearer
}

// keyHeader is where r carried the key, for the record.
func keyHeader(r *http.Request) string {
	switch {
	case r.Header.Get("x-api-key") != "":
		return "x-api-key"
	case r.Header.Get("Authorization") != "":
		return "bearer"
	}
	return ""
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
	// One list for both protocols: context_length is the OpenAI-compatible
	// servers', max_input_tokens Anthropic's.
	type entry struct {
		ID             string `json:"id"`
		Type           string `json:"type"`
		ContextLength  int    `json:"context_length,omitempty"`
		MaxInputTokens int    `json:"max_input_tokens,omitempty"`
	}
	var data []entry
	s.mu.Lock()
	for _, m := range s.models {
		data = append(data, entry{m.ID, "model", m.Context, m.Context})
	}
	s.mu.Unlock()
	_ = json.NewEncoder(w).Encode(map[string]any{"data": data, "has_more": false})
}

// asked is what a request asked, in either protocol.
type asked struct {
	model        string
	messages     []json.RawMessage // chat messages, Responses input items, or Messages messages
	effort       *string
	tools        int
	cacheControl json.RawMessage
	thinking     json.RawMessage // Messages only
}

func (s *Server) complete(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Model        string            `json:"model"`
		Messages     []json.RawMessage `json:"messages"`
		Effort       *string           `json:"reasoning_effort"`
		Tools        []json.RawMessage `json:"tools"`
		CacheControl json.RawMessage   `json:"cache_control"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		refuse(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	s.answer(w, r, asked{model: body.Model, messages: body.Messages, effort: body.Effort, tools: len(body.Tools), cacheControl: body.CacheControl})
}

// responsesParams are the parameters of a Responses request the fake knows:
// those nodwyn may send, and cache_control for a route that caches (Caches).
var responsesParams = []string{"model", "input", "tools", "stream", "store", "max_output_tokens", "temperature", "prompt_cache_key", "reasoning", "cache_control"}

func (s *Server) respond(w http.ResponseWriter, r *http.Request) {
	var params map[string]json.RawMessage
	if err := json.NewDecoder(r.Body).Decode(&params); err != nil {
		refuse(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	for k := range params {
		if !slices.Contains(responsesParams, k) {
			refuse(w, http.StatusBadRequest, "unknown_parameter", fmt.Sprintf("Unknown parameter: '%s'.", k))
			return
		}
	}
	var body struct {
		Model     string            `json:"model"`
		Input     []json.RawMessage `json:"input"`
		Reasoning *struct {
			Effort *string `json:"effort"`
		} `json:"reasoning"`
		Tools        []json.RawMessage `json:"tools"`
		CacheControl json.RawMessage   `json:"cache_control"`
	}
	raw, _ := json.Marshal(params)
	if err := json.Unmarshal(raw, &body); err != nil {
		refuse(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	a := asked{model: body.Model, messages: body.Input, tools: len(body.Tools), cacheControl: body.CacheControl}
	if body.Reasoning != nil {
		a.effort = body.Reasoning.Effort
	}
	s.answer(w, r, a)
}

// messagesParams are the parameters of a Messages request the fake knows:
// those nodwyn may send. Anything else is refused as the API refuses an extra
// input.
var messagesParams = []string{"model", "max_tokens", "system", "messages", "tools", "temperature", "thinking", "output_config", "cache_control"}

func (s *Server) message(w http.ResponseWriter, r *http.Request) {
	var params map[string]json.RawMessage
	if err := json.NewDecoder(r.Body).Decode(&params); err != nil {
		refuseMessages(w, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}
	for k := range params {
		if !slices.Contains(messagesParams, k) {
			refuseMessages(w, http.StatusBadRequest, "invalid_request_error", k+": Extra inputs are not permitted")
			return
		}
	}
	if _, ok := params["max_tokens"]; !ok {
		refuseMessages(w, http.StatusBadRequest, "invalid_request_error", "max_tokens: Field required")
		return
	}
	var body struct {
		Model        string            `json:"model"`
		Messages     []json.RawMessage `json:"messages"`
		Tools        []json.RawMessage `json:"tools"`
		CacheControl json.RawMessage   `json:"cache_control"`
		Thinking     json.RawMessage   `json:"thinking"`
		OutputConfig *struct {
			Effort *string `json:"effort"`
		} `json:"output_config"`
	}
	raw, _ := json.Marshal(params)
	if err := json.Unmarshal(raw, &body); err != nil {
		refuseMessages(w, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}
	a := asked{model: body.Model, messages: body.Messages, tools: len(body.Tools), cacheControl: body.CacheControl, thinking: body.Thinking}
	if body.OutputConfig != nil {
		a.effort = body.OutputConfig.Effort
	}
	s.answer(w, r, a)
}

// refuseMessages answers in the shape the Messages API refuses in.
func refuseMessages(w http.ResponseWriter, status int, kind, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"type": "error", "error": map[string]any{"type": kind, "message": message}})
}

func (s *Server) answer(w http.ResponseWriter, r *http.Request, a asked) {
	messages := r.URL.Path == "/v1/messages"
	req := Request{Path: r.URL.Path, Model: a.model, Authenticated: s.authenticated(r), KeyHeader: keyHeader(r), Messages: len(a.messages)}
	if len(a.thinking) > 0 && string(a.thinking) != "null" {
		req.Thinking = string(a.thinking)
	}
	if a.effort != nil {
		req.Effort, req.HasEffort = *a.effort, true
	}
	if len(a.cacheControl) > 0 && string(a.cacheControl) != "null" {
		req.CacheControl = string(a.cacheControl)
	}
	var model *Model
	s.mu.Lock()
	for i := range s.models {
		if s.models[i].ID == a.model {
			model = &s.models[i]
		}
	}
	hold := s.hold
	s.mu.Unlock()
	status, code, message := http.StatusOK, "", ""
	switch {
	case !req.Authenticated && messages:
		status, code, message = http.StatusUnauthorized, "authentication_error", "invalid x-api-key"
	case !req.Authenticated:
		status, code, message = http.StatusUnauthorized, "invalid_api_key", "Incorrect API key provided."
	case messages && r.Header.Get("anthropic-version") == "":
		status, code, message = http.StatusBadRequest, "invalid_request_error", "anthropic-version: header is required"
	case model == nil && messages:
		status, code, message = http.StatusNotFound, "not_found_error", "model: "+a.model
	case model == nil:
		status, code, message = http.StatusNotFound, "model_not_found", fmt.Sprintf("The model `%s` does not exist or you do not have access to it.", a.model)
	case messages && req.Thinking != "" && len(model.Efforts) == 0:
		status, code, message = http.StatusBadRequest, "invalid_request_error", `"thinking.type.adaptive" is not supported for this model`
	case messages && req.HasEffort && len(model.Efforts) == 0:
		status, code, message = http.StatusBadRequest, "invalid_request_error", "output_config.effort: this model does not support effort"
	case messages && req.HasEffort && !slices.Contains(model.Efforts, req.Effort):
		status, code, message = http.StatusBadRequest, "invalid_request_error", fmt.Sprintf("output_config.effort: Input should be %s", strings.Join(model.Efforts, ", "))
	case req.CacheControl != "" && !model.Caches && !messages:
		status, code, message = http.StatusBadRequest, "unsupported_parameter", fmt.Sprintf("Unrecognized request argument supplied: cache_control (%s does not support it)", a.model)
	case req.HasEffort && len(model.Efforts) == 0:
		status, code, message = http.StatusBadRequest, "unsupported_parameter", fmt.Sprintf("Unrecognized request argument supplied: reasoning_effort (%s does not support it)", a.model)
	case req.HasEffort && !slices.Contains(model.Efforts, req.Effort):
		status, code, message = http.StatusBadRequest, "invalid_value", fmt.Sprintf("Invalid value: '%s'. Supported values are: %s.", req.Effort, strings.Join(model.Efforts, ", "))
	case req.Path == "/v1/chat/completions" && model.ToolsNeedResponses && req.HasEffort && a.tools > 0:
		status, code, message = http.StatusBadRequest, "", fmt.Sprintf("Function tools with reasoning_effort are not supported for %s in /v1/chat/completions. To use function tools, use /v1/responses or set reasoning_effort to 'none'.", a.model)
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
		s.account(&req, a.messages)
	}
	s.requests = append(s.requests, req)
	s.mu.Unlock()
	if status != http.StatusOK && messages {
		refuseMessages(w, status, code, message)
		return
	}
	if status != http.StatusOK {
		refuse(w, status, code, message)
		return
	}
	effort := req.Effort
	if !req.HasEffort {
		effort = "default"
	}
	text := fmt.Sprintf("Answered by %s with %s effort.", a.model, effort)
	if messages {
		content := []any{}
		if req.Thinking != "" {
			content = append(content, map[string]any{"type": "thinking", "thinking": fmt.Sprintf("Thinking at %s effort.", effort), "signature": "fake"})
		}
		content = append(content, map[string]any{"type": "text", "text": text})
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "msg_fake", "type": "message", "role": "assistant", "model": a.model, "content": content, "stop_reason": "end_turn",
			// input_tokens is only the prompt after the last cache breakpoint.
			"usage": map[string]any{
				"input_tokens": req.PromptTokens - req.CachedTokens - req.CacheWriteTokens, "output_tokens": 8,
				"cache_read_input_tokens": req.CachedTokens, "cache_creation_input_tokens": req.CacheWriteTokens,
			},
		})
		return
	}
	if req.Path == "/v1/responses" {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status": "completed",
			"output": []any{map[string]any{"type": "message", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": text}}}},
			"usage": map[string]any{
				"input_tokens": req.PromptTokens, "output_tokens": 8,
				"input_tokens_details": map[string]any{"cached_tokens": req.CachedTokens},
			},
		})
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{
		"choices": []any{map[string]any{"finish_reason": "stop", "message": map[string]any{"role": "assistant", "content": text}}},
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
