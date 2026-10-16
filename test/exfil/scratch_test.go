package exfil

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fclaude/nodwyn/internal/config"
	"github.com/fclaude/nodwyn/internal/core"
	"github.com/fclaude/nodwyn/internal/guard"
	"github.com/fclaude/nodwyn/internal/llm"
)

// ADR 28 (amended 2026-10-16): a session's scratch directory is outside
// nodwyn's configuration and data directories, so the commands it is the
// TMPDIR of work as they do anywhere, and the protection of those
// directories is what it was: the files in them are refused by name, by
// path under the default denylist a program builds for itself, and through a
// link made in the scratch directory, and nothing in them reaches the log.
func TestScratchIsOutsideTheProtectedDirectoriesWhichStayDenied(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_DATA_HOME", "")
	paths, err := config.DefaultPaths()
	if err != nil {
		t.Fatal(err)
	}
	// The data and configuration directories are inside the folder the
	// session works in, so the repository pin lets their paths through and
	// the denylist alone decides.
	repo := home
	secrets := map[string]string{
		filepath.Join(paths.SessionsDir, "other.jsonl"): "log " + canary,
		filepath.Join(paths.ConfigDir, "notes.txt"):     "config " + canary,
	}
	for path, text := range secrets {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	logb := &strings.Builder{}
	s, err := core.New(core.Options{Config: config.Default(), Repo: repo, Paths: paths, NoConfirm: true, Provider: llm.NewOllama("http://127.0.0.1:1"), LogWriter: logb})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	v, err := s.Eval(t.Context(), `(scratch-dir)`)
	if err != nil {
		t.Fatal(err)
	}
	scratch := v.S
	for _, dir := range []string{paths.DataDir, paths.ConfigDir} {
		if guard.DefaultDenylist().Denies(scratch) != "" || strings.HasPrefix(scratch, config.CanonicalPath(dir)+string(filepath.Separator)) {
			t.Fatalf("the scratch directory %q is in the protected directory %q", scratch, dir)
		}
	}

	// The scratch directory takes the model's files, and a program that makes
	// its own temporary files there, under the default denylist, is not refused.
	for _, src := range []string{`(fs-write "$TMPDIR/check.sh" "echo ok\n")`, `(fs-read "$TMPDIR/check.sh")`} {
		if _, err := s.Eval(t.Context(), src); err != nil {
			t.Errorf("%s: %v", src, err)
		}
	}
	if reason := guard.DefaultDenylist().Denies(filepath.Join(scratch, "TestX001", "README.md")); reason != "" {
		t.Errorf("a temporary file in TMPDIR is refused by the default denylist: %s", reason)
	}

	// The protected directories are still refused: by path, through a link
	// in the scratch directory, and for a program's own default denylist.
	if err := os.Symlink(paths.DataDir, filepath.Join(scratch, "data")); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{
		filepath.Join(paths.SessionsDir, "other.jsonl"),
		filepath.Join(paths.ConfigDir, "notes.txt"),
		"$TMPDIR/data/sessions/other.jsonl",
	} {
		_, err := s.Eval(t.Context(), `(fs-read "`+path+`")`)
		if err == nil || !strings.Contains(err.Error(), "denied") {
			t.Errorf("fs-read %s: %v", path, err)
		}
		if err != nil && strings.Contains(err.Error(), canary) {
			t.Errorf("fs-read %s: the error holds the canary", path)
		}
	}
	for _, path := range []string{filepath.Join(paths.SessionsDir, "other.jsonl"), filepath.Join(paths.ConfigDir, "notes.txt")} {
		if guard.DefaultDenylist().Denies(path) == "" {
			t.Errorf("the default denylist no longer protects %s", path)
		}
	}
	if strings.Contains(logb.String(), canary) {
		t.Error("the canary reached the log")
	}
}
