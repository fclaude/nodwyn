package core

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fclaude/nibb/internal/llm"
	fake "github.com/fclaude/nibb/test/fakes/ollama"
)

// A session's scratch directory takes throwaway files without a prompt, is
// the TMPDIR of its commands, lives under nibb's data directory, and is gone
// when the session closes; a resumed session gets a new, empty one. The
// system prompt names $TMPDIR, not the per-session path.
func TestScratchDirectory(t *testing.T) {
	srv := fake.New(fake.Turn{Content: "ok"}, fake.Turn{Content: "ok"})
	defer srv.Close()
	o := diskOptions(t) // NoConfirm: any prompt would be refused
	o.Provider = llm.NewOllama(srv.URL)
	os.MkdirAll(filepath.Join(o.Repo, ".nibb"), 0o755)
	os.WriteFile(filepath.Join(o.Repo, ".nibb", "init.scm"), []byte(`
(define seen #f)
(define (agent-turn request)
  (let ((file (string-append (scratch-dir) "/repro.py")))
    (fs-write file "print(1)\n")
    (set! seen (fs-read file))
    (model-turn)))`), 0o644)
	s, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	dir := s.scratch
	data, _ := filepath.EvalSymlinks(o.Paths.DataDir)
	if dir == "" || strings.HasPrefix(dir, o.Repo) || !strings.HasPrefix(dir, filepath.Join(data, "scratch")+string(filepath.Separator)) {
		t.Fatalf("scratch directory %q", dir)
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
// week old; a recent one is kept.
func TestScratchSweep(t *testing.T) {
	data := t.TempDir()
	root := scratchRoot(data)
	os.MkdirAll(filepath.Join(root, "nibb-scratch-old-1"), 0o700)
	os.MkdirAll(filepath.Join(root, "nibb-scratch-new-1"), 0o700)
	os.MkdirAll(filepath.Join(root, "unrelated"), 0o700)
	week := time.Now().Add(-8 * 24 * time.Hour)
	os.Chtimes(filepath.Join(root, "nibb-scratch-old-1"), week, week)
	os.Chtimes(filepath.Join(root, "unrelated"), week, week)
	dir, err := makeScratch(data, "s1")
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]bool{"nibb-scratch-old-1": false, "nibb-scratch-new-1": true, "unrelated": true} {
		if _, err := os.Stat(filepath.Join(root, name)); (err == nil) != want {
			t.Errorf("%s: exists=%v, want %v", name, err == nil, want)
		}
	}
	removeScratch(dir)
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("removeScratch left the directory")
	}
}
