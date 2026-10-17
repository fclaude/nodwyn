package core

import (
	"testing"

	"github.com/fclaude/nodwyn/internal/config"
	fake "github.com/fclaude/nodwyn/test/fakes/ollama"
)

// The lean prompt says who and where, the tools in one phrase each, where
// throwaway files go, one sentence on the work and one on the answer, and
// nothing else: none of the behavioural sentences had earned its place in the
// measurements (ADR 32, amended 2026-10-17).
func TestTheLeanPromptIsShortAndGeneric(t *testing.T) {
	useProfile(t, config.ProfileLean)
	r := profileRig(t, unattendedOptions, "", fake.Turn{Content: "done"})
	want := "You are a coding agent working in the repository at " + r.repo + ", unattended: nobody approves or answers your calls.\n" +
		"Your tools: shell-exec runs a program (cmd and args, no shell; use bash with args [\"-c\", \"...\"] for pipes, redirects or several commands); fs-edit replaces exact text in a file. Paths are relative to the repository root; throwaway files go in $TMPDIR or /tmp.\n" +
		"Do what the request asks, then check it by running something that exercises the change.\n" +
		"When you are done, say what you changed, what you ran and what it showed, and anything you could not check.\n"
	if got := r.s.loop.WindowContext().System; got != want {
		t.Fatalf("the lean prompt:\n%s\nwant:\n%s", got, want)
	}
}
