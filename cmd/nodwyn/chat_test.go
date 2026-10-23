package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fclaude/nodwyn/internal/config"
	"github.com/fclaude/nodwyn/internal/frontend/rpc"
	"github.com/fclaude/nodwyn/test/fakes/must"
	fake "github.com/fclaude/nodwyn/test/fakes/ollama"
)

// chatIn runs line mode in dir's environment on input, and says what it
// printed and how it ended.
func chatIn(t *testing.T, env []string, dir, input string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command(os.Args[0], append([]string{"-test.run=^TestCLIHelper$", "--"}, args...)...)
	cmd.Dir = dir
	cmd.Env = env
	cmd.Stdin = strings.NewReader(input)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// envValue is the last value env gives key.
func envValue(env []string, key string) string {
	value := ""
	for _, kv := range env {
		if k, v, ok := strings.Cut(kv, "="); ok && k == key {
			value = v
		}
	}
	return value
}

// pathsIn are the paths a command run in dir's environment uses.
func pathsIn(dir string) config.Paths {
	return config.PathsUnder(filepath.Join(dir, "config", "nodwyn"), filepath.Join(dir, "data", "nodwyn"))
}

// startDaemon serves paths' tasks on the standard socket of env's runtime
// directory, as nodwyn app would, and connects to it as the console does.
func startDaemon(t *testing.T, env []string, paths config.Paths) *rpc.Client {
	t.Helper()
	socket := filepath.Join(envValue(env, "XDG_RUNTIME_DIR"), "nodwyn", "sock")
	must.MkdirAll(t, filepath.Dir(socket), 0o700)
	ctx, cancel := context.WithCancel(context.Background())
	owned, done, err := ensureDaemon(ctx, paths, socket, "test")
	if err != nil || !owned {
		t.Fatalf("no daemon started: %v", err)
	}
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	})
	console, err := rpc.Dial(ctx, socket)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(console.Close)
	return console
}

// The flags line mode took while it hosted its own session, which a task in
// the daemon has no form for, are refused with where the setting is now,
// before anything is opened; so is a --model nothing configures.
func TestChatRefusesWhatTheDaemonHasNoFormFor(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, controlOn("http://127.0.0.1:1", "test-model"))
	env := lineEnv(t, dir)
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"--url", "http://127.0.0.1:2"}, "--url is no longer taken: the daemon reaches only the servers config.toml names"},
		{[]string{"--budget-tokens", "10"}, "--budget-tokens is no longer taken: a task in the daemon stops at the limits config.toml sets for every task: set [budget] tokens_hard"},
		{[]string{"--budget-actions", "10"}, "[budget] actions_hard"},
		{[]string{"--budget-wall", "2h"}, "[budget] wall_hard"},
		{[]string{"--budget-cost", "1"}, "[budget] cost_hard"},
		{[]string{"--max-tool-calls", "3"}, "[budget] tool_calls_hard"},
		{[]string{"--model", "other"}, "--model other: no [models.other] is configured, and [control] names no [servers.NAME] for it to be on"},
	} {
		out, err := chatIn(t, env, dir, ":q\n", append(c.args, "--no-keychain", "--repo", t.TempDir())...)
		if err == nil || !strings.Contains(out, c.want) || strings.Contains(out, "nodwyn session") {
			t.Errorf("%v: %v\n%s\nwant %q", c.args, err, out, c.want)
		}
	}
	if entries, _ := os.ReadDir(pathsIn(dir).SessionsDir); len(entries) != 0 {
		t.Fatalf("a refused command opened %d tasks", len(entries))
	}
}

