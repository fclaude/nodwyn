package log

import (
	"encoding/json"
	"github.com/fclaude/nibb/internal/binding"
	"github.com/fclaude/nibb/internal/budget"
)

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
	Actor    string `json:"actor,omitempty"`
	PromptID string `json:"prompt_id,omitempty"`
	Scope    string `json:"scope,omitempty"`
	Prim     string `json:"prim"`
	Origin   string `json:"origin"`
	Decision string `json:"decision"` // allow | deny | confirm
	Checker  string `json:"checker,omitempty"`
	Reason   string `json:"reason,omitempty"`
	Answer   string `json:"answer,omitempty"` // for confirm: yes | no | turn
	Args     string `json:"args,omitempty"`   // short written form of the arguments
	Detail   string `json:"detail,omitempty"` // the diff, when the user did not see it (standing approvals)
}

// EgressCheck records a binding lookup for a secret-bearing egress (E3).
type EgressCheck struct {
	Prim   string `json:"prim"`
	Class  string `json:"class"`
	Host   string `json:"host,omitempty"`
	Method string `json:"method,omitempty"`
	Bound  bool   `json:"bound"`
	Reason string `json:"reason,omitempty"` // set when not bound
}

// GatePlan is the P5 gate outcome for one user message: the noul verdict,
// whether a plan was required, the Seam A score, and the revision count.
type GatePlan struct {
	Origin    string  `json:"origin"`
	Noul      string  `json:"noul"` // "plan" | "proceed" | "degraded"
	Plan      string  `json:"plan,omitempty"`
	Score     float64 `json:"score,omitempty"`
	Revisions int     `json:"revisions,omitempty"`
	Reason    string  `json:"reason,omitempty"`
}

// JevReq is one Jev egress (ADR 19/20): what left, how much, and the score.
// The payload hash is a short digest; the raw payload is never logged, and a
// redacted payload is never logged either (only the events it triggered).
type JevReq struct {
	Tool        string   `json:"tool"` // jev-noul | jev-score | jev-choice
	Origin      string   `json:"origin"`
	Level       string   `json:"level"`                // none | plan | plan+summaries | full
	Bytes       int      `json:"bytes"`                // bytes of the redacted payload sent
	PayloadHash string   `json:"payload_hash"`         // hex sha256[:16] of the redacted payload
	Redactions  []string `json:"redactions,omitempty"` // class names removed by L1
	Answer      any      `json:"answer,omitempty"`     // the answer object Jev returned (no content of ours)
	OK          bool     `json:"ok"`
	Error       string   `json:"error,omitempty"`
}

// CredRegister records a credential registration. Class and Handle are
// metadata only; the secret value never appears in the log.
type CredRegister struct {
	Actor    string `json:"actor,omitempty"`
	PromptID string `json:"prompt_id,omitempty"`
	Class    string `json:"class"`
	Handle   string `json:"handle"` // the handle id
}

// Note is a free-form annotation.
type Note struct {
	Text string `json:"text"`
}

// TurnCheckpoint records a budget decision, including cancellation and the
// unchanged hard ceiling. Text is redacted by the ordinary Record boundary.
type TurnCheckpoint struct {
	Actor      string            `json:"actor,omitempty"`
	PromptID   string            `json:"prompt_id,omitempty"`
	Checkpoint budget.Checkpoint `json:"checkpoint"`
	Choice     budget.Choice     `json:"choice"`
	Error      string            `json:"error,omitempty"`
}

// ApprovalChange records the scope and issuer of an explicit temporary grant.
// It never contains a secret, and replay never reinstalls the grant.
type ApprovalChange struct {
	Actor    string `json:"actor,omitempty"`
	PromptID string `json:"prompt_id"`
	Scope    string `json:"scope"`
	Kind     string `json:"kind"` // command or write
	Prefix   string `json:"prefix"`
}

// EvalRequest/EvalResult are user-origin history, never model messages.
type EvalRequest struct {
	Source string `json:"source"`
	Actor  string `json:"actor,omitempty"`
}
type EvalResult struct {
	Value string `json:"value,omitempty"`
	Error string `json:"error,omitempty"`
}

// BindingChange records intent before persistence and its outcome afterwards.
// Replaying these audit records never reinstalls permission.
type BindingChange struct {
	Actor    string          `json:"actor"`
	PromptID string          `json:"prompt_id"`
	Binding  binding.Binding `json:"binding"`
	State    string          `json:"state"` // requested, applied, failed
	Error    string          `json:"error,omitempty"`
}
