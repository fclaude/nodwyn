package plain

import (
	"fmt"
	"strings"

	"github.com/fclaude/nodwyn/internal/guard"
	"github.com/fclaude/nodwyn/internal/log"
)

// EgressSummary names the guard's call facts without inferring missing scope.
// The TUI uses the same text and applies its terminal-safety filter afterwards.
func EgressSummary(scope *guard.EgressScope) string {
	if scope == nil {
		return ""
	}
	known := func(value string) string {
		if value == "" {
			return "not specified by primitive"
		}
		return fmt.Sprintf("%q", value)
	}
	scopes := ""
	if len(scope.Scopes) > 0 {
		// An OAuth2 credential: what it may do at the provider is part of
		// what is being sent (ADR 24 §4).
		scopes = fmt.Sprintf("\nOAuth2 scopes: %s", strings.Join(scope.Scopes, " "))
	}
	return fmt.Sprintf("Credential class: %s\nDestination host: %s\nMethod: %s\nPosition: %s%s\nThese are the credential scope facts for this call.", known(scope.Class), known(scope.Host), known(scope.Method), known(scope.Position), scopes)
}

// ApprovalSummary is an approval change as the terminal clients show it:
// one standing answer, or "don't ask" switched on or off.
func ApprovalSummary(p log.ApprovalChange) string {
	if p.Kind == "all" {
		if p.Off {
			return "[don't ask: off, writes and commands ask again]"
		}
		return "[don't ask: on, writes and commands approved without asking]"
	}
	return fmt.Sprintf("[approval requested: %s %s %s]", p.Scope, p.Kind, p.Prefix)
}

// BindingSummary distinguishes a requested change from a persisted outcome.
func BindingSummary(p log.BindingChange) string {
	scopes := ""
	if len(p.Binding.Scopes) > 0 {
		scopes = fmt.Sprintf(" scopes=%q", p.Binding.Scopes)
	}
	return fmt.Sprintf("binding %s: %q → %q methods=%q positions=%q%s by %q %s", p.State, p.Binding.Class, p.Binding.Dest, p.Binding.Methods, p.Binding.Positions, scopes, p.Actor, p.Error)
}
