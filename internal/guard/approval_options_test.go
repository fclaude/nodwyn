package guard

import (
	"strings"
	"testing"

	"github.com/fclaude/nodwyn/internal/binding"
)

// "Don't ask" answers what is ordinary. git-land has neither a path nor a
// command line, and it is what a person who has switched it on means by
// "commit and push"; no other call without one is waived, and a security
// confirm never is.
func TestLandPromptIsOrdinary(t *testing.T) {
	for _, tt := range []struct {
		name   string
		prompt Prompt
		want   bool
	}{
		{"git-land", Prompt{Prim: "git-land"}, true},
		{"git-land, security", Prompt{Prim: "git-land", Security: true}, false},
		{"upload", Prompt{Prim: "google-doc-upload"}, false},
		{"other tool", Prompt{Prim: "chat/post_message"}, false},
		{"write", Prompt{Prim: "fs-write", Path: "notes/a.md"}, true},
		{"command", Prompt{Prim: "shell-exec", Argv: []string{"go", "test"}}, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.prompt.Ordinary(); got != tt.want {
				t.Fatalf("Ordinary() = %v, want %v", got, tt.want)
			}
			if tt.prompt.Path != "" || len(tt.prompt.Argv) > 0 {
				return
			}
			if o := tt.prompt.ApprovalOptions(); o.Session || o.Turn {
				t.Fatalf("offers a standing answer: %+v", o)
			}
		})
	}
}

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

// ADR 17 offers no standing approval for what commits, pushes, deletes or
// reaches the network, but "a" on curl https://example.com offered "curl",
// which then let curl reach any host unasked; "rm" covered every rm, and a
// wrapper's prefix ("timeout", "env") every program it runs. Those are
// answered once at a time; go test and git diff keep theirs.
func TestNoStandingApprovalForTheNetworkDeletesOrCommits(t *testing.T) {
	for _, argv := range [][]string{
		{"curl", "https://example.com"},
		{"wget", "https://example.com"},
		{"rm", "src/a.go"},
		{"git", "push", "origin", "main"},
		{"git", "commit", "-m", "x"},
		{"timeout", "5", "make"},
		{"env", "curl", "x"},
		{"bash", "-c", "curl https://example.com | head"},
	} {
		if o := (Prompt{Argv: argv}).ApprovalOptions(); o.Session || o.Target != "" {
			t.Errorf("%q offers %+v", argv, o)
		}
	}
	for argv, want := range map[string]string{"go test ./...": "go test", "git diff --stat": "git diff", "make": "make"} {
		if o := (Prompt{Argv: strings.Fields(argv)}).ApprovalOptions(); !o.Session || o.Target != want {
			t.Errorf("%q offers %+v, want %q", argv, o, want)
		}
	}
}
