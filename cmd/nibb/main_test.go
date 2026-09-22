package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	fake "github.com/fclaude/nibb/test/fakes/ollama"
)

// Invoke the real CLI entry point in a subprocess, including flag parsing and
// configuration discovery, while retaining the test binary's race instrumentation.
func TestCLIHelper(t *testing.T) {
	if os.Getenv("NIBB_TEST_CLI") != "1" {
		return
	}
	for i, arg := range os.Args {
		if arg == "--" {
			os.Args = append([]string{"nibb"}, os.Args[i+1:]...)
			main()
			return
		}
	}
	t.Fatal("missing CLI arguments")
}

func TestCLIResume(t *testing.T) {
	srv := fake.New(fake.Turn{Content: "original answer"}, fake.Turn{Content: "continued answer"})
	defer srv.Close()
	dir := t.TempDir()
	repo := t.TempDir()
	run := func(input string, args ...string) string {
		t.Helper()
		cmd := exec.Command(os.Args[0], append([]string{"-test.run=^TestCLIHelper$", "--"}, args...)...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "NIBB_TEST_CLI=1", "XDG_CONFIG_HOME="+filepath.Join(dir, "config"), "XDG_DATA_HOME="+filepath.Join(dir, "data"))
		cmd.Stdin = strings.NewReader(input)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%v: %v\n%s", args, err, out)
		}
		return string(out)
	}
	flags := []string{"--url", srv.URL, "--no-keychain", "--no-confirm"}
	run("first ask\n:q\n", append(flags, "--repo", repo)...)
	// Test subprocess adds its PASS line after main returns.
	ids := strings.Fields(run("", "sessions", "--ids"))
	if len(ids) != 2 || ids[1] != "PASS" {
		t.Fatalf("ids: %q", ids)
	}
	out := run("second ask\n:q\n", append(flags, "--resume", ids[0])...)
	if !strings.Contains(out, "repo  "+repo) || strings.Count(out, "original answer") != 1 || strings.Count(out, "Session resumed;") != 1 {
		t.Fatalf("bad resume transcript:\n%s", out)
	}
	if !strings.Contains(string(srv.Requests[1].Raw), "original answer") {
		t.Fatal("resumed model lost context")
	}
	listing := run("", "sessions")
	if !strings.Contains(listing, "first ask") || !strings.Contains(listing, "idle") || !strings.Contains(listing, repo) {
		t.Fatalf("list: %s", listing)
	}
}
