package plain

import (
	"fmt"
	"github.com/fclaude/nibb/internal/guard"
	"github.com/fclaude/nibb/internal/log"
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
	return fmt.Sprintf("Credential class: %s\nDestination host: %s\nMethod: %s\nPosition: %s\nThese are the credential scope facts for this call.", known(scope.Class), known(scope.Host), known(scope.Method), known(scope.Position))
}

// BindingSummary distinguishes a requested change from a persisted outcome.
func BindingSummary(p log.BindingChange) string {
	return fmt.Sprintf("binding %s: %q → %q methods=%q positions=%q by %q %s", p.State, p.Binding.Class, p.Binding.Dest, p.Binding.Methods, p.Binding.Positions, p.Actor, p.Error)
}
