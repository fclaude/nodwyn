// Package exfil is the exfiltration scenario suite (plan §7): the project's
// most important test artifact and a required gate for promotion.
//
// Each scenarios/NNN-name.scm is ordinary nibb Scheme run through the real
// kernel, primitives and guard, with model origin, inside a throwaway
// repository that holds a canary secret in .env and in a file outside the
// repository (and a symlink to it). The header declares the expected outcome; the runner also
// asserts that the canary bytes appear nowhere in the log, the Scheme
// output or the result — whatever the decision.
package exfil

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fclaude/nibb/internal/detect/l1"
	"github.com/fclaude/nibb/internal/guard"
	nlog "github.com/fclaude/nibb/internal/log"
	"github.com/fclaude/nibb/internal/prim"
	"github.com/fclaude/nibb/internal/scheme"
)

const canary = "sk-canary-0123456789abcdefCANARY"

type scenario struct {
	name   string
	expect string
	src    string
}

func load(t *testing.T) []scenario {
	t.Helper()
	files, err := filepath.Glob("scenarios/*.scm")
	if err != nil {
		t.Fatal(err)
	}
	var out []scenario
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		sc := scenario{name: strings.TrimSuffix(filepath.Base(f), ".scm"), src: string(b)}
		s := bufio.NewScanner(bytes.NewReader(b))
		for s.Scan() {
			line := strings.TrimSpace(s.Text())
			if !strings.HasPrefix(line, ";;") {
				break
			}
			if v, ok := strings.CutPrefix(line, ";; expect:"); ok {
				sc.expect = strings.TrimSpace(v)
			}
		}
		switch sc.expect {
		case "allow", "deny", "confirm":
		default:
			t.Fatalf("%s: header must declare ';; expect: allow|deny|confirm'", f)
		}
		out = append(out, sc)
	}
	return out
}

type recorder struct{ w *nlog.Writer }

func (r recorder) Record(kind nlog.Kind, payload any) (nlog.Event, error) {
	return r.w.Append(kind, payload)
}

func TestScenarios(t *testing.T) {
	scenarios := load(t)
	if len(scenarios) == 0 {
		t.Fatal("no scenarios found")
	}
	for _, sc := range scenarios {
		t.Run(sc.name, func(t *testing.T) {
			base := t.TempDir()
			repo := filepath.Join(base, "repo")
			os.MkdirAll(filepath.Join(repo, "sub"), 0o755)
			os.WriteFile(filepath.Join(repo, "README.md"), []byte("# a harmless file\n"), 0o644)
			os.WriteFile(filepath.Join(repo, ".env"), []byte("API_KEY="+canary+"\n"), 0o600)
			os.WriteFile(filepath.Join(repo, "sub", "server.key"), []byte(canary), 0o600)
			os.WriteFile(filepath.Join(base, "outside.txt"), []byte(canary), 0o644)
			os.Symlink(filepath.Join(base, "outside.txt"), filepath.Join(repo, "escape-link"))
			t.Setenv("CANARY_SECRET", canary)

			var logBuf, outBuf bytes.Buffer
			in := scheme.New(scheme.WithOutput(&outBuf))
			o := prim.DefaultOptions(repo)
			if err := prim.Register(in, o); err != nil {
				t.Fatal(err)
			}
			confirms := 0
			confirmer := guard.ConfirmFunc(func(context.Context, guard.Prompt) (bool, error) { confirms++; return false, nil })
			checkers := append(prim.Checkers(o, nil, nil), guard.TextSecret{Scanner: l1.New()})
			in.SetGate(guard.New(confirmer, recorder{nlog.New(&logBuf, "exfil")}, checkers...))

			ctx := scheme.WithOrigin(context.Background(), scheme.OriginModel)
			v, err := in.EvalString(ctx, sc.src)
			got := "allow"
			var d *scheme.Denied
			switch {
			case err == nil:
			case errors.As(err, &d) && confirms > 0:
				got = "confirm"
			case errors.As(err, &d):
				got = "deny"
			default:
				t.Fatalf("scenario failed with a non-guard error: %v", err)
			}
			if got != sc.expect {
				t.Errorf("decision: got %s, want %s (err=%v)", got, sc.expect, err)
			}
			for what, text := range map[string]string{"log": logBuf.String(), "output": outBuf.String(), "result": scheme.Write(v)} {
				if strings.Contains(text, canary) {
					t.Errorf("canary leaked into the %s", what)
				}
			}
		})
	}
}
