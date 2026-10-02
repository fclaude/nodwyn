// Package llm is the control-plane model client behind a Provider
// interface (ADR 8). The bootstrap implementation is Ollama's native
// /api/chat; nothing above this package knows which provider is attached.
package llm

import (
	"context"
	"encoding/json"
)

// Role of a chat message.
const (
	RoleSystem    = "system"
	RoleUser      = "user"
	RoleAssistant = "assistant"
	RoleTool      = "tool"
)

// Message is one entry of the conversation sent to the model.
type Message struct {
	Role       string     `json:"role"`
	Content    string     `json:"content"`
	Thinking   string     `json:"thinking,omitempty"` // never sent back (ADR 9); present on responses
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolName   string     `json:"tool_name,omitempty"`    // role=tool
	ToolCallID string     `json:"tool_call_id,omitempty"` // role=tool
	// Images go with a user message to a model that takes them: an
	// attached image (ADR 22 §3). Omitted when empty, so requests without
	// one hash as before.
	Images []Image `json:"images,omitempty"`
}

// Image is a picture sent with a message. Only its media type and digest
// are in the request's JSON, so a request's hash names the image by its
// SHA-256 and nodwyn replay --window verifies the request without the
// pixels; the bytes go only into a provider's wire format.
type Image struct {
	MediaType string `json:"media_type"` // image/png, image/jpeg, image/gif or image/webp
	SHA256    string `json:"sha256"`     // hex digest of Data
	Data      []byte `json:"-"`
}

// ToolCall is a model request to apply a tool. Args is the JSON object of
// named arguments.
type ToolCall struct {
	ID   string          `json:"id,omitempty"`
	Name string          `json:"name"`
	Args json.RawMessage `json:"args"`
}

// Tool is a tool definition offered to the model.
type Tool struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Parameters  any    `json:"parameters"` // JSON schema
}

// Request is one chat completion.
type Request struct {
	Model     string
	Messages  []Message
	Tools     []Tool
	NumCtx    int
	KeepAlive string
	// MaxTokens caps the tokens the model generates, thinking included, so
	// prompt and output fit the context (window.output_reserve); 0 leaves it
	// to the server. Omitted when 0, so older requests hash as before.
	MaxTokens int `json:",omitempty"`
	// Temperature and Seed are sampling settings; nil leaves them to the
	// server, and is omitted so requests without them hash as before.
	Temperature *float64 `json:",omitempty"`
	Seed        *int64   `json:",omitempty"`
}

// Delta is a streamed fragment.
type Delta struct {
	Text     string
	Thinking string
}

// Usage is what the provider reported about the request.
type Usage struct {
	PromptTokens int
	OutputTokens int
	PromptEvalMS int64
	EvalMS       int64
	TotalMS      int64
	LoadMS       int64
	// CachedTokens is how many of PromptTokens the provider read from its
	// prompt cache, when it says; nil when it does not (Ollama), which is
	// not the same as a reported 0. CacheWriteTokens are prompt tokens it
	// wrote to the cache (billed above the input rate by some providers).
	// ReasoningTokens are the part of OutputTokens spent thinking: shown,
	// never added again.
	CachedTokens     *int
	CacheWriteTokens int
	ReasoningTokens  int
	// Cost is what the provider says the request cost, in USD (OpenRouter
	// does); nil when it does not say.
	Cost *float64
}

// Response is the complete model output for one request.
type Response struct {
	Message    Message
	DoneReason string // "stop", "length", "tool_calls"...
	Usage      Usage
}

// Provider is the seam every model sits behind.
type Provider interface {
	// Name identifies the provider for the log.
	Name() string
	// Chat streams a completion, calling onDelta for each fragment, and
	// returns the assembled response. It must honour ctx.
	Chat(ctx context.Context, req Request, onDelta func(Delta)) (*Response, error)
	// Ping checks reachability and returns a version string.
	Ping(ctx context.Context) (string, error)
}

// VisionReporter is a provider that can say whether a model takes images.
// A provider that is not one is taken not to.
type VisionReporter interface {
	Vision(ctx context.Context, model string) (bool, error)
}
