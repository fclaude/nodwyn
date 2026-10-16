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
	Role      string     `json:"role"`
	Content   string     `json:"content"`
	Thinking  string     `json:"thinking,omitempty"` // on a response, and sent back on the reply it came with (window recipe v14, ADR 9)
	ToolCalls []ToolCall `json:"tool_calls,omitempty"`
	// Blocks are a reply's reasoning as its provider signed or encrypted it,
	// given back with the reply to a provider that reads it again (Anthropic,
	// and a Responses model that keeps its reasoning; window recipe v18, ADR 9
	// amended 2026-10-05 and 2026-10-07).
	Blocks     []ThinkingBlock `json:"blocks,omitempty"`
	ToolName   string          `json:"tool_name,omitempty"`    // role=tool
	ToolCallID string          `json:"tool_call_id,omitempty"` // role=tool
	// Images go with a user message to a model that takes them: an
	// attached image (ADR 22 §3). Omitted when empty, so requests without
	// one hash as before.
	Images []Image `json:"images,omitempty"`
}

// ThinkingBlock is one block of a reply's reasoning as its provider gave
// it, to go back unchanged with that reply: Anthropic signs each, and reads
// the reasoning behind a signature again only from a block that comes back
// as it was; a Responses server encrypts a reasoning item's; a
// chat-completions server such as OpenRouter gives a reply's reasoning as
// reasoning_details, which go back whole. Before is how many of the reply's
// tool calls came before it, so it goes back where it was. Each provider
// gives back only the types it makes.
type ThinkingBlock struct {
	Type      string `json:"type"`                // "thinking" or "redacted_thinking" (Anthropic), "reasoning" (Responses), ReasoningDetails (chat completions)
	Thinking  string `json:"thinking,omitempty"`  // the summary given, empty when none was
	Signature string `json:"signature,omitempty"` // a thinking block's
	Data      string `json:"data,omitempty"`      // a redacted_thinking block's, a reasoning item's encrypted content, or the reasoning_details array as the JSON text it came as
	Before    int    `json:"before,omitempty"`
	// Origin is the server and model a reasoning item or reasoning_details
	// came from, the only ones they go back to (OpenAI.ReasoningOrigin).
	Origin string `json:"origin,omitempty"`
}

// ReasoningDetails is the type of the block that keeps a chat-completions
// reply's reasoning_details: its Data is the whole array, the JSON text the
// server sent, never read field by field, so it goes back as it came, in
// order (ADR 9, amended 2026-10-15).
const ReasoningDetails = "reasoning_details"

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
	// Effort is how hard the model is asked to think (effort.go); empty
	// leaves it to the provider, and is omitted so requests without one hash
	// as before.
	Effort string `json:",omitempty"`
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
	// WallMS is how long the request took as this machine saw it, from send
	// to the reply's complete arrival, the successful attempt only (a
	// retry's earlier failures are not the model's time). Host-measured, so
	// every provider has it; the fields above are what the provider
	// reported. tok/s is OutputTokens per WallMS/1000.
	WallMS int64
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
	// Unreported: the server said nothing about tokens, so PromptTokens and
	// OutputTokens are 0 for want of a count, not a count of 0.
	Unreported bool
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

// ContextReporter is a provider whose server's model list says how many
// tokens a model's context holds (OpenAI-compatible servers that give it, and
// Anthropic). Server is the address it asks, which names what it said.
type ContextReporter interface {
	Server() string
	ContextLength(ctx context.Context, model string) (int, error)
}
