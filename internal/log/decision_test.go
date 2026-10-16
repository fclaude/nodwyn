package log

import "testing"

// Wrote is the one place that says which guard decisions changed a file:
// allowed, or asked and answered yes, and only with a path.
func TestGuardDecisionWrote(t *testing.T) {
	for _, tc := range []struct {
		name string
		d    GuardDecision
		want bool
	}{
		{"allowed", GuardDecision{Decision: "allow", Path: "a.go"}, true},
		{"asked and yes", GuardDecision{Decision: "confirm", Answer: "yes", Path: "a.go"}, true},
		{"asked and no", GuardDecision{Decision: "confirm", Answer: "no", Path: "a.go"}, false},
		{"asked and failed", GuardDecision{Decision: "confirm", Answer: "error", Path: "a.go"}, false},
		{"denied", GuardDecision{Decision: "deny", Path: "a.go"}, false},
		{"allowed with no path", GuardDecision{Decision: "allow"}, false},
	} {
		if got := tc.d.Wrote(); got != tc.want {
			t.Errorf("%s: Wrote() = %v", tc.name, got)
		}
	}
}
