package core

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fclaude/nibb/internal/llm"
	fake "github.com/fclaude/nibb/test/fakes/ollama"
)

// A session's scratch directory takes throwaway files without a prompt, is
// the TMPDIR of its commands, is gone when the session closes, and comes
// back, at the same path, when the session resumes.
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
	if dir == "" || strings.HasPrefix(dir, o.Repo) {
		t.Fatalf("scratch directory %q", dir)
	}
	if prompt := s.loop.WindowContext().System; !strings.Contains(prompt, "scratch directory "+dir) {
		t.Fatalf("the system prompt does not name the scratch directory:\n%s", prompt)
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
	if resumed.scratch != dir {
		t.Fatalf("resumed scratch %q, was %q", resumed.scratch, dir)
	}
	v, err := resumed.Eval(t.Context(), `(assoc-ref (shell-exec "sh" '("-c" "printf %s \"$TMPDIR\"")) "stdout")`)
	if err != nil || v.S != dir {
		t.Fatalf("TMPDIR in commands: %q %v", v.S, err)
	}
}
