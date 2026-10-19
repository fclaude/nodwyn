package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fclaude/nodwyn/internal/config"
	"github.com/fclaude/nodwyn/internal/core"
	"github.com/fclaude/nodwyn/internal/frontend/rpc"
	"github.com/fclaude/nodwyn/internal/frontend/run"
	"github.com/fclaude/nodwyn/internal/llm"
	fake "github.com/fclaude/nodwyn/test/fakes/ollama"
)

func TestCLIBoundedRun(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "nodwyn-run-cli-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	// The conversation is pinned turn by turn; the default loop's explorer and
	// evidence reminders are off here and tested in internal/core.
	// The task folder sits beside the daemon's config and data, never around them.
	repo := filepath.Join(dir, "repo")
	os.MkdirAll(filepath.Join(repo, ".nodwyn"), 0o755)
	os.WriteFile(filepath.Join(repo, ".nodwyn", "init.scm"), []byte("(define plan-explore-rounds 0)\n(define finish-reminders 0)\n"), 0o644)
	socket := filepath.Join(dir, "sock")
	listener, err := rpc.Listen(socket)
	if err != nil {
		t.Fatal(err)
	}
	// The second run is in sandbox mode, so nobody approves it, the lean
	// loop it runs asks no planner, and its turn is asked to check its result
	// before it ends.
	srv := fake.New(fake.Turn{Content: "1. Return the requested answer."}, fake.Turn{Content: "done\n" + run.CompleteLine},
		fake.Turn{Content: "done\n" + run.CompleteLine}, fake.Turn{Content: "checked\n" + run.CompleteLine})
	defer srv.Close()
	paths := config.PathsUnder(filepath.Join(dir, "config"), filepath.Join(dir, "data"))
	files, err := config.ReadProjectFiles(repo)
	if err != nil {
		t.Fatal(err)
	}
	if err := config.TrustProject(context.Background(), paths, repo, files.Revision()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- rpc.Serve(ctx, listener, core.Options{Config: config.Default(), Paths: paths, Provider: llm.NewOllama(srv.URL)})
	}()
	defer func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()
	task := filepath.Join(dir, "task.txt")
	os.WriteFile(task, []byte("Return an answer."), 0600)
	invoke := func(args ...string) []byte {
		t.Helper()
		callCtx, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		cmd := exec.CommandContext(callCtx, os.Args[0], append([]string{"-test.run=^TestCLIHelper$", "--", "run"}, args...)...)
		cmd.Env = append(os.Environ(), "NODWYN_TEST_CLI=1", quickExit, "XDG_CONFIG_HOME="+filepath.Join(dir, "cli-config"), "XDG_DATA_HOME="+filepath.Join(dir, "cli-data"))
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("CLI %v: %v %s", args, err, stderr.String())
		}
		return bytes.SplitN(out, []byte("\n"), 2)[0]
	}
	var started struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(invoke("start", "--socket", socket, "--repo", repo, "--task", task, "--budget-wall", "1m", "--max-turns", "2"), &started); err != nil || started.ID == "" {
		t.Fatalf("start %v %+v", err, started)
	}
	// The submitting CLI has exited; a new invocation sees daemon-owned progress.
	var state run.State
	ended := func(id string) {
		t.Helper()
		for end := time.Now().Add(4 * time.Second); time.Now().Before(end); {
			if err := json.Unmarshal(invoke("status", "--socket", socket, id), &state); err != nil {
				t.Fatal(err)
			}
			if state.Status != "running" {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	ended(started.ID)
	if state.Status != "completed" || state.Turns != 1 || state.Sandbox {
		t.Fatalf("%+v", state)
	}
	// --sandbox container is sandbox mode (ADR 55), which needs neither
	// --yolo nor --unattended said: it is both.
	if err := json.Unmarshal(invoke("start", "--socket", socket, "--repo", repo, "--task", task, "--budget-wall", "1m", "--max-turns", "1", "--sandbox", "container"), &started); err != nil || started.ID == "" {
		t.Fatalf("start --sandbox %v %+v", err, started)
	}
	ended(started.ID)
	if state.Status != "completed" || !state.Sandbox || !state.Unattended || !state.AutoApprove {
		t.Fatalf("--sandbox container: %+v", state)
	}
}

// --sandbox takes "container" alone, and excludes --judge, before anything
// is sent to a daemon.
func TestRunSandboxFlagIsChecked(t *testing.T) {
	task := filepath.Join(t.TempDir(), "task.txt")
	if err := os.WriteFile(task, []byte("t"), 0o600); err != nil {
		t.Fatal(err)
	}
	for args, want := range map[string]string{
		"--sandbox vm":                  `--sandbox takes "container"`,
		"--sandbox container --judge":   "--sandbox and --judge exclude each other",
		"--sandbox=container --judge=1": "--sandbox and --judge exclude each other",
	} {
		err := runTask(append(strings.Fields(args), "--task", task, "--budget-wall", "1m", "--max-turns", "1"))
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v", args, err)
		}
	}
}
func TestRunRejectsImplicitLimits(t *testing.T) {
	for _, args := range [][]string{{}, {"start"}, {"--task", "missing", "--max-turns", "2"}, {"start", "--task", "missing", "--max-turns", "2"}, {"resume", "--budget-wall", "2h", "id"}} {
		if err := runTask(args); err == nil || strings.Contains(err.Error(), "daemon") {
			t.Fatalf("did not reject invalid request before connecting: %v", err)
		}
	}
}
