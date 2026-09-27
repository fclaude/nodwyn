package prim

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fclaude/nibb/internal/guard"
	nlog "github.com/fclaude/nibb/internal/log"
	"github.com/fclaude/nibb/internal/scheme"
)

type harness struct {
	in      *scheme.Interp
	root    string
	logBuf  *bytes.Buffer
	prompts []guard.Prompt
	answer  bool
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	root := t.TempDir()
	root, _ = filepath.EvalSymlinks(root)
	os.WriteFile(filepath.Join(root, "README.md"), []byte("hello\nworld\n"), 0o644)
	os.WriteFile(filepath.Join(root, ".env"), []byte("SECRET=sk-canary-000\n"), 0o600)
	os.MkdirAll(filepath.Join(root, "sub"), 0o755)
	os.WriteFile(filepath.Join(root, "sub", "a.txt"), []byte("a"), 0o644)
	outside := filepath.Join(filepath.Dir(root), "outside-"+filepath.Base(root))
	os.WriteFile(outside, []byte("outside"), 0o644)
	t.Cleanup(func() { os.Remove(outside) })
	os.Symlink(outside, filepath.Join(root, "escape-link"))

	h := &harness{root: root, logBuf: &bytes.Buffer{}}
	h.in = scheme.New()
	o := DefaultOptions(root)
	o.MaxOutput = 4096
	if err := Register(h.in, o); err != nil {
		t.Fatal(err)
	}
	logger := recorder{nlog.New(h.logBuf, "test")}
	confirm := guard.ConfirmFunc(func(_ context.Context, p guard.Prompt) (bool, error) {
		h.prompts = append(h.prompts, p)
		return h.answer, nil
	})
	h.in.SetGate(guard.New(confirm, logger, Checkers(o, nil, nil)...))
	return h
}

type recorder struct{ w *nlog.Writer }

func (r recorder) Record(kind nlog.Kind, payload any) (nlog.Event, error) {
	return r.w.Append(kind, payload)
}

func (h *harness) eval(t *testing.T, src string) (scheme.Value, error) {
	t.Helper()
	return h.in.EvalString(scheme.WithOrigin(context.Background(), scheme.OriginModel), src)
}

func (h *harness) mustEval(t *testing.T, src string) scheme.Value {
	t.Helper()
	v, err := h.eval(t, src)
	if err != nil {
		t.Fatalf("%s: %v", src, err)
	}
	return v
}

func denied(t *testing.T, err error, want string) {
	t.Helper()
	var d *scheme.Denied
	if !errors.As(err, &d) {
		t.Fatalf("want *Denied containing %q, got %v", want, err)
	}
	if !strings.Contains(d.Reason, want) {
		t.Fatalf("denial reason %q does not contain %q", d.Reason, want)
	}
}

func TestReadListInsideRepo(t *testing.T) {
	h := newHarness(t)
	if got := scheme.Write(h.mustEval(t, `(fs-read "README.md")`)); got != `"hello\nworld\n"` {
		t.Fatal(got)
	}
	if got := scheme.Write(h.mustEval(t, `(fs-list ".")`)); got != `(".env" "README.md" "escape-link" "sub/")` {
		t.Fatal(got)
	}
	if got := scheme.Write(h.mustEval(t, `(fs-read "sub/a.txt")`)); got != `"a"` {
		t.Fatal(got)
	}
	if _, err := h.eval(t, `(fs-read "nope.txt")`); err == nil || !strings.Contains(err.Error(), "fs-read:") {
		t.Fatalf("missing file: %v", err)
	}
	if _, err := h.eval(t, `(fs-read "sub")`); err == nil || !strings.Contains(err.Error(), "directory") {
		t.Fatalf("dir read: %v", err)
	}
}

