package prim

import (
	"os"
	"slices"
	"strings"
	"testing"
)

// The person's own change that removes a leaked key, or edits beside a test
// fixture that holds one, is not a leak: only the lines a change adds are
// scanned. The refusal stays for a key it adds.
func TestGitLandScansOnlyWhatTheChangeAdds(t *testing.T) {
	r := newLandRig(t)
	const key = "AKIAIOSFODNN7EXAMPLE"
	r.write(r.repo, "cfg.go", "package cfg\n\nvar AwsKey = \""+key+"\"\n")
	r.write(r.repo, "fixture.txt", "one\n"+key+"\nthree\nfour\n")
	r.git(r.repo, "add", "-A")
	r.git(r.repo, "commit", "-q", "-m", "A leaked key and a fixture")
	r.git(r.repo, "push", "-q", "origin", "main")
	r.git(r.tree, "merge", "-q", "--ff-only", "main")

	// The task: take the key out of cfg.go and edit the line beside the
	// fixture's key, which git shows as context.
	r.write(r.tree, "cfg.go", "package cfg\n\nvar AwsKey = \"\"\n")
	r.write(r.tree, "fixture.txt", "one\n"+key+"\nTHREE\nfour\n")
	out, err := r.land("Take the key out of cfg.go")
	if err != nil {
		t.Fatalf("a change that removes a key and edits beside a fixture: %v", err)
	}
	if !strings.Contains(out, "pushed") {
		t.Fatalf("landed: %q", out)
	}

	r.write(r.tree, "cfg.go", "package cfg\n\nvar AwsKey = \""+key+"\"\n")
	if _, err := r.land("Put the key back"); err == nil || !strings.Contains(err.Error(), "nothing was pushed") {
		t.Fatalf("a change that adds a key: %v", err)
	}
}

func TestAddedTextIsTheAddedLinesOfEachHunk(t *testing.T) {
	diff := "diff --git a/a b/a\n--- a/a\n+++ b/a\n@@ -1,2 +1,2 @@\n-old\n+new\n+++ not a header\n@@ -9 +9 @@\n+next\ndiff --git a/b b/b\n--- a/b\n+++ b/b\n@@ -1 +1 @@\n-gone\n"
	if got, want := addedText(diff), "new\n++ not a header\nnext"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// git-land's ssh runs the person's own command when they have one, and
// refuses to prompt otherwise. The decision used to read the parent's
// environment while the child's was built from an allowlist without it, so
// a person's GIT_SSH_COMMAND reached git as nothing at all.
func TestLandEnvKeepsThePersonsSSHCommand(t *testing.T) {
	ssh := func() []string {
		var out []string
		for _, kv := range landEnv(DefaultOptions(t.TempDir())) {
			if strings.HasPrefix(kv, "GIT_SSH_COMMAND=") {
				out = append(out, kv)
			}
		}
		return out
	}
	t.Setenv("GIT_SSH_COMMAND", "ssh -i /home/p/.ssh/work")
	if got := ssh(); !slices.Equal(got, []string{"GIT_SSH_COMMAND=ssh -i /home/p/.ssh/work"}) {
		t.Errorf("with a command set: %q", got)
	}
	os.Unsetenv("GIT_SSH_COMMAND") // restored by the Setenv above
	if got := ssh(); !slices.Equal(got, []string{"GIT_SSH_COMMAND=ssh -o BatchMode=yes"}) {
		t.Errorf("with none set: %q", got)
	}
}
