package core

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fclaude/nodwyn/internal/config"
	"github.com/fclaude/nodwyn/internal/log"
	"github.com/fclaude/nodwyn/test/fakes/must"
	fake "github.com/fclaude/nodwyn/test/fakes/ollama"
)

func sandboxOptions(o *Options) { o.Sandbox = true }

// A session in sandbox mode (ADR 55), as a Terminal-Bench task container
// has one: a working directory that is no git repository, a file outside it
// the task asks to change, and a run nobody watches. Sandbox mode alone
// makes the session unattended and auto-approved, so it runs the lean loop,
// whose prompt says the machine is a disposable sandbox; fs-edit changes the
// file outside the folder, which the same session without sandbox mode is
// refused.
func TestASandboxSessionChangesFilesOutsideItsFolder(t *testing.T) {
	useProfile(t, "")
	machine := t.TempDir()
	conf := filepath.Join(machine, "etc", "app.conf")
	must.MkdirAll(t, filepath.Dir(conf), 0o755)
	edit := fake.Turn{ToolCalls: []fake.ToolCall{{ID: "e1", Name: "fs-edit", Args: map[string]any{"path": conf, "old": "port=80", "new": "port=8080"}}}}

	must.WriteFile(t, conf, []byte("port=80\n"), 0o644)
	// The last answer is to the sandbox finish check.
	r := profileRigIn(t, emptyFolder(t, machine, "app"), sandboxOptions, "", edit, fake.Turn{Content: "done"}, fake.Turn{Content: "done"})
	if r.s.profile != config.ProfileLean || !r.s.opts.AutoApprove || !r.s.opts.Unattended {
		t.Fatalf("profile %q, auto-approve %v, unattended %v: want lean, both", r.s.profile, r.s.opts.AutoApprove, r.s.opts.Unattended)
	}
	if got := r.eval("(list (sandbox?) (unattended?))"); got != "(#t #t)" {
		t.Errorf("(sandbox?) and (unattended?) = %s", got)
	}
	sys := r.s.loop.WindowContext().System
	if want := "You are a coding agent working in " + r.repo + ", unattended: nobody approves or answers your calls. This machine is a disposable sandbox: anything on it is yours to read, change, install or run.\n"; !strings.HasPrefix(sys, want) || !strings.Contains(sys, " Relative paths are relative to "+r.repo+"; throwaway files go in $TMPDIR or /tmp.\n") {
		t.Errorf("system prompt:\n%s", sys)
	}
	if e := r.turn("serve on port 8080"); e != "" {
		t.Fatal(e)
	}
	if b, _ := os.ReadFile(conf); string(b) != "port=8080\n" {
		t.Errorf("in sandbox mode %s is %q", conf, b)
	}

	must.WriteFile(t, conf, []byte("port=80\n"), 0o644)
	plain := profileRigIn(t, emptyFolder(t, machine, "plain"), unattendedOptions, "", edit, fake.Turn{Content: "done"}, fake.Turn{Content: "done"})
	if got := plain.eval("(sandbox?)"); got != "#f" {
		t.Errorf("(sandbox?) = %s without sandbox mode", got)
	}
	if sys := plain.s.loop.WindowContext().System; strings.Contains(sys, "sandbox") || !strings.HasPrefix(sys, leanPromptStart+plain.repo+", unattended: nobody approves or answers your calls.\n") {
		t.Errorf("without sandbox mode the system prompt is:\n%s", sys)
	}
	if e := plain.turn("serve on port 8080"); e != "" {
		t.Fatal(e)
	}
	if b, _ := os.ReadFile(conf); string(b) != "port=80\n" {
		t.Errorf("without sandbox mode %s became %q", conf, b)
	}
	if res := toolResults(plain); len(res) != 1 || !strings.Contains(res[0], "outside the repository") {
		t.Errorf("without sandbox mode the edit's result was %q", res)
	}
}

