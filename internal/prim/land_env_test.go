package prim

import (
	"os"
	"path/filepath"
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

// Repository-scoped deploy keys live in core.sshCommand, shared by the
// checkout and its worktrees. Landing must use that transport for both fetch
// and push; a noninteractive default must not replace its key or SSH config.
func TestGitLandUsesTheRepositoriesSSHCommand(t *testing.T) {
	t.Setenv("GIT_SSH_COMMAND", "")
	if err := os.Unsetenv("GIT_SSH_COMMAND"); err != nil {
		t.Fatal(err)
	}
	r := newLandRig(t)
	bin := t.TempDir()
	r.write(bin, "ssh", "#!/bin/sh\necho default-ssh-must-not-run >&2\nexit 97\n")
	if err := os.Chmod(filepath.Join(bin, "ssh"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }
	transport := filepath.Join(t.TempDir(), "repository ssh")
	called := filepath.Join(t.TempDir(), "transport-calls")
	script := "#!/bin/sh\ncase \"$*\" in\n" +
		"*git-upload-pack*) echo fetch >> " + quote(called) + "; exec git-upload-pack " + quote(r.remote) + ";;\n" +
		"*git-receive-pack*) echo push >> " + quote(called) + "; exec git-receive-pack " + quote(r.remote) + ";;\n" +
		"*) exit 0;;\nesac\n"
	if err := os.WriteFile(transport, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	r.git(r.repo, "config", "core.sshCommand", quote(transport)+" -i scoped-deploy-key")
	r.git(r.repo, "remote", "set-url", "origin", "ssh://git@example.invalid/repository.git")
	r.write(r.tree, "app.txt", "one\ntwo\nchanged\n")
	if _, err := r.land("The task uses the configured SSH transport"); err != nil {
		t.Fatal(err)
	}
	used, err := os.ReadFile(called)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(used), "fetch\n") || !strings.Contains(string(used), "push\n") {
		t.Fatalf("configured transport was not used for fetch and push: %q", used)
	}
	if got, want := r.git(r.remote, "rev-parse", "main"), r.git(r.tree, "rev-parse", "HEAD"); got != want {
		t.Fatalf("remote main = %s, want task head %s", got, want)
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
