package log

import "encoding/json"

// Typed payloads for the event kinds. They are the schema of the log; the
// window, the replay command and every front-end decode these.

// SessionStart is the first event of every session.
type SessionStart struct {
	Version  string `json:"version"`
	Repo     string `json:"repo"`
	Provider string `json:"provider"`
	Model    string `json:"model"`
	URL      string `json:"url"`
	Tools    int    `json:"tools"`
}

// SessionEnd is written on clean shutdown.
type SessionEnd struct {
	Reason string `json:"reason"`
}

// UserMsg is text the user sent.
type UserMsg struct {
	Text string `json:"text"`
}

// LLMReq describes a request to the control-plane model. The messages are
// not repeated: they are rebuilt from the log by the window.
type LLMReq struct {
	Model     string   `json:"model"`
	Messages  int      `json:"messages"`
	Tools     int      `json:"tools"`
	EstTokens int      `json:"est_tokens"`
	Elided    []uint64 `json:"elided,omitempty"`
}

// ToolCall is one tool application the model asked for.
type ToolCall struct {
	ID   string          `json:"id"`
	Name string          `json:"name"`
	Args json.RawMessage `json:"args"`
}

// Usage is what the provider reported.
type Usage struct {
	PromptTokens int   `json:"prompt_tokens"`
	OutputTokens int   `json:"output_tokens"`
	PromptEvalMS int64 `json:"prompt_eval_ms"`
	EvalMS       int64 `json:"eval_ms"`
	TotalMS      int64 `json:"total_ms"`
	LoadMS       int64 `json:"load_ms"`
}

// LLMResp is the model's full output for one request.
type LLMResp struct {
	Content    string     `json:"content"`
	Thinking   string     `json:"thinking,omitempty"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	DoneReason string     `json:"done_reason"`
	Usage      Usage      `json:"usage"`
}

// ToolCallEvent is written before a tool application is evaluated.
type ToolCallEvent struct {
	ToolCall
	Origin string `json:"origin"`
}

// ToolResult is written after it. Exactly one of Value or Error is set;
// Value is what the model is shown.
type ToolResult struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Value  string `json:"value,omitempty"`
	Error  string `json:"error,omitempty"`
	Millis int64  `json:"ms"`
}

// GuardDecision records the guard's answer at apply->Prim.
type GuardDecision struct {
	Prim     string `json:"prim"`
	Origin   string `json:"origin"`
	Decision string `json:"decision"` // allow | deny | confirm
	Checker  string `json:"checker,omitempty"`
	Reason   string `json:"reason,omitempty"`
	Answer   string `json:"answer,omitempty"` // for confirm: yes | no | turn
	Args     string `json:"args,omitempty"`   // short written form of the arguments
	Detail   string `json:"detail,omitempty"` // the diff, when the user did not see it (standing approvals)
}

// Note is a free-form annotation.
type Note struct {
	Text string `json:"text"`
}
