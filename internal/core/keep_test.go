package core

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fclaude/nodwyn/internal/budget"
	"github.com/fclaude/nodwyn/internal/config"
	"github.com/fclaude/nodwyn/internal/llm"
	"github.com/fclaude/nodwyn/internal/loop"
	fake "github.com/fclaude/nodwyn/test/fakes/ollama"
)

// keepRig is a --yolo session in a git work tree whose turns stop at the
// fourth tool call.
func keepRig(t *testing.T, turns ...fake.Turn) *rig {
	t.Helper()
	repo := gitWorkTree(t)
	srv := fake.NewT(t, turns...)
	t.Cleanup(srv.Close)
	cfg := config.Default()
	logb := &bytes.Buffer{}
	s, err := New(Options{Config: cfg, Repo: repo, Provider: llm.NewOllama(srv.URL), AutoApprove: true, LogWriter: logb, Limits: &LimitsChoice{Limits: budget.Task{ToolCalls: 4}}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return &rig{t: t, s: s, srv: srv, repo: repo, logb: logb}
}

func contentOf(t *testing.T, repo, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(repo, name))
	if err != nil {
		return "<" + err.Error() + ">"
	}
	return string(b)
}

// An unattended turn that a hard limit stops one edit past the state its
// tests passed on is left in that state, and told so: the edits since are
// saved, not lost (ADR 18, amended 2026-10-06).
func TestHardStopPutsBackTheLastPassingState(t *testing.T) {
	t.Parallel()
	r := keepRig(t,
		write("w1", "fix.txt", "good\n"),    // a change
		shell("t", "true # go test ./..."),  // a test run that passes: kept
		write("w2", "fix.txt", "broken\n"),  // edits that no test has passed
		write("w3", "late.txt", "late\n"),   // the fourth call, the last the budget allows
		write("w4", "never.txt", "never\n"), // the fifth: stopped
	)
	e := r.turn("fix it")
	if !strings.Contains(e, "hard stop") {
		t.Fatalf("the turn ended with %q, want a hard stop", e)
	}
	if got := contentOf(t, r.repo, "fix.txt"); got != "good\n" {
		t.Errorf("fix.txt is %q, want the state the test passed on", got)
	}
	for _, name := range []string{"late.txt", "never.txt"} {
		if _, err := os.Stat(filepath.Join(r.repo, name)); err == nil {
			t.Errorf("%s is still there", name)
		}
	}
	if got := contentOf(t, r.repo, "notes.txt"); got != "mine\n" {
		t.Errorf("notes.txt, which was there before the turn, is %q", got)
	}
	log := r.logb.String()
	if !strings.Contains(log, "put the files back to the state a test run last passed on: 1 written, 1 removed") {
		t.Fatalf("the log does not say the files were put back:\n%s", log)
	}
	// The turn's end tells the person too, where it stopped.
	if !strings.Contains(log, `"where":`) || !strings.Contains(log, "last passed on") {
		t.Fatalf("turn.end does not say it:\n%s", log)
	}
}

// A turn that has changed nothing keeps nothing: the state its tests passed
// on before any edit is no result to go back to.
func TestAHardStopBeforeAnyEditPutsNothingBack(t *testing.T) {
	t.Parallel()
	r := keepRig(t,
		shell("t", "true # go test ./..."), // passes on the unchanged code: nothing to keep
		write("w1", "a.txt", "a\n"),
		write("w2", "b.txt", "b\n"),
		write("w3", "c.txt", "c\n"),
		write("w4", "d.txt", "d\n"), // the fifth call: stopped
	)
	if e := r.turn("add files"); !strings.Contains(e, "hard stop") {
		t.Fatalf("the turn ended with %q, want a hard stop", e)
	}
	for _, name := range []string{"a.txt", "b.txt", "c.txt"} {
		if _, err := os.Stat(filepath.Join(r.repo, name)); err != nil {
			t.Errorf("%s was taken away: the turn had kept nothing", name)
		}
	}
	if strings.Contains(r.logb.String(), "put the files back") {
		t.Fatalf("the log says files were put back:\n%s", r.logb.String())
	}
}

// Only a stop that ends a turn nobody reads puts anything back: a hard
// limit or the progress guard, in a --yolo, judged or unattended session,
// never a person's own stop, a soft limit or an ordinary error.
func TestRestoreKeptOnlyForAHardStopOfAnUnattendedTurn(t *testing.T) {
	t.Parallel()
	hard := &budget.Stop{Hard: true, Reason: "hard stop: tool call budget reached"}
	for _, c := range []struct {
		name       string
		err        error
		unattended bool
		restored   bool
	}{
		{"a hard limit", hard, true, true},
		{"a hard limit, wrapped", errors.Join(errors.New("turn failed"), hard), true, true},
		{"the progress guard", &loop.ProgressStop{Reason: "the same call four times"}, true, true},
		{"a hard limit in a chat", hard, false, false},
		{"a soft stop", &budget.Stop{Reason: "stopped at a checkpoint"}, true, false},
		{"a person's stop", context.Canceled, true, false},
		{"an ordinary error", errors.New("model request failed"), true, false},
		{"no error", nil, true, false},
	} {
		r := keepRig(t)
		if err := os.WriteFile(filepath.Join(r.repo, "tracked.txt"), []byte("one\ntwo\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := r.s.keeper.Keep(context.Background()); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(r.repo, "tracked.txt"), []byte("one\nbroken\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		r.s.opts.AutoApprove = c.unattended
		said := r.s.restoreKept(c.err)
		got := contentOf(t, r.repo, "tracked.txt")
		if c.restored != (got == "one\ntwo\n") || c.restored != (said != "") {
			t.Errorf("%s: tracked.txt is %q, said %q; restored should be %v", c.name, got, said, c.restored)
		}
	}
}
