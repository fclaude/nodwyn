package exfil

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fclaude/nodwyn/internal/detect/l1"
	"github.com/fclaude/nodwyn/internal/guard"
	nlog "github.com/fclaude/nodwyn/internal/log"
	"github.com/fclaude/nodwyn/internal/prim"
	"github.com/fclaude/nodwyn/internal/scheme"
)

// ADR 43: an edit through a credential's placeholder keeps the credential in
// the file and leaks it nowhere: not into the log, whose guard decision
// carries the diff, not into the output, the result or an error, and not into
// the text of an edit the model could not make. The credential is a canary
// the model is never shown; a file that holds it is an ordinary file, since
// nothing but its content makes it one a model should not read in full.
func TestEditThroughAPlaceholderLeaksNothing(t *testing.T) {
	repo := t.TempDir()
	file := filepath.Join(repo, "config.txt")
	if err := os.WriteFile(file, []byte("name: demo\ntoken: "+canary+"\nmode: test\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var logBuf, outBuf bytes.Buffer
	in := scheme.New(scheme.WithOutput(&outBuf))
	o := prim.DefaultOptions(repo)
	if err := prim.Register(in, o); err != nil {
		t.Fatal(err)
	}
	yes := guard.ConfirmFunc(func(context.Context, guard.Prompt) (bool, error) { return true, nil })
	checkers := append(prim.Checkers(o, nil, nil), guard.Taint{Policy: "deny"}, guard.TextSecret{Scanner: l1.New()}, guard.FolderWrites{})
	in.SetGate(guard.New(yes, recorder{nlog.New(&logBuf, "exfil")}, checkers...))
	ctx := scheme.WithOrigin(context.Background(), scheme.OriginModel)

	var seen []string
	run := func(src string) error {
		v, err := in.EvalString(ctx, src)
		seen = append(seen, scheme.Write(v))
		if err != nil {
			seen = append(seen, err.Error())
		}
		return err
	}
	// Kept, with text around it, as a model that read the line would write it.
	if err := run(`(fs-edit "config.txt" "token: [REDACTED openai-key]" "token: [REDACTED openai-key] # kept")`); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(file); string(b) != "name: demo\ntoken: "+canary+" # kept\nmode: test\n" {
		t.Fatalf("the credential did not stay in the file: %q", b)
	}
	// The edit answers with the line it changed, as the model reads it
	// (seen holds the written form, its tab escaped).
	if !strings.Contains(seen[0], `2\ttoken: [REDACTED openai-key] # kept`) {
		t.Fatalf("the edit's answer: %s", seen[0])
	}
	// Refused edits: a copy, a half placeholder, and a guess that is not the credential.
	for _, src := range []string{
		`(fs-edit "config.txt" "token: [REDACTED openai-key] # kept" "token: [REDACTED openai-key]\ncopy: [REDACTED openai-key]")`,
		`(fs-edit "config.txt" "openai-key] # kept" "x")`,
		`(fs-edit "config.txt" "token: sk-canary-0000000000000000GUESSED" "x")`,
	} {
		if err := run(src); err == nil {
			t.Fatalf("%s: the edit went ahead", src)
		}
	}
	if b, _ := os.ReadFile(file); string(b) != "name: demo\ntoken: "+canary+" # kept\nmode: test\n" {
		t.Fatalf("a refused edit changed the file: %q", b)
	}
	if !strings.Contains(logBuf.String(), "[REDACTED openai-key]") {
		t.Fatal("the log holds no diff of the edit, so it shows nothing about leaks")
	}
	for what, text := range map[string]string{"log": logBuf.String(), "output": outBuf.String(), "results and errors": strings.Join(seen, "\n")} {
		if strings.Contains(text, canary) {
			t.Errorf("the credential leaked into the %s", what)
		}
	}
}
