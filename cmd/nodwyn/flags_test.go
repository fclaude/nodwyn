package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// One rule places the flags: the shared ones go before the command, and a
// command refuses the ones it does not read, whether they come before it or
// after. They used to be taken in silence by sessions, replay, memory and
// the rest, or for a file name or an argument.
func TestSharedFlagsAreRefusedWhereTheyDoNothing(t *testing.T) {
	dir := t.TempDir()
	run := func(args ...string) (string, error) {
		cmd := exec.Command(os.Args[0], append([]string{"-test.run=^TestCLIHelper$", "--"}, args...)...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "NODWYN_TEST_CLI=1", quickExit, "XDG_CONFIG_HOME="+filepath.Join(dir, "config"), "XDG_DATA_HOME="+filepath.Join(dir, "data"))
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"--model", "x", "--yolo", "sessions"}, "sessions: --model is not an option of sessions"},
		{[]string{"--repo", "/tmp", "paths"}, "paths: --repo is not an option of paths"},
		{[]string{"--yolo", "memory", "search", "words"}, "memory: --yolo is not an option of memory"},
		{[]string{"--model", "x", "replay", "ID"}, "replay: --model is not an option of replay"},
		{[]string{"--repo", "/tmp", "eval", "-e", "1"}, "eval: --repo is not an option of eval"},
		{[]string{"versions", "foo.txt", "--repo", "/tmp"}, "versions: put --repo before versions"},
		{[]string{"replay", "ID", "--show-thinking"}, "replay: put --show-thinking before replay"},
		{[]string{"chat", "--repo", "/tmp"}, "chat: put --repo before chat"},
		{[]string{"chat", "garbage"}, `chat: unexpected argument "garbage"`},
		// The message names the command that takes pipes, not a flag that
		// selects nothing.
		{[]string{"tui"}, "use nodwyn chat (line mode) for pipes"},
	} {
		out, err := run(c.args...)
		if err == nil || !strings.Contains(out, c.want) {
			t.Errorf("%v: %v\n%s\nwant %q", c.args, err, out, c.want)
		}
	}
	// What a command does read goes before it, and the deprecated --plain,
	// which nothing reads, still passes where a script may have it.
	for _, args := range [][]string{{"--show-thinking", "replay", "missing.jsonl"}, {"--plain", "sessions", "--ids"}, {"--repo", dir, "versions", "x.txt"}} {
		if out, err := run(args...); strings.Contains(out, "option of") || strings.Contains(out, "put --") {
			t.Errorf("%v refused: %v\n%s", args, err, out)
		}
	}
	if out, err := run("--plain", "sessions", "--ids"); err != nil {
		t.Errorf("--plain sessions: %v\n%s", err, out)
	}
}