// The default loop tells a turn that changed nothing so, once; in a folder
// that is no git repository a change made through the shell is invisible to
// it, so a lean turn that wrote its answer with a command is told "No file
// has changed". In sandbox mode the whole machine is the workspace and no
// change can be told from no change, so nothing of the kind is said.
func TestASandboxTurnIsNotToldThatNoFileChanged(t *testing.T) {
	useProfile(t, "")
	machine := t.TempDir()
	write := fake.Turn{ToolCalls: []fake.ToolCall{{ID: "w1", Name: "shell-exec", Args: map[string]any{"cmd": "bash", "args": []any{"-c", "echo '\\d{4}' > regex.txt"}}}}}

	r := profileRigIn(t, emptyFolder(t, machine, "app"), sandboxOptions, "", write, fake.Turn{Content: "done"}, fake.Turn{Content: "done"})
	if e := r.turn("save the regex in regex.txt"); e != "" {
		t.Fatal(e)
	}
	if len(r.srv.Requests) != 3 || !strings.HasPrefix(lastMessage(r, 2), "Before you finish, check the result") {
		t.Errorf("%d requests in sandbox mode, want 3: the call, the answer and the answer to the finish check", len(r.srv.Requests))
	}
	for i := range r.srv.Requests {
		if strings.Contains(lastMessage(r, i), "No file has changed") {
			t.Errorf("request %d in sandbox mode says no file changed", i)
		}
	}
	if b, err := os.ReadFile(filepath.Join(r.repo, "regex.txt")); err != nil || string(b) != "\\d{4}\n" {
		t.Errorf("regex.txt: %q %v", b, err)
	}

	plain := profileRigIn(t, emptyFolder(t, machine, "plain"), unattendedOptions, "", write, fake.Turn{Content: "done"}, fake.Turn{Content: "done"})
	if e := plain.turn("save the regex in regex.txt"); e != "" {
		t.Fatal(e)
	}
	if len(plain.srv.Requests) != 3 || !strings.Contains(lastMessage(plain, 2), "No file has changed") {
		t.Errorf("without sandbox mode: %d requests, the last ending %q", len(plain.srv.Requests), lastMessage(plain, len(plain.srv.Requests)-1))
	}
}

// Sandbox mode answers ordinary confirms itself, so it excludes the judge
// and a session that declines every confirm.
func TestSandboxModeExcludesTheJudgeAndNoConfirm(t *testing.T) {
	for _, adjust := range []func(*Options){
		func(o *Options) { o.Sandbox, o.JudgeApprove = true, true },
		func(o *Options) { o.Sandbox, o.NoConfirm = true, true },
	} {
		o := Options{Config: config.Default(), Repo: t.TempDir()}
		adjust(&o)
		if _, err := resolveOptions(o); err == nil || !strings.Contains(err.Error(), "sandbox mode") {
			t.Errorf("%+v: %v", o, err)
		}
	}
}

// emptyFolder is an empty folder named name in machine, its links resolved.
func emptyFolder(t *testing.T, machine, name string) string {
	t.Helper()
	dir := filepath.Join(machine, name)
	must.MkdirAll(t, dir, 0o755)
	real, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	return real
}

// toolResults are the texts of the session's tool results, in order.
func toolResults(r *rig) []string {
	var out []string
	for _, ev := range r.s.EventsSoFar() {
		if ev.Kind == "tool.result" {
			out = append(out, string(ev.Payload))
		}
	}
	return out
}

// A session's scratch directory is removed when it closes, but in sandbox
// mode it stays: what the session's commands started in the background has
// TMPDIR set to it and goes on after the session.
func TestASandboxSessionLeavesItsScratchDirectory(t *testing.T) {
	useProfile(t, "")
	for _, sandbox := range []bool{true, false} {
		r := profileRig(t, func(o *Options) { o.Sandbox, o.AutoApprove = sandbox, true }, "")
		scratch := r.s.scratch
		t.Cleanup(func() { os.RemoveAll(scratch) })
		must.WriteFile(t, filepath.Join(scratch, "server.tmp"), []byte("x"), 0o600)
		if err := r.s.Close(); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(filepath.Join(scratch, "server.tmp")); (err == nil) != sandbox {
			t.Errorf("sandbox %v: after the session closed, the scratch directory's file: %v", sandbox, err)
		}
	}
}

