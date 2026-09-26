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

// ApprovalOptions describes the wider scopes available for one confirmation.
// It is display metadata, not a grant; core checks the still-pending prompt again.
type ApprovalOptions struct {
	Session bool   `json:"session"`
	Target  string `json:"target"`
	Kind    string `json:"kind"`
	Turn    bool   `json:"turn"`
	Binding bool   `json:"binding"`
}

// ApprovalOptions says which answers beyond "once" this prompt may offer:
// saving the proposed binding, unless it is a security prompt; and, unless
// it is a security prompt or touches a protected path, approving a command
// prefix for the session, or a write's directory for the session or the
// rest of the turn.
func (p Prompt) ApprovalOptions() ApprovalOptions {
	options := ApprovalOptions{Binding: !p.Security && p.Binding != nil}
	if p.Security || IsProtected(p.Path) {
		return options
	}
	options.Session = p.Path != "" || len(p.Argv) > 0
	options.Turn = p.Path != ""
	if len(p.Argv) > 0 {
		options.Kind, options.Target = "command", Prefix(p.Argv)
	} else if p.Path != "" {
		options.Kind, options.Target = "write", DirPrefix(p.Path)
	}
	return options
}
