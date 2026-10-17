package core

import (
	"strings"
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

// A lean run's requests carry no plan: no header, no plan-step, no plan-write,
// though the one-line plan is recorded for the core and the person, hidden.
// With lean-plan-hidden off (as full.scm has it) the same run shows it, as it
// did (ADR 32, amended 2026-10-17).
func TestALeanRunShowsTheModelNoPlan(t *testing.T) {
	useProfile(t, config.ProfileLean)
	for _, init := range []string{"", "(define lean-plan-hidden #f)"} {
		turns := []fake.Turn{shell("look", "ls"), {Content: "done"}, {Content: "done"}, {Content: "done"}}
		r := profileRig(t, func(o *Options) { o.AutoApprove = true; o.Unattended = true }, init, turns...)
		if err := r.s.SendRunTurn("change main.go", "TASK COMPLETE", 1, 1); err != nil {
			t.Fatal(err)
		}
		_ = r.failure()
		if len(r.srv.Requests) == 0 {
			t.Fatalf("%q: no request", init)
		}
		if !strings.Contains(r.logb.String(), `"kind":"gate.plan"`) {
			t.Errorf("%q: no plan was recorded", init)
		}
		if init != "" {
			if raw := string(r.srv.Requests[0].Raw); !strings.Contains(raw, "Plan for the current task") || !strings.Contains(raw, "plan's next unmarked step") {
				t.Errorf("with lean-plan-hidden off the run's first request shows no plan")
			}
			continue
		}
		for i := range r.srv.Requests {
			raw := string(r.srv.Requests[i].Raw)
			for _, word := range []string{"Plan for the current task", "plan-step", "plan-write", "plan's next unmarked step"} {
				if strings.Contains(raw, word) {
					t.Errorf("lean request %d carries %q", i, word)
				}
			}
		}
		if !strings.Contains(r.logb.String(), `"hidden":true`) {
			t.Errorf("the one-line plan was not recorded hidden")
		}
	}
}
