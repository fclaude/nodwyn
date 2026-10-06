package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestCLIProjectTrustReviewAcceptAndRevoke(t *testing.T) {
	dir, repo := t.TempDir(), t.TempDir()
	if err := os.Mkdir(filepath.Join(repo, ".nodwyn"), 0755); err != nil {
		t.Fatal(err)
	}
	init := filepath.Join(repo, ".nodwyn", "init.scm")
	if err := os.WriteFile(init, []byte("(define marker 1)"), 0600); err != nil {
		t.Fatal(err)
	}
	run := func(wantOK bool, args ...string) string {
		t.Helper()
		cmd := exec.Command(os.Args[0], append([]string{"-test.run=^TestCLIHelper$", "--", "trust", "--repo", repo}, args...)...)
		cmd.Env = append(os.Environ(), "NODWYN_TEST_CLI=1", quickExit, "XDG_CONFIG_HOME="+filepath.Join(dir, "config"), "XDG_DATA_HOME="+filepath.Join(dir, "data"))
		out, err := cmd.CombinedOutput()
		if (err == nil) != wantOK {
			t.Fatalf("trust %v: %v\n%s", args, err, out)
		}
		return string(out)
	}
	review := run(true)
	revision := regexp.MustCompile(`Revision: ([0-9a-f]{64})`).FindStringSubmatch(review)
	if len(revision) != 2 || !strings.Contains(review, "(define marker 1)") {
		t.Fatalf("no review: %s", review)
	}
	trust := filepath.Join(dir, "config", "nodwyn", "trusted-projects.json")
	if _, err := os.Stat(trust); !os.IsNotExist(err) {
		t.Fatal("review alone granted trust")
	}
	run(true, "--accept", revision[1])
	if err := os.WriteFile(init, []byte("(define marker 2)"), 0600); err != nil {
		t.Fatal(err)
	}
	if out := run(false, "--accept", revision[1]); !strings.Contains(out, "changed") {
		t.Fatal(out)
	}
	run(true, "--revoke")
	raw, err := os.ReadFile(trust)
	if err != nil || strings.TrimSpace(string(raw)) != "{}" {
		t.Fatalf("trust retained: %q %v", raw, err)
	}
}
