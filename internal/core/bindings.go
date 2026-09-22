package core

import (
	"slices"

	"github.com/fclaude/nibb/internal/binding"
)

// Bindings returns the session's loaded authorization metadata, never secrets.
// Copies isolate inspectors from the table used by the guard. Policy changes
// still require editing configuration and reopening the session.
func (s *Session) Bindings() []binding.Binding {
	rows := make([]binding.Binding, len(s.bindings.Bind))
	for i, row := range s.bindings.Bind {
		rows[i] = row
		rows[i].Positions = slices.Clone(row.Positions)
		rows[i].Methods = slices.Clone(row.Methods)
	}
	return rows
}
