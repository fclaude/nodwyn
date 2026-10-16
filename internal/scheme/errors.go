package scheme

import (
	"fmt"
	"strings"
)

// Error is a Scheme-level error: what (error ...) raises and what runtime
// failures produce. guard catches it (a denial and a cancellation excepted,
// which are Denied and the context's error); otherwise it unwinds to the host.
type Error struct {
	Message   string
	Irritants []Value
	Cause     error // wrapped host error, if any (e.g. context.Canceled, *Denied)
	// Taint is the provenance of what Message may quote: the arguments of
	// the primitive that failed. guard gives it to the message it hands on.
	Taint Taint
}

func (e *Error) Error() string {
	if len(e.Irritants) == 0 {
		return e.Message
	}
	parts := make([]string, len(e.Irritants))
	for i, v := range e.Irritants {
		parts[i] = Write(v)
	}
	return e.Message + " " + strings.Join(parts, " ")
}

func (e *Error) Unwrap() error { return e.Cause }

// Denied is the guard's refusal, surfaced to Scheme as "denied: reason".
type Denied struct {
	Prim   string
	Reason string
}

func (d *Denied) Error() string { return "denied: " + d.Reason }

// errorf builds a runtime error "who: message".
func errorf(who, format string, args ...any) *Error {
	return &Error{Message: who + ": " + fmt.Sprintf(format, args...)}
}

// wrap turns a Go error into a Scheme error, preserving it as Cause. A bare
// *Error passes through unchanged so nested applications do not stack
// prefixes; a wrapped one (e.g. "file.scm: <error>") is re-prefixed.
func wrap(who string, err error) *Error {
	if se, ok := err.(*Error); ok {
		return se
	}
	return &Error{Message: who + ": " + err.Error(), Cause: err}
}
