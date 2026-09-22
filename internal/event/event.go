// Package event defines what a front-end sees (ADR 10): every log event as
// it is written, plus a few ephemeral events that are not worth logging per
// token (stream fragments) or that need an answer (prompts).
package event

import (
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
	StreamText     = "stream.text"     // Payload: Text
	StreamThinking = "stream.thinking" // Payload: Text
	PromptConfirm  = "prompt.confirm"  // Payload: Confirm; answered through Session.Answer
	PromptSecret   = "prompt.secret"   // Payload: SecretPrompt; answered through Session.Secret
	Status         = "status"          // Payload: StatusInfo
	Error          = "error"           // Payload: ErrorInfo
	Output         = "output"          // Payload: Text — Scheme display/write output
)

// Text is a fragment of streamed text or output.
type Text struct {
	Text string
}

// Confirm asks the user to approve a primitive application.
type Confirm struct {
	ID     string
	Prompt guard.Prompt
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
