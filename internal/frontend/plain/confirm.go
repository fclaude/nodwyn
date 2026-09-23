package plain

import (
	"fmt"
	"github.com/fclaude/nibb/internal/guard"
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
	return fmt.Sprintf("Credential class: %s\nDestination host: %s\nMethod: %s\nPosition: %s\nThese are call facts; bindings are unchanged.", known(scope.Class), known(scope.Host), known(scope.Method), known(scope.Position))
}
