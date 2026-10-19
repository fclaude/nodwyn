package core

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fclaude/nodwyn/internal/config"
	"github.com/fclaude/nodwyn/test/fakes/must"
	fake "github.com/fclaude/nodwyn/test/fakes/ollama"
)

func sandboxOptions(o *Options) { o.Sandbox = true }

// A session in sandbox mode (ADR 54), as a Terminal-Bench task container
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
	r := profileRigIn(t, emptyFolder(t, machine, "app"), sandboxOptions, "", edit, fake.Turn{Content: "done"})
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

	r := profileRigIn(t, emptyFolder(t, machine, "app"), sandboxOptions, "", write, fake.Turn{Content: "done"})
	if e := r.turn("save the regex in regex.txt"); e != "" {
		t.Fatal(e)
	}
	if len(r.srv.Requests) != 2 {
		t.Errorf("%d requests in sandbox mode, want 2: the call and the answer", len(r.srv.Requests))
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