// sandboxCheck begins sandbox-finish-message (prompts.scm).
const sandboxCheck = "Before you finish, check the result as someone who did not watch you work would"

// A turn of a sandbox run that ends on the model's word is asked once to
// look at its result as someone who did not watch it work would, and may go
// on working (ADR 55, amended 2026-10-19): twice a sandbox run passed a
// task's checks with a result shaped to them and did not say so. It is asked
// in the lean profile, which a sandbox run has unless told otherwise, and in
// the full one; once in the session, so a later turn is not; with a note in
// the log for each time. A turn that a budget stops is not asked, and outside
// sandbox mode no turn is.
func TestASandboxRunIsAskedOnceToCheckItsResult(t *testing.T) {
	write := func(id string) fake.Turn {
		return fake.Turn{ToolCalls: []fake.ToolCall{{ID: id, Name: "shell-exec", Args: map[string]any{"cmd": "bash", "args": []any{"-c", "echo '\\d{4}' > regex.txt"}}}}}
	}
	for _, profile := range []string{"", config.ProfileFull} {
		useProfile(t, profile)
		r := profileRigIn(t, emptyFolder(t, t.TempDir(), "app"), sandboxOptions, "",
			write("w1"), fake.Turn{Content: "done"}, fake.Turn{Content: "checked: the pattern is general"}, fake.Turn{Content: "nothing else"})
		if want := map[string]string{"": config.ProfileLean, config.ProfileFull: config.ProfileFull}[profile]; r.s.profile != want {
			t.Fatalf("profile %q, want %q", r.s.profile, want)
		}
		if e := r.turn("save the regex in regex.txt"); e != "" {
			t.Fatal(e)
		}
		if len(r.srv.Requests) != 3 || !strings.HasPrefix(lastMessage(r, 2), sandboxCheck) {
			t.Fatalf("%s profile: %d requests, the last ending %q", r.s.profile, len(r.srv.Requests), lastMessage(r, len(r.srv.Requests)-1))
		}
		if e := r.turn("anything else?"); e != "" {
			t.Fatal(e)
		}
		if len(r.srv.Requests) != 4 {
			t.Errorf("%s profile: the second turn made %d requests, want 1", r.s.profile, len(r.srv.Requests)-3)
		}
		if n := notesStarting(r, "sandbox finish check: "); n != 1 {
			t.Errorf("%s profile: %d notes of the check, want 1: %q", r.s.profile, n, r.notes())
		}
	}

	useProfile(t, "")
	limited := profileRigIn(t, emptyFolder(t, t.TempDir(), "limited"), func(o *Options) { o.Sandbox, o.Config.Loop.MaxToolCalls = true, 1 }, "",
		write("w1"), write("w2"))
	if e := limited.turn("save the regex in regex.txt"); e == "" {
		t.Fatal("the turn was not stopped at its tool-call limit")
	}
	if len(limited.srv.Requests) != 2 || notesStarting(limited, "sandbox finish check") != 0 {
		t.Errorf("a turn the budget stopped: %d requests, notes %q", len(limited.srv.Requests), limited.notes())
	}

	// Outside sandbox mode the lean loop tells this turn that no file changed
	// (TestASandboxTurnIsNotToldThatNoFileChanged), and nothing more.
	plain := profileRigIn(t, emptyFolder(t, t.TempDir(), "plain"), unattendedOptions, "", write("w1"), fake.Turn{Content: "done"}, fake.Turn{Content: "done"})
	if e := plain.turn("save the regex in regex.txt"); e != "" {
		t.Fatal(e)
	}
	for i := range plain.srv.Requests {
		if strings.Contains(lastMessage(plain, i), sandboxCheck) {
			t.Errorf("request %d without sandbox mode carries the finish check", i)
		}
	}
	if len(plain.srv.Requests) != 3 || notesStarting(plain, "sandbox finish check") != 0 {
		t.Errorf("without sandbox mode: %d requests, notes %q", len(plain.srv.Requests), plain.notes())
	}
}