func TestRepoPinDeniesEscapes(t *testing.T) {
	h := newHarness(t)
	for _, src := range []string{
		`(fs-read "../outside-` + filepath.Base(h.root) + `")`,
		`(fs-read "/etc/passwd")`,
		`(fs-list "..")`,
		`(fs-write "../x.txt" "y")`,
		`(fs-read "escape-link")`, // symlink pointing outside
		`(fs-write "sub/../../x.txt" "y")`,
	} {
		_, err := h.eval(t, src)
		if err == nil {
			t.Fatalf("%s: allowed", src)
		}
		denied(t, err, "outside the repository")
	}
	if len(h.prompts) != 0 {
		t.Fatal("deny must not prompt")
	}
}

func TestDenylistBeatsRepoPin(t *testing.T) {
	h := newHarness(t)
	_, err := h.eval(t, `(fs-read ".env")`)
	denied(t, err, "denylist")
	if bytes.Contains(h.logBuf.Bytes(), []byte("sk-canary-000")) {
		t.Fatal("canary reached the log")
	}
	for _, p := range []string{"sub/server.key", "certs/x.pem", "id_ed25519", "sub/.env.local"} {
		_, err := h.eval(t, `(fs-write "`+p+`" "x")`)
		denied(t, err, "denylist")
	}
}

func TestWriteConfirmShowsDiff(t *testing.T) {
	h := newHarness(t)
	h.answer = false
	_, err := h.eval(t, `(fs-write "README.md" "hello\nthere\n")`)
	denied(t, err, "not confirmed")
	if len(h.prompts) != 1 {
		t.Fatalf("prompts: %d", len(h.prompts))
	}
	p := h.prompts[0]
	if p.Prim != "fs-write" || !strings.Contains(p.Detail, "-world\n+there\n") {
		t.Fatalf("prompt: %+v", p)
	}
	if strings.Join(p.Reasons, ";") != "fs-write is irreversible;changes README.md" {
		t.Fatalf("reasons: %v", p.Reasons)
	}
	if b, _ := os.ReadFile(filepath.Join(h.root, "README.md")); string(b) != "hello\nworld\n" {
		t.Fatal("file changed despite denial")
	}

	h.answer = true
	h.mustEval(t, `(fs-write "new/dir/f.txt" "fresh\n")`)
	if b, _ := os.ReadFile(filepath.Join(h.root, "new", "dir", "f.txt")); string(b) != "fresh\n" {
		t.Fatal("write did not land")
	}
	if !strings.Contains(h.prompts[1].Detail, "new file new/dir/f.txt (1 lines") {
		t.Fatalf("new-file prompt: %s", h.prompts[1].Detail)
	}
	logText := h.logBuf.String()
	if !strings.Contains(logText, `"decision":"confirm"`) || !strings.Contains(logText, `"answer":"no"`) || !strings.Contains(logText, `"answer":"yes"`) {
		t.Fatalf("guard decisions not logged:\n%s", logText)
	}
	if !strings.Contains(logText, `"origin":"model"`) {
		t.Fatal("origin not logged")
	}
}

func TestShellExec(t *testing.T) {
	h := newHarness(t)
	h.answer = true
	v := h.mustEval(t, `(shell-exec "sh" '("-c" "echo out; echo err 1>&2; exit 3"))`)
	if got := scheme.Write(v); got != `(("status" . 3) ("stdout" . "out\n") ("stderr" . "err\n"))` {
		t.Fatal(got)
	}
	// cwd is the repo root
	v = h.mustEval(t, `(shell-exec "pwd" '())`)
	if !strings.Contains(scheme.Write(v), h.root) {
		t.Fatalf("cwd: %s", scheme.Write(v))
	}
	// environment is scrubbed
	t.Setenv("NIBB_TEST_SECRET", "sk-canary-env")
	v = h.mustEval(t, `(shell-exec "sh" '("-c" "echo $NIBB_TEST_SECRET; echo $HOME"))`)
	out := scheme.Write(v)
	if strings.Contains(out, "sk-canary-env") {
		t.Fatal("secret env var leaked to child")
	}
	if !strings.Contains(out, os.Getenv("HOME")) {
		t.Fatal("allowlisted HOME missing")
	}
	// output cap
	v = h.mustEval(t, `(shell-exec "sh" '("-c" "yes | head -c 100000"))`)
	if !strings.Contains(scheme.Write(v), "[output truncated at 4 KiB]") {
		t.Fatalf("cap: %s", scheme.Write(v))
	}
	// missing program is an error, not a status
	if _, err := h.eval(t, `(shell-exec "definitely-not-a-program-xyz" '())`); err == nil {
		t.Fatal("missing program")
	}
	// a whole command line as cmd gets told how to split it
	_, err := h.eval(t, `(shell-exec "python3 -m pip" '("--version"))`)
	if err == nil || !strings.Contains(err.Error(), `cmd "python3" args ["-m","pip","--version"]`) {
		t.Fatalf("command line as cmd: %v", err)
	}
	// shell-exec is irreversible: every call was confirmed
	if len(h.prompts) != 6 {
		t.Fatalf("prompts: %d", len(h.prompts))
	}
}

