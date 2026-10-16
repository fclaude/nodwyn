package core

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fclaude/nodwyn/internal/config"
	"github.com/fclaude/nodwyn/internal/guard"
	"github.com/fclaude/nodwyn/internal/llm"
	fake "github.com/fclaude/nodwyn/test/fakes/ollama"
)

// A session's scratch directory takes throwaway files without a prompt, is
// the TMPDIR of its commands, lives under the system's temporary directory
// and not in nodwyn's data directory, which the guard protects, and is gone
// when the session closes; a resumed session gets a new, empty one. The
// system prompt names $TMPDIR, not the per-session path.
func TestScratchDirectory(t *testing.T) {
	t.Parallel()
	srv := fake.NewT(t, fake.Turn{Content: "ok"}, fake.Turn{Content: "ok"})
	defer srv.Close()
	o := diskOptions(t) // NoConfirm: any prompt would be refused
	o.Provider = llm.NewOllama(srv.URL)
	os.MkdirAll(filepath.Join(o.Repo, ".nodwyn"), 0o755)
	os.WriteFile(filepath.Join(o.Repo, ".nodwyn", "init.scm"), []byte(`
(define seen #f)
(define (agent-turn request)
  (let ((file (string-append (scratch-dir) "/repro.py")))
    (fs-write file "print(1)\n")
    (set! seen (fs-read file))
    (model-turn)))`), 0o644)
	s, err := newInitSession(o)
	if err != nil {
		t.Fatal(err)
	}
	dir := s.scratch
	data, _ := filepath.EvalSymlinks(o.Paths.DataDir)
	if dir == "" || strings.HasPrefix(dir, o.Repo) || !strings.HasPrefix(dir, scratchRoot()+string(filepath.Separator)) || strings.HasPrefix(dir, data+string(filepath.Separator)) {
		t.Fatalf("scratch directory %q: want one under the system's temporary directory %q, outside the data directory %q", dir, scratchRoot(), data)
	}
	if info, err := os.Stat(dir); err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("scratch directory mode: %v %v", info, err)
	}
	if prompt := s.loop.WindowContext().System; strings.Contains(prompt, dir) || !strings.Contains(prompt, "$TMPDIR") {
		t.Fatalf("the system prompt should name $TMPDIR, not the path:\n%s", prompt)
	}
	r := &rig{t: t, s: s, srv: srv}
	if e := r.turn("go"); e != "" {
		t.Fatalf("a scratch write was refused: %s", e)
	}
	if v, err := s.Eval(t.Context(), "seen"); err != nil || v.S != "print(1)\n" {
		t.Fatalf("read back %v %v", v, err)
	}
	if _, err := os.Stat(filepath.Join(o.Repo, "repro.py")); err == nil {
		t.Fatal("the scratch file landed in the repository")
	}
	id := s.ID()
	s.Close()
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("scratch directory left behind after close: %v", err)
	}

	o.Repo, o.NoConfirm, o.AutoApprove = "", false, true
	resumed, err := Resume(o, id)
	if err != nil {
		t.Fatal(err)
	}
	defer resumed.Close()
	if resumed.scratch == "" || resumed.scratch == dir {
		t.Fatalf("resumed scratch %q, was %q", resumed.scratch, dir)
	}
	v, err := resumed.Eval(t.Context(), `(assoc-ref (shell-exec "sh" '("-c" "printf %s \"$TMPDIR\"")) "stdout")`)
	if err != nil || v.S != resumed.scratch {
		t.Fatalf("TMPDIR in commands: %q %v", v.S, err)
	}
}

