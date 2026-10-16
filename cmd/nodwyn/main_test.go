package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fclaude/nodwyn/internal/config"
	fake "github.com/fclaude/nodwyn/test/fakes/ollama"
)

// quickExit spares the CLI, run as this test binary built with -race, the
// race detector's one-second sleep as it exits: a second for every command a
// test runs, which was two thirds of this package's time (2026-10-03).
// test/fakes/container does the same.
var quickExit = "GORACE=" + strings.TrimSpace(os.Getenv("GORACE")+" atexit_sleep_ms=0")

// Invoke the real CLI entry point in a subprocess, including flag parsing and
// configuration discovery, while retaining the test binary's race instrumentation.
func TestCLIHelper(t *testing.T) {
	if os.Getenv("NODWYN_TEST_CLI") != "1" {
		return
	}
	for i, arg := range os.Args {
		if arg == "--" {
			os.Args = append([]string{"nodwyn"}, os.Args[i+1:]...)
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
		cmd.Env = append(os.Environ(), "NODWYN_TEST_CLI=1", quickExit, "XDG_CONFIG_HOME="+filepath.Join(dir, "config"), "XDG_DATA_HOME="+filepath.Join(dir, "data"))
		cmd.Stdin = strings.NewReader(input)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%v: %v\n%s", args, err, out)
		}
		return string(out)
	}
	flags := []string{"--url", srv.URL, "--model", "test-model", "--no-keychain", "--no-confirm"}
	run("first ask\n:q\n", append(flags, "--repo", repo)...)
	// Test subprocess adds its PASS line after main returns.
	ids := strings.Fields(run("", "sessions", "--ids"))
	if len(ids) != 2 || ids[1] != "PASS" {
		t.Fatalf("ids: %q", ids)
	}
	out := run("second ask\n:q\n", append(flags, "--resume", ids[0])...)
	if !strings.Contains(out, "repo  "+config.CanonicalPath(repo)) || strings.Count(out, "original answer") != 1 || strings.Count(out, "Session resumed;") != 1 {
		t.Fatalf("bad resume transcript:\n%s", out)
	}
	if !strings.Contains(string(srv.Requests[1].Raw), "original answer") {
		t.Fatal("resumed model lost context")
	}
	listing := run("", "sessions")
	if !strings.Contains(listing, "first ask") || !strings.Contains(listing, "idle") || !strings.Contains(listing, config.CanonicalPath(repo)) {
		t.Fatalf("list: %s", listing)
	}
}

func TestCLIBudgetCheckpoint(t *testing.T) {
	srv := fake.New(fake.Turn{ToolCalls: []fake.ToolCall{{ID: "a", Name: "fs-list", Args: map[string]any{"path": "."}}, {ID: "b", Name: "fs-list", Args: map[string]any{"path": "."}}}}, fake.Turn{Content: "finished"})
	defer srv.Close()
	dir := t.TempDir()
	configDir := filepath.Join(dir, "config", "nodwyn")
	os.MkdirAll(configDir, 0700)
	os.WriteFile(filepath.Join(configDir, "config.toml"), []byte("[budget]\ntool_calls_soft=1\ntool_calls_hard=3\n"), 0600)
	cmd := exec.Command(os.Args[0], "-test.run=^TestCLIHelper$", "--", "--url", srv.URL, "--model", "test-model", "--no-keychain", "--budget-wall", "2h", "--budget-actions", "10")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "NODWYN_TEST_CLI=1", quickExit, "XDG_CONFIG_HOME="+filepath.Join(dir, "config"), "XDG_DATA_HOME="+filepath.Join(dir, "data"))
	cmd.Stdin = strings.NewReader("list twice\n4\n3\n:q\n")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("CLI: %v\n%s", err, out)
	}
	for _, want := range []string{"checkpoint: tool_calls", "hard limit", "finished"} {
		if !strings.Contains(string(out), want) {
			t.Fatalf("missing %q:\n%s", want, out)
		}
	}
	if len(srv.Requests) != 2 {
		t.Fatal("checkpoint failed to resume the turn")
	}
}

// Line mode starts with a control model of any provider: only Ollama
// answers /api/version, which it asked of every provider but openai, so an
// anthropic one ("404 page not found") never started.
func TestCLIStartsWithAnAnthropicControlModel(t *testing.T) {
	server := httptest.NewServer(http.NotFoundHandler())
	defer server.Close()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "config", "nodwyn"), 0o700); err != nil {
		t.Fatal(err)
	}
	conf := "[control]\nprovider = \"anthropic\"\nurl = \"" + server.URL + "\"\nmodel = \"claude-test\"\n"
	if err := os.WriteFile(filepath.Join(dir, "config", "nodwyn", "config.toml"), []byte(conf), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestCLIHelper$", "--", "--no-keychain", "--no-confirm", "--repo", t.TempDir())
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "NODWYN_TEST_CLI=1", quickExit, "XDG_CONFIG_HOME="+filepath.Join(dir, "config"), "XDG_DATA_HOME="+filepath.Join(dir, "data"))
	cmd.Stdin = strings.NewReader(":q\n")
	out, _ := cmd.CombinedOutput()
	if strings.Contains(string(out), "unreachable") || !strings.Contains(string(out), "control model: "+server.URL+" claude-test (anthropic)") {
		t.Fatalf("line mode with an anthropic control model:\n%s", out)
	}
}
