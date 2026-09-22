package guard

import "context"

// Approval is the reply and its provenance, not an independent permission.
// The guard still evaluates all policy before asking for this receipt.
type Approval struct {
	Allowed  bool
	Actor    string
	PromptID string
	Scope    string // once, turn, session
}

// AttributedConfirmer is an optional extension of the boolean Confirmer. The
// plain boolean interface remains available to embedded hosts and existing
// checkers; transports that know the answering actor can supply a receipt.
type AttributedConfirmer interface {
	ConfirmApproval(context.Context, Prompt) (Approval, error)
}

func (g *Guard) confirm(ctx context.Context, p Prompt) (Approval, error) {
	if c, ok := g.confirmer.(AttributedConfirmer); ok {
		return c.ConfirmApproval(ctx, p)
	}
	allowed, err := g.confirmer.Confirm(ctx, p)
	return Approval{Allowed: allowed}, err
}