// A scratch directory a crashed session left behind is swept once it is a
// week old; a recent one is kept. The system's temporary directory is shared,
// so only this user's directories named for a scratch directory go, never a
// link to one; and the directories sessions before 2026-10-16 left in the
// data directory go too.
func TestScratchSweep(t *testing.T) {
	root := t.TempDir()
	t.Setenv("TMPDIR", root)
	root = scratchRoot()
	data := t.TempDir()
	legacy := legacyScratchRoot(data)
	os.MkdirAll(filepath.Join(root, "nodwyn-scratch-old-1"), 0o700)
	os.MkdirAll(filepath.Join(root, "nodwyn-scratch-new-1"), 0o700)
	os.MkdirAll(filepath.Join(root, "unrelated"), 0o700)
	os.MkdirAll(filepath.Join(legacy, "nodwyn-scratch-old-2"), 0o700)
	os.MkdirAll(filepath.Join(legacy, "nodwyn-scratch-new-2"), 0o700)
	week := time.Now().Add(-8 * 24 * time.Hour)
	elsewhere := t.TempDir()
	os.Chtimes(elsewhere, week, week)
	if err := os.Symlink(elsewhere, filepath.Join(root, "nodwyn-scratch-link-1")); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{filepath.Join(root, "nodwyn-scratch-old-1"), filepath.Join(root, "unrelated"), filepath.Join(legacy, "nodwyn-scratch-old-2")} {
		os.Chtimes(p, week, week)
	}
	dir, err := makeScratch(data, "s1")
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(dir) != root {
		t.Fatalf("scratch directory %q is not directly in %q", dir, root)
	}
	for path, want := range map[string]bool{
		filepath.Join(root, "nodwyn-scratch-old-1"):  false,
		filepath.Join(root, "nodwyn-scratch-new-1"):  true,
		filepath.Join(root, "unrelated"):             true,
		filepath.Join(root, "nodwyn-scratch-link-1"): true,
		elsewhere: true,
		filepath.Join(legacy, "nodwyn-scratch-old-2"): false,
		filepath.Join(legacy, "nodwyn-scratch-new-2"): true,
	} {
		if _, err := os.Lstat(path); (err == nil) != want {
			t.Errorf("%s: exists=%v, want %v", path, err == nil, want)
		}
	}
	removeScratch(dir)
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("removeScratch left the directory")
	}
	// A session touches its directory every turn, so however long it stays
	// open, the sweep keeps it.
	live := filepath.Join(root, "nodwyn-scratch-live-1")
	os.MkdirAll(live, 0o700)
	os.Chtimes(live, week, week)
	touchScratch(live)
	sweepScratch(root)
	if _, err := os.Stat(live); err != nil {
		t.Fatalf("the sweep removed a directory touched just now: %v", err)
	}
}

// With nodwyn's default layout (HOME, no XDG overrides) the data directory
// is ~/.local/share/nodwyn, which the denylist protects, and the scratch
// directory is not in it: the model's fs-write and fs-read of $TMPDIR/...
// go through, index-grep searches it, a program run with that TMPDIR that
// builds a guard of its own (nodwyn's tests, run by nodwyn) is not refused
// its temporary files, and the data directory, named directly or through a
// link in the scratch directory, stays denied.
func TestScratchDirectoryWithTheDefaultLayout(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_DATA_HOME", "")
	paths, err := config.DefaultPaths()
	if err != nil {
		t.Fatal(err)
	}
	if paths.DataDir != filepath.Join(home, ".local", "share", "nodwyn") {
		t.Fatalf("data directory %q", paths.DataDir)
	}
	srv := fake.NewT(t, fake.Turn{ToolCalls: []fake.ToolCall{
		{ID: "w", Name: "fs-write", Args: map[string]any{"path": "$TMPDIR/a.py", "content": "print(1)\n"}},
		{ID: "r", Name: "fs-read", Args: map[string]any{"path": "$TMPDIR/a.py"}},
	}}, fake.Turn{Content: "done"})
	defer srv.Close()
	logb := &bytes.Buffer{}
	s, err := New(Options{Repo: t.TempDir(), Paths: paths, Config: config.Default(), NoConfirm: true, Provider: llm.NewOllama(srv.URL), LogWriter: logb})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	r := &rig{t: t, s: s, srv: srv, logb: logb}
	if e := r.turn("go"); e != "" {
		t.Fatal(e)
	}
	for id, e := range r.results() {
		if e != "" {
			t.Errorf("call %s: %s", id, e)
		}
	}
	if b, err := os.ReadFile(filepath.Join(s.scratch, "a.py")); err != nil || string(b) != "print(1)\n" {
		t.Fatalf("the model's scratch file: %q %v", b, err)
	}
	if !strings.HasPrefix(s.scratch, scratchRoot()+string(filepath.Separator)) {
		t.Fatalf("scratch directory %q, want one under %q", s.scratch, scratchRoot())
	}
	// What a program run with this TMPDIR meets: its own guard, built from
	// the default denylist, which protects ~/.local/share/nodwyn. Until
	// 2026-10-16 the scratch directory was in it, so every temporary
	// repository of a test run by the model was refused ("path README.md is
	// under the protected directory"), and the model went to /tmp, or set HOME
	// and TMPDIR itself (commit-bench, 5 to 17 calls a run).
	made := filepath.Join(s.scratch, "TestSomething001", "README.md")
	if reason := guard.DefaultDenylist().Denies(made); reason != "" {
		t.Errorf("a file a program makes in TMPDIR is refused by the default denylist: %s", reason)
	}
	if reason := guard.DefaultDenylist().Denies(filepath.Join(paths.DataDir, "scratch", "x.go")); reason == "" {
		t.Error("the data directory is no longer protected by the default denylist")
	}
	if got := r.eval(`(let ((found (index-grep "$TMPDIR" "print"))) (list (assoc-ref found "files") (assoc-ref found "skipped")))`); got != "(1 0)" {
		t.Errorf("index-grep in the scratch directory: files and skipped %s", got)
	}

	secret := filepath.Join(paths.SessionsDir, "other.jsonl")
	os.MkdirAll(paths.SessionsDir, 0o700)
	os.WriteFile(secret, []byte("log"), 0o600)
	os.Symlink(paths.DataDir, filepath.Join(s.scratch, "data"))
	for _, path := range []string{secret, "$TMPDIR/data/sessions/other.jsonl"} {
		if _, err := s.Eval(t.Context(), `(fs-read "`+path+`")`); err == nil || !strings.Contains(err.Error(), "denied") {
			t.Errorf("fs-read %s: %v", path, err)
		}
	}
}

