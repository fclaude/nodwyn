package prim

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// (project-instructions) is the repository's AGENTS.md, which rides in
// every request's system prompt: "" without one, AGENTS.md before
// agents.md, secrets redacted, capped, and never a file a link reaches
// outside the root or past the credential denylist.
func TestProjectInstructions(t *testing.T) {
	h := newHarness(t)
	read := func() string {
		t.Helper()
		return h.mustEval(t, `(project-instructions)`).S
	}
	if got := read(); got != "" {
		t.Fatalf("no AGENTS.md: %q", got)
	}

	lower := filepath.Join(h.root, "agents.md")
	os.WriteFile(lower, []byte("- run go test ./...\n"), 0o644)
	if got := read(); got != "- run go test ./...\n" {
		t.Fatalf("agents.md: %q", got)
	}
	upper := filepath.Join(h.root, "AGENTS.md")
	const canary = "sk-ant-api03-abcdefghijklmnopqrstuvwxyz0123456789"
	os.WriteFile(upper, []byte("- test with make test\n- key: "+canary+"\n"), 0o644)
	// Both exist on a case-sensitive file system; elsewhere they are one file.
	got := read()
	if !strings.HasPrefix(got, "- test with make test\n") {
		t.Fatalf("AGENTS.md beside agents.md: %q", got)
	}
	if strings.Contains(got, canary) {
		t.Fatalf("a credential in AGENTS.md reached the prompt: %q", got)
	}
	os.Remove(lower)

	long := strings.Repeat("- a rule that goes on\n", 2000)
	os.WriteFile(upper, []byte(long), 0o644)
	if got := read(); len(got) > maxProjectInstructions+100 || !strings.HasSuffix(got, "\n[AGENTS.md is longer: read the rest with fs-read]") ||
		!strings.HasPrefix(got, "- a rule that goes on\n") {
		t.Fatalf("long AGENTS.md: %d bytes, ending %q", len(got), got[max(0, len(got)-80):])
	}

	// A link is followed only to a file the session could read anyway.
	for _, target := range []string{
		filepath.Join(filepath.Dir(h.root), "outside-"+filepath.Base(h.root)), // outside the root
		filepath.Join(h.root, ".env"),                                         // on the denylist
		filepath.Join(h.root, "sub"),                                          // not a file
	} {
		os.Remove(upper)
		if err := os.Symlink(target, upper); err != nil {
			t.Fatal(err)
		}
		if got := read(); got != "" {
			t.Errorf("AGENTS.md linked to %s read as %q", target, got)
		}
	}
	os.Remove(upper)
	os.Symlink(filepath.Join(h.root, "README.md"), upper)
	if got := read(); got != "hello\nworld\n" {
		t.Fatalf("AGENTS.md linked inside the root: %q", got)
	}
}
