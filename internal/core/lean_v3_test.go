package core

import (
	"fmt"
	"os"
	"path/filepath"
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

// The lean harness says little (ADR 32, amended 2026-10-17): twelve rounds in a
// row that only read get no reading reminder, a command that fails for a
// missing module gets no environment hint, and a turn that changed nothing is
// told so once, generically. With the switches off, as full.scm has them, each
// comes back, the no-change reminder in its old words.
func TestTheLeanHarnessSaysLittle(t *testing.T) {
	useProfile(t, config.ProfileLean)
	const missing = `echo "ModuleNotFoundError: No module named 'yaml'" >&2; exit 1`
	for _, init := range []string{"", "(define lean-no-read-reminder #f)\n(define lean-no-envhint #f)\n(define lean-quiet #f)\n(define lean-empty-reminders 1)"} {
		turns := []fake.Turn{shell("mod", missing)}
		for i := 0; i < 12; i++ {
			turns = append(turns, shell(fmt.Sprint("r", i), fmt.Sprintf("sed -n '%d,%dp' main.go", i+1, i+2)))
		}
		for i := 0; i < 6; i++ {
			turns = append(turns, fake.Turn{Content: "done"})
		}
		r := profileRig(t, unattendedOptions, init, turns...)
		_ = r.turn("fix it")
		var all []string
		for i := range r.srv.Requests {
			all = append(all, lastMessage(r, i))
		}
		said := strings.Join(all, "\n---\n")
		lean := init == ""
		if strings.Contains(said, "You have only read") == lean {
			t.Errorf("lean %v: the reading reminder: %v", lean, !lean)
		}
		if strings.Contains(said, "[hint: if this interpreter lacks the project's dependencies") == lean {
			t.Errorf("lean %v: the environment hint: %v", lean, !lean)
		}
		const generic = "No file has changed. If the request needs a change, make it; if it does not, say what you ran that shows the code already does what is asked."
		if lean && strings.Count(said, generic) != 1 {
			t.Errorf("lean: the no-change reminder %d times in\n%s", strings.Count(said, generic), said)
		}
		if !lean && (strings.Contains(said, generic) || !strings.Contains(said, "No file has changed yet.")) {
			t.Errorf("switches off: the no-change reminder in its new words, or none")
		}
	}
}

// A lean session nobody approves writes and reads /tmp as its scratch
// directory, and a path outside the repository, $TMPDIR and /tmp is refused
// with a message that says nothing ran; a full session nobody approves is
// refused /tmp as before (ADR 32, amended 2026-10-17).
func TestALeanUnattendedSessionUsesTmp(t *testing.T) {
	name := "nodwyn-lean-tmp-" + filepath.Base(t.TempDir())
	t.Cleanup(func() { os.Remove(filepath.Join("/tmp", name)) })
	for _, profile := range []string{config.ProfileLean, config.ProfileFull} {
		useProfile(t, profile)
		os.Remove(filepath.Join("/tmp", name))
		r := profileRig(t, unattendedOptions, "",
			shell("tmp", "echo kept > /tmp/"+name+" && cat /tmp/"+name),
			shell("etc", "cat /etc/hosts"),
			fake.Turn{Content: "done"}, fake.Turn{Content: "done"}, fake.Turn{Content: "done"}, fake.Turn{Content: "done"},
		)
		_ = r.turn("try it")
		tmp, etc := lastMessage(r, 1), lastMessage(r, 2)
		if profile == config.ProfileLean {
			if !strings.Contains(tmp, "kept") {
				t.Errorf("lean: the /tmp file was not written and read: %q", tmp)
			}
			if !strings.Contains(etc, "outside the repository, $TMPDIR and /tmp, so it was refused and nothing of it ran") {
				t.Errorf("lean: the refusal of /etc/hosts: %q", etc)
			}
			continue
		}
		if strings.Contains(tmp, "kept") || !strings.Contains(tmp, "outside the repository") {
			t.Errorf("full: /tmp was usable: %q", tmp)
		}
	}
}