// A TMPDIR that an operator put inside nodwyn's data directory puts the
// scratch directory there, and it is still the one place in it the tools may
// use: the exception to the denylist is by what the session made, not by
// where the system keeps temporary files (ADR 28). The rest of the directory,
// the temporary directory's other files included, stays denied.
func TestScratchInsideTheDataDirectoryIsStillTheOnePlaceToUse(t *testing.T) {
	// The data directory is inside the repository, so the repository pin
	// lets every path through and only the denylist is asked.
	repo := t.TempDir()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(repo, "xdg-config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(repo, "xdg-data"))
	paths, err := config.DefaultPaths()
	if err != nil {
		t.Fatal(err)
	}
	tmp := filepath.Join(paths.DataDir, "tmp")
	if err := os.MkdirAll(tmp, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMPDIR", tmp)
	if err := os.WriteFile(filepath.Join(tmp, "other.txt"), []byte("not the session's"), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := New(Options{Repo: repo, Paths: paths, Config: config.Default(), NoConfirm: true, Provider: llm.NewOllama("http://127.0.0.1:1")})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if !strings.HasPrefix(s.scratch, config.CanonicalPath(tmp)+string(filepath.Separator)) {
		t.Fatalf("scratch directory %q, want one in %q", s.scratch, tmp)
	}
	for _, src := range []string{`(fs-write "$TMPDIR/a.py" "print(1)\n")`, `(fs-read "$TMPDIR/a.py")`} {
		if _, err := s.Eval(t.Context(), src); err != nil {
			t.Errorf("%s: %v", src, err)
		}
	}
	for _, path := range []string{"xdg-data/nodwyn/tmp/other.txt", "xdg-data/nodwyn/tmp", "xdg-config/nodwyn/bindings.toml"} {
		if _, err := s.Eval(t.Context(), `(fs-read "`+path+`")`); err == nil || !strings.Contains(err.Error(), "protected directory") {
			t.Errorf("fs-read %s: %v", path, err)
		}
	}
}

// The guard denies nodwyn's configuration and data directories where the
// session found them, not only at their default places: with XDG_DATA_HOME
// inside the repository, the data directory there is still off limits,
// except the session's scratch directory.
func TestDenylistFollowsTheConfiguredDirectories(t *testing.T) {
	repo := t.TempDir()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(repo, "xdg-config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(repo, "xdg-data"))
	paths, err := config.DefaultPaths()
	if err != nil {
		t.Fatal(err)
	}
	os.MkdirAll(paths.SessionsDir, 0o700)
	os.WriteFile(filepath.Join(paths.SessionsDir, "other.jsonl"), []byte("log"), 0o600)
	os.WriteFile(filepath.Join(repo, "main.go"), []byte("package main\n"), 0o644)
	s, err := New(Options{Repo: repo, Paths: paths, Config: config.Default(), NoConfirm: true, Provider: llm.NewOllama("http://127.0.0.1:1")})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, src := range []string{`(fs-read "main.go")`, `(fs-write "$TMPDIR/a.py" "x")`} {
		if _, err := s.Eval(t.Context(), src); err != nil {
			t.Fatalf("%s: %v", src, err)
		}
	}
	for _, path := range []string{"xdg-data/nodwyn/sessions/other.jsonl", "xdg-config/nodwyn/bindings.toml"} {
		if _, err := s.Eval(t.Context(), `(fs-read "`+path+`")`); err == nil || !strings.Contains(err.Error(), "protected directory") {
			t.Errorf("fs-read %s: %v", path, err)
		}
	}
}

// $TMPDIR names the scratch directory in every tool, not only in a shell:
// a file tool or a command argument that starts with it gets the directory,
// so a throwaway file never lands in the repository as a directory named
// $TMPDIR, and a write there needs no approval. A turn marks the directory
// in use for the sweep.
func TestTmpdirNamesTheScratchDirectoryInEveryTool(t *testing.T) {
	// The test ages its scratch directory by a week, which another session's
	// start sweeps from the shared temporary directory: use one of its own.
	t.Setenv("TMPDIR", t.TempDir())
	srv := fake.NewT(t, fake.Turn{Content: "ok"})
	defer srv.Close()
	o := diskOptions(t) // NoConfirm: any prompt would be refused
	o.Provider = llm.NewOllama(srv.URL)
	s, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Eval(t.Context(), `(fs-write "$TMPDIR/repro.py" "print(1)\n")`); err != nil {
		t.Fatalf("a write to $TMPDIR was refused: %v", err)
	}
	if b, err := os.ReadFile(filepath.Join(s.scratch, "repro.py")); err != nil || string(b) != "print(1)\n" {
		t.Fatalf("the file is not in the scratch directory: %q %v", b, err)
	}
	for _, stray := range []string{"$TMPDIR", "${TMPDIR}"} {
		if _, err := os.Stat(filepath.Join(o.Repo, stray)); !os.IsNotExist(err) {
			t.Fatalf("%s was created in the repository", stray)
		}
	}
	if v, err := s.Eval(t.Context(), `(fs-read "${TMPDIR}/repro.py")`); err != nil || v.S != "print(1)\n" {
		t.Fatalf("read back %q %v", v.S, err)
	}
	old := time.Now().Add(-8 * 24 * time.Hour)
	os.Chtimes(s.scratch, old, old)
	r := &rig{t: t, s: s, srv: srv}
	if e := r.turn("go"); e != "" {
		t.Fatal(e)
	}
	if info, err := os.Stat(s.scratch); err != nil || time.Since(info.ModTime()) > time.Minute {
		t.Fatalf("a turn did not mark the scratch directory in use: %v %v", info, err)
	}
	s.Close()

	o.Repo, o.NoConfirm, o.AutoApprove = t.TempDir(), false, true
	c, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	os.WriteFile(filepath.Join(c.scratch, "note.txt"), []byte("kept out"), 0o600)
	v, err := c.Eval(t.Context(), `(assoc-ref (shell-exec "cat" '("$TMPDIR/note.txt")) "stdout")`)
	if err != nil || v.S != "kept out" {
		t.Fatalf("a command argument naming $TMPDIR: %q %v", v.S, err)
	}
}

// A repository's notes are kept across sessions, beside nodwyn's data rather
// than in the repository: redacted, capped, and shown in later sessions'
// system prompts as leads to check. Another repository does not see them.
func TestProjectNotesCarryAcrossSessions(t *testing.T) {
	t.Parallel()
	o := diskOptions(t)
	o.Provider = llm.NewOllama("http://127.0.0.1:1")
	s, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(s.loop.WindowContext().System, "Notes kept from earlier sessions") {
		t.Fatal("a repository without notes shows a notes section")
	}
	// Content that looks like a credential is refused by the guard before
	// the primitive's own redaction would matter.
	const canary = "sk-ant-api03-abcdefghijklmnopqrstuvwxyz0123456789"
	if _, err := s.Eval(t.Context(), `(project-notes-save "- key: `+canary+`")`); err == nil {
		t.Fatal("notes holding a credential were saved")
	}
	if _, err := s.Eval(t.Context(), `(project-notes-save "- tests: python -m pytest tests/")`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Eval(t.Context(), `(project-notes-save "`+strings.Repeat("x", 9000)+`")`); err == nil || !strings.Contains(err.Error(), "at most 8192") {
		t.Fatalf("oversized notes: %v", err)
	}
	s.Close()
	file := notesFile(o.Paths.DataDir, o.Repo)
	if strings.HasPrefix(file, o.Repo) {
		t.Fatalf("notes inside the repository: %s", file)
	}
	b, err := os.ReadFile(file)
	if err != nil || strings.Contains(string(b), canary) || !strings.Contains(string(b), "python -m pytest tests/") {
		t.Fatalf("stored notes %q %v", b, err)
	}
	next, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	defer next.Close()
	if sys := next.loop.WindowContext().System; !strings.Contains(sys, "Notes kept from earlier sessions in this repository") || !strings.Contains(sys, "- tests: python -m pytest tests/") {
		t.Fatalf("the next session's prompt lacks the notes:\n%s", sys)
	}
	other := o
	other.Repo = t.TempDir()
	elsewhere, err := New(other)
	if err != nil {
		t.Fatal(err)
	}
	defer elsewhere.Close()
	if strings.Contains(elsewhere.loop.WindowContext().System, "python -m pytest tests/") {
		t.Fatal("another repository sees these notes")
	}
}

// The model keeps what it learned about a repository with
// project-notes-save, a tool it is offered and told when to use: until
// 2026-10-05 only a person could write the notes the prompt calls the
// agent's own, and no model ever did (ADR 32, amended 2026-10-05). The next
// session is shown them, and a save carrying a credential needs a person.
func TestTheModelKeepsProjectNotes(t *testing.T) {
	t.Parallel()
	token := "ghp_" + strings.Repeat("A1b2C3d4", 5)
	srv := fake.NewT(t,
		fake.Turn{ToolCalls: []fake.ToolCall{{ID: "n", Name: "project-notes-save", Args: map[string]any{"text": "Tests: go test ./... from the root."}}}},
		fake.Turn{ToolCalls: []fake.ToolCall{{ID: "k", Name: "project-notes-save", Args: map[string]any{"text": "Tests: go test ./... from the root. The token is " + token + ", from the keychain."}}}},
		fake.Turn{Content: "done"},
	)
	defer srv.Close()
	o := diskOptions(t)
	o.Provider = llm.NewOllama(srv.URL)
	s, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	offered := false
	for _, tool := range s.Tools() {
		offered = offered || tool.Name == "project-notes-save"
	}
	if !offered {
		t.Fatal("project-notes-save is not offered to the model")
	}
	if !strings.Contains(s.loop.WindowContext().System, "keep it with project-notes-save") {
		t.Fatal("the prompt does not say when to keep notes")
	}
	r := &rig{t: t, s: s, srv: srv}
	if e := r.turn("run the tests"); e != "" {
		t.Fatal(e)
	}
	s.Close()
	next, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	defer next.Close()
	// A save that carries a credential is the guard's to ask a person
	// about, and nobody here says yes, so the second save leaves the first.
	if sys := next.loop.WindowContext().System; !strings.Contains(sys, "Tests: go test ./... from the root.") || strings.Contains(sys, "keychain") || strings.Contains(sys, token) {
		t.Fatalf("the next session is not shown the notes, or is shown the key:\n%s", sys)
	}
}
