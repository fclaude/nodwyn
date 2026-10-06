package guard

import (
	"testing"

	"github.com/fclaude/nodwyn/internal/binding"
)

func TestApprovalOptionsMatchSupportedScopes(t *testing.T) {
	row := &binding.Binding{Class: "service", Dest: "api.test"}
	for _, tt := range []struct {
		name   string
		prompt Prompt
		want   ApprovalOptions
	}{
		{"none", Prompt{}, ApprovalOptions{}},
		{"nested write", Prompt{Path: "notes/a.md"}, ApprovalOptions{Session: true, Target: "notes/", Kind: "write", Turn: true}},
		{"root write", Prompt{Path: "a.md"}, ApprovalOptions{Session: true, Target: "a.md", Kind: "write", Turn: true}},
		{"command", Prompt{Argv: []string{"go", "test", "./..."}}, ApprovalOptions{Session: true, Target: "go test", Kind: "command"}},
		{"absolute executable", Prompt{Argv: []string{"/usr/bin/go", "test", "./..."}}, ApprovalOptions{Kind: "command"}},
		{"protected", Prompt{Path: ".nodwyn/allow.toml"}, ApprovalOptions{}},
		{"security", Prompt{Path: "notes/a.md", Security: true, Binding: row}, ApprovalOptions{}},
		{"binding", Prompt{Binding: row}, ApprovalOptions{Binding: true}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.prompt.ApprovalOptions(); got != tt.want {
				t.Fatalf("%+v, want %+v", got, tt.want)
			}
		})
	}
}
