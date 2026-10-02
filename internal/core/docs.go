package core

import (
	"errors"

	"github.com/fclaude/nodwyn/internal/guard"
)

// CheckOutgoing implements prim.DocsClient: the L1 verdict on text about to
// leave for host by method, as the guard's L1 checker gives it for an egress
// payload (guard.TextSecret.Outgoing). A known secret shape is refused unless
// its class is bound for host. A document sent by google-doc-upload never
// was a Scheme value, so its handler asks this before it sends. With
// policy.l1.enabled = false the guard scans no payload, and this scans
// nothing either.
func (s *Session) CheckOutgoing(text, host, method string) error {
	if !s.opts.Config.Policy.L1.Enabled {
		return nil
	}
	if err := s.bindings.Refresh(); err != nil {
		return err
	}
	d := guard.TextSecret{Table: s.bindings, Scanner: s.Redactor()}.Outgoing(text, host, method)
	if d.Verdict != guard.Allow {
		return errors.New(d.Reason)
	}
	return nil
}