// Line mode opens its task in the daemon on the standard socket. One
// started with another data directory (another XDG_DATA_HOME, as a test or
// a script sets) keeps tasks elsewhere, with its own configuration: line
// mode refuses it, closing the task it opened there, rather than talk to
// models and keys that are not this command's.
func TestLineModeRefusesADaemonOfAnotherDataDirectory(t *testing.T) {
	model := fake.NewT(t)
	theirs, ours := t.TempDir(), t.TempDir()
	writeConfig(t, theirs, controlOn(model.URL, "their-model"))
	writeConfig(t, ours, controlOn(model.URL, "test-model"))
	env := lineEnv(t, ours)
	console := startDaemon(t, env, pathsIn(theirs))
	out, err := chatIn(t, env, ours, "hello\n:q\n", "--no-keychain", "--repo", t.TempDir())
	if err == nil || !strings.Contains(out, "keeps its tasks outside "+pathsIn(ours).SessionsDir) {
		t.Fatalf("line mode used a daemon of another data directory: %v\n%s", err, out)
	}
	var tasks []rpc.SessionInfo
	if err := console.Call(context.Background(), "session.list", struct{}{}, &tasks); err != nil {
		t.Fatal(err)
	}
	for _, task := range tasks {
		if task.Open {
			t.Fatalf("the task line mode opened is still open there: %+v", task)
		}
	}
	if len(model.Requests) != 0 {
		t.Fatalf("the model was asked %d times", len(model.Requests))
	}
}

// --yolo turns on the daemon's Don't ask for the task, which Settings must
// offer ([approval] dont_ask), as it must for the console's switch; without
// it --yolo is refused in the daemon's words.
func TestYoloTurnsOnDontAsk(t *testing.T) {
	model := fake.NewT(t,
		fake.Turn{ToolCalls: []fake.ToolCall{{ID: "w", Name: "fs-write", Args: map[string]any{"path": "a.txt", "content": "a"}}}},
		fake.Turn{Content: "written"})
	dir, repo := t.TempDir(), t.TempDir()
	writeConfig(t, dir, controlOn(model.URL, "test-model")+"[approval]\nwrites = \"ask\"\n")
	env := lineEnv(t, dir)
	out, err := chatIn(t, env, dir, "write a\n:q\n", "--yolo", "--no-keychain", "--repo", repo)
	if err == nil || !strings.Contains(out, "--yolo turns on Don't ask for the task") || !strings.Contains(out, "Don't ask is not offered") {
		t.Fatalf("--yolo without the setting: %v\n%s", err, out)
	}
	writeConfig(t, dir, controlOn(model.URL, "test-model")+"[approval]\nwrites = \"ask\"\ndont_ask = true\n")
	out, err = chatIn(t, env, dir, "write a\n:q\n", "--yolo", "--no-keychain", "--repo", repo)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if _, err := os.Stat(filepath.Join(repo, "a.txt")); err != nil || !strings.Contains(out, "[don't ask: on") || strings.Contains(out, "allow?") {
		t.Fatalf("the write was asked, or not made: %v\n%s", err, out)
	}
}

// A task the daemon already had open for another client stays open there
// when line mode quits, as a tab closed in the console leaves it, and
// without the Don't ask --yolo turned on; one line mode opened it closes, as
// quitting always did.
func TestLineModeLeavesATaskAnotherClientHasOpen(t *testing.T) {
	model := fake.NewT(t, fake.Turn{Content: "from line mode"})
	dir, repo := t.TempDir(), t.TempDir()
	writeConfig(t, dir, controlOn(model.URL, "test-model")+"[approval]\ndont_ask = true\n")
	env := lineEnv(t, dir)
	console := startDaemon(t, env, pathsIn(dir))
	ctx := context.Background()
	var opened rpc.SessionDetails
	if err := console.Call(ctx, "session.open", map[string]any{"repo": repo}, &opened); err != nil {
		t.Fatal(err)
	}
	out, err := chatIn(t, env, dir, "hello\n:q\n", "--yolo", "--no-keychain", "--resume", opened.ID)
	if err != nil || !strings.Contains(out, "from line mode") || !strings.Contains(out, "the task stays open in the daemon") {
		t.Fatalf("%v\n%s", err, out)
	}
	var info rpc.SessionDetails
	if err := console.Call(ctx, "session.info", map[string]any{"id": opened.ID}, &info); err != nil {
		t.Fatalf("line mode closed a task the console has open: %v", err)
	}
	if info.ApproveAll {
		t.Fatal("Don't ask is still on after line mode left")
	}
	out, err = chatIn(t, env, dir, ":q\n", "--no-keychain", "--repo", repo)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	var tasks []rpc.SessionInfo
	if err := console.Call(ctx, "session.list", struct{}{}, &tasks); err != nil {
		t.Fatal(err)
	}
	for _, task := range tasks {
		if task.ID != opened.ID && task.Open {
			t.Fatalf("the task line mode opened is still open: %+v", task)
		}
	}
}
