package guard

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStandingApprovalChecksExecutableIdentity(t *testing.T) {
	repo := t.TempDir()
	for _, name := range []string{"git", "bash", "tail"} {
		if err := os.WriteFile(filepath.Join(repo, name), []byte("#!/bin/sh\nexit 0\n"), 0755); err != nil {
			t.Fatal(err)
		}
	}
	allow := NewCommandAllow("git status")
	for _, argv := range [][]string{{filepath.Join(repo, "git"), "status"}, {filepath.Join(repo, "bash"), "-c", "git status"}, {"bash", "-c", "git status | " + filepath.Join(repo, "tail")}} {
		if match := allow.Match(argv); match != "" {
			t.Errorf("%q inherited %q", argv, match)
		}
	}
	if match := allow.Match([]string{"git", "status"}); match != "git status" {
		t.Fatalf("the actual approved executable did not match: %q", match)
	}
	link := filepath.Join(repo, "approved")
	if err := os.Symlink(filepath.Join(repo, "git"), link); err != nil {
		t.Fatal(err)
	}
	allow = NewCommandAllowIn(repo, "./approved status")
	if got := allow.Match([]string{"./approved", "status"}); got != "./approved status" {
		t.Fatalf("explicit repository executable did not match: %s", got)
	}
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(repo, "bash"), link); err != nil {
		t.Fatal(err)
	}
	if got := allow.Match([]string{"./approved", "status"}); got != "" {
		t.Fatalf("changed symlink retained approval: %s", got)
	}
}