// In sandbox mode the same call four times in a row warns the model and the
// turn goes on (ADR 55, amended 2026-10-19): a sandbox run waiting on a long
// training job polled its log with one command line, and the progress guard
// ended the run before the training finished. The fourth result and the
// eighth say so after the command's output, as the model reads them, and the
// log keeps a note of each warning. Outside sandbox mode the fourth still
// ends the turn, with no warning.
func TestASandboxRunIsWarnedOfARepeatedCallAndGoesOn(t *testing.T) {
	useProfile(t, "")
	poll := func(id string) fake.Turn {
		return fake.Turn{ToolCalls: []fake.ToolCall{{ID: id, Name: "shell-exec", Args: map[string]any{"cmd": "echo", "args": []any{"epoch 3 of 9"}}}}}
	}
	var turns []fake.Turn
	for i := 1; i <= 9; i++ {
		turns = append(turns, poll(fmt.Sprint(i)))
	}
	turns = append(turns, fake.Turn{Content: "the training finished"}, fake.Turn{Content: "checked"})
	r := profileRigIn(t, emptyFolder(t, t.TempDir(), "app"), sandboxOptions, "", turns...)
	if e := r.turn("wait for the training to finish"); e != "" {
		t.Fatalf("the turn ended with %q", e)
	}
	results := resultsByID(r)
	for i := 1; i <= 9; i++ {
		res := results[fmt.Sprint(i)]
		note := fmt.Sprintf("\n[this exact call has now run %d times in a row; if you are waiting on something, that is fine; otherwise look at what the results show before running it again]", i)
		if warned := i%4 == 0; !res.OK || !strings.Contains(res.Value, "epoch 3 of 9") || strings.HasSuffix(res.Value, note) != warned || (!warned && strings.Contains(res.Value, "times in a row")) {
			t.Errorf("result %d: %+v", i, res)
		}
	}
	if !strings.HasSuffix(lastMessage(r, 4), "run 4 times in a row; if you are waiting on something, that is fine; otherwise look at what the results show before running it again]") {
		t.Errorf("the model read the fourth result as %q", lastMessage(r, 4))
	}
	if n := notesStarting(r, "progress warning: "); n != 2 || notesStarting(r, "progress stop") != 0 {
		t.Errorf("%d warnings in the log, want 2: %q", n, r.notes())
	}

	plain := profileRigIn(t, emptyFolder(t, t.TempDir(), "plain"), unattendedOptions, "", poll("1"), poll("2"), poll("3"), poll("4"))
	if e := plain.turn("wait for the training to finish"); !strings.Contains(e, "the same tool and arguments ran four times in a row") {
		t.Fatalf("without sandbox mode the turn ended with %q", e)
	}
	if res := resultsByID(plain)["4"]; strings.Contains(res.Value, "times in a row") || notesStarting(plain, "progress warning") != 0 {
		t.Errorf("without sandbox mode: the fourth result %+v, notes %q", res, plain.notes())
	}
}

// resultsByID is the session's tool results, by the id of the call each answers.
func resultsByID(r *rig) map[string]log.ToolResult {
	r.t.Helper()
	out := map[string]log.ToolResult{}
	for _, ev := range r.s.EventsSoFar() {
		if ev.Kind != log.KindToolResult {
			continue
		}
		var res log.ToolResult
		if err := ev.Decode(&res); err != nil {
			r.t.Fatal(err)
		}
		out[res.ID] = res
	}
	return out
}

// notesStarting counts the notes in the session's log that begin with prefix.
func notesStarting(r *rig, prefix string) int {
	n := 0
	for _, text := range r.notes() {
		if strings.HasPrefix(text, prefix) {
			n++
		}
	}
	return n
}
