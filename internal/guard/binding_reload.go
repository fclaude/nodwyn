package guard

import (
	"context"
	"github.com/fclaude/nibb/internal/binding"
	"github.com/fclaude/nibb/internal/scheme"
)

// BindingReload refreshes the session snapshot before any egress checker runs.
// Invalid policy is a denial even when unbound egress normally asks the user.
type BindingReload struct{ Policy *binding.Live }

func (BindingReload) Name() string { return "bindings" }
func (r BindingReload) Check(_ context.Context, p *scheme.Prim, _ []scheme.Value) Decision {
	if p.Meta.Egress {
		if err := r.Policy.Refresh(); err != nil {
			return Decision{Verdict: Deny, Reason: "cannot reload binding policy: " + err.Error()}
		}
	}
	return Decision{Verdict: Allow}
}
