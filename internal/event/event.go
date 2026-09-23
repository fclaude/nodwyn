// Package event defines what a front-end sees (ADR 10): every log event as
// it is written, plus a few ephemeral events that are not worth logging per
// token (stream fragments) or that need an answer (prompts).
package event

import (
	"github.com/fclaude/nibb/internal/budget"
	"github.com/fclaude/nibb/internal/guard"
	"github.com/fclaude/nibb/internal/log"
)

// Event is one item on a session's event stream. For log-backed events
// Kind is the log kind and Seq its sequence number; ephemeral events have
// Seq 0.
type Event struct {
	Kind    string
	Seq     uint64
	Payload any
}

// Ephemeral kinds.
const (
	PolicyResult     = "policy.result"     // Payload: Policy; completed guarded policy operation
	InspectionResult = "inspection.result" // Payload: Inspection; ephemeral result of a guarded frontend read
	StreamText       = "stream.text"       // Payload: Text
	StreamThinking   = "stream.thinking"   // Payload: Text
	PromptCheckpoint = "prompt.checkpoint" // Payload: Checkpoint; answered through Session.Answer
	PromptConfirm    = "prompt.confirm"    // Payload: Confirm; answered through Session.Answer
	PromptSecret     = "prompt.secret"     // Payload: SecretPrompt; answered through Session.Secret
	Status           = "status"            // Payload: StatusInfo
	Error            = "error"             // Payload: ErrorInfo
	Output           = "output"            // Payload: Text — Scheme display/write output
)

// Text is a fragment of streamed text or output.
type Text struct {
	Text string
}

// Confirm asks the user to approve a primitive application.
type Confirm struct {
	ID      string
	Prompt  guard.Prompt
	Options *guard.ApprovalOptions `json:",omitempty"`
}

// NewConfirm gives all frontends the same derived scope labels. Older daemon
// versions omit Options; browser clients then offer only one-time answers.
func NewConfirm(id string, prompt guard.Prompt) Confirm {
	options := prompt.ApprovalOptions()
	return Confirm{ID: id, Prompt: prompt, Options: &options}
}

// SecretPrompt asks the user for a masked secret (credential-register).
type SecretPrompt struct {
	ID    string
	Class string
}

// StatusInfo says what the session is doing.
type StatusInfo struct {
	State  string // "idle" | "busy"
	Detail string // e.g. "waiting for model", "running fs-read"
}

// ErrorInfo reports a turn-level failure.
type ErrorInfo struct {
	Err string
}

// FromLog wraps a written log event with its decoded payload.
func FromLog(ev log.Event, payload any) Event {
	return Event{Kind: string(ev.Kind), Seq: ev.Seq, Payload: payload}
}

// Checkpoint offers additional soft budget, never additional tool authority.
type Checkpoint struct {
	ID         string
	Checkpoint budget.Checkpoint
}

// Inspection carries a completed guarded read. ID ties it to the initiating
// request; this is a display result and never confers approval authority.
type Inspection struct {
	ID   string
	Name string
	Text string
	Err  string
}

// Policy is a completed fixed-policy read or save, not an authorization grant.
type Policy struct {
	ID       string
	Name     string
	Content  string
	Revision string
	Exists   bool
	Saved    bool
	Err      string
}
