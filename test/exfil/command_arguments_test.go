package exfil

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fclaude/brote/scheme"
	"github.com/fclaude/nodwyn/internal/guard"
	nlog "github.com/fclaude/nodwyn/internal/log"
	"github.com/fclaude/nodwyn/internal/prim"
)

// These scenarios use a canary with no credential shape, so only the command
// boundary can stop the read. They run real git through the guarded primitive.
func TestStandingGitCannotReadOrWriteOutsideTheRepository(t *testing.T) {
	const privateText = "violet otters count seventeen unlisted islands"
	for _, operation := range []string{"output", "no-index"} {
		t.Run(operation, func(t *testing.T) {
			repo, outside := t.TempDir(), t.TempDir()
			if out, err := exec.Command("git", "-C", repo, "init", "-q").CombinedOutput(); err != nil {
				t.Fatalf("git init: %s: %v", out, err)
			}
			private := filepath.Join(outside, "private.txt")
			if err := os.WriteFile(private, []byte(privateText), 0600); err != nil {
				t.Fatal(err)
			}
			target := filepath.Join(outside, "changed.patch")
			source := fmt.Sprintf(`(shell-exec "git" (list "diff" %q))`, "--output="+target)
			if operation == "no-index" {
				source = fmt.Sprintf(`(shell-exec "git" (list "diff" "--no-index" %q "/dev/null"))`, private)
			}
			var logged, output bytes.Buffer
			in := scheme.New(scheme.WithOutput(&output))
			o := prim.DefaultOptions(repo)
			if err := prim.Register(in, o); err != nil {
				t.Fatal(err)
			}
			var prompts []guard.Prompt
			confirm := guard.ConfirmFunc(func(_ context.Context, p guard.Prompt) (bool, error) {
				prompts = append(prompts, p)
				return false, nil
			})
			in.SetGate(guard.New(confirm, recorder{nlog.New(&logged, "command-arguments")}, prim.Checkers(o, guard.NewCommandAllowIn(repo, "git diff"), nil)...))
			v, err := in.EvalString(scheme.WithOrigin(context.Background(), scheme.OriginModel), source)
			var denied *scheme.Denied
			if !errors.As(err, &denied) || len(prompts) != 1 || !prompts[0].Security || !strings.Contains(strings.Join(prompts[0].Reasons, " "), "path") {
				t.Errorf("expected a security path confirm: error=%v prompts=%+v", err, prompts)
			}
			if _, err := os.Stat(target); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("git wrote outside the repository: %v", err)
			}
			for where, text := range map[string]string{"log": logged.String(), "output": output.String(), "result": scheme.Write(v)} {
				if strings.Contains(text, privateText) {
					t.Errorf("private file reached %s", where)
				}
			}
		})
	}
}