func TestArgvTriggerConfirmsNetworkAndDestructive(t *testing.T) {
	h := newHarness(t)
	h.answer = false
	for _, src := range []string{
		`(shell-exec "curl" '("https://example.com"))`,
		`(shell-exec "git" '("push" "--force"))`,
		`(shell-exec "sh" '("-c" "rm -rf /"))`,
		`(shell-exec "cat" '("/etc/hosts"))`,
	} {
		_, err := h.eval(t, src)
		denied(t, err, "not confirmed")
	}
	want := []string{"network tool curl", "hard-trigger pattern git push", "hard-trigger pattern rm -rf", "outside the repository: /etc/hosts"}
	for i, w := range want {
		joined := strings.Join(h.prompts[i].Reasons, ";")
		if !strings.Contains(joined, w) {
			t.Errorf("prompt %d reasons %q lack %q", i, joined, w)
		}
	}
	h.answer = true
	h.mustEval(t, `(shell-exec "/bin/echo" '("system binaries need no extra prompt"))`)
	if got := strings.Join(h.prompts[4].Reasons, ";"); got != "shell-exec is irreversible" {
		t.Fatalf("system binary: %s", got)
	}
}

func TestShellCancellationKillsProcessTree(t *testing.T) {
	h := newHarness(t)
	h.answer = true
	ctx, cancel := context.WithTimeout(scheme.WithOrigin(context.Background(), scheme.OriginUser), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := h.in.EvalString(ctx, `(shell-exec "sh" '("-c" "sleep 30 & sleep 30"))`)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("want deadline error, got %v", err)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatal("cancellation did not return promptly")
	}
}

func TestToolsExposedToModel(t *testing.T) {
	h := newHarness(t)
	var names []string
	for _, p := range h.in.Tools() {
		names = append(names, p.Name)
	}
	if strings.Join(names, " ") != "fs-edit fs-list fs-read fs-write index-grep index-symbols shell-exec" {
		t.Fatalf("tools: %v", names)
	}
}

func TestEdit(t *testing.T) {
	h := newHarness(t)
	h.answer = true
	h.mustEval(t, `(fs-edit "README.md" "world" "there")`)
	if b, _ := os.ReadFile(filepath.Join(h.root, "README.md")); string(b) != "hello\nthere\n" {
		t.Fatalf("edit: %q", b)
	}
	if !strings.Contains(h.prompts[0].Detail, "-world\n+there\n") {
		t.Fatalf("preview: %s", h.prompts[0].Detail)
	}
	for src, want := range map[string]string{
		`(fs-edit "README.md" "nope" "x")`:   "not found",
		`(fs-edit "README.md" "" "x")`:       "must not be empty",
		`(fs-edit "README.md" "e" "x")`:      "occurs 3 times",
		`(fs-edit "missing.txt" "a" "b")`:    "fs-edit:",
		`(fs-edit "../outside.txt" "a" "b")`: "outside the repository",
	} {
		_, err := h.eval(t, src)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: got %v, want %q", src, err, want)
		}
	}
}
