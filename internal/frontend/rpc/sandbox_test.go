package rpc

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fclaude/nodwyn/internal/config"
	"github.com/fclaude/nodwyn/internal/frontend/run"
	"github.com/fclaude/nodwyn/internal/llm"
	"github.com/fclaude/nodwyn/test/fakes/must"
	fake "github.com/fclaude/nodwyn/test/fakes/ollama"
)

// run.start with sandbox "container" (ADR 54) starts a run that is
// unattended and auto-approved without saying so, in a folder a run is
// otherwise refused, the home directory itself, and records that it is in
// sandbox mode; a resume is in sandbox mode only when it says so again.
// Another value, and the judge, are refused.
func TestSandboxRunOverRPC(t *testing.T) {
	t.Setenv(config.ProfileEnv, "") // a run nobody watches runs the lean loop
	srv := fake.New(fake.Turn{Content: "working on it"}, fake.Turn{Content: "done\n" + run.CompleteLine})
	defer srv.Close()
	o := options(t)
	o.Provider = llm.NewOllama(srv.URL)
	path, stop := start(t, o)
	defer stop()
	c := connect(t, path)
	c.hello()
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	must.MkdirAll(t, filepath.Join(home, ".ssh"), 0o700)

	for _, p := range []runParams{
		{Repo: home, Task: "t", Wall: "1m", MaxTurns: 2, Sandbox: "vm"},
		{Repo: home, Task: "t", Wall: "1m", MaxTurns: 2, Sandbox: "container", Judge: true},
		{Repo: home, Task: "t", Wall: "1m", MaxTurns: 2, AutoApprove: true, Unattended: true},
		{Repo: filepath.Join(home, ".ssh"), Task: "t", Wall: "1m", MaxTurns: 2, Sandbox: "container"},
	} {
		if r := c.call("run.start", p); r.Error == nil || r.Error.Code != -32602 {
			t.Errorf("%+v: %+v", p, r)
		}
	}
	var started runResult
	if err := json.Unmarshal(c.ok("run.start", runParams{Repo: home, Task: "serve the site on port 8000", Wall: "1m", MaxTurns: 3, Sandbox: "container"}), &started); err != nil {
		t.Fatal(err)
	}
	if s := started.State; !s.Sandbox || !s.Unattended || !s.AutoApprove {
		t.Fatalf("started %+v", s)
	}
	stopped := waitRun(t, c, started.ID, "stopped")
	if !stopped.Sandbox || !strings.Contains(stopped.Reason, "marked no plan step") {
		t.Fatalf("stopped %+v", stopped)
	}
	if r := c.call("run.resume", map[string]any{"id": started.ID, "sandbox": "vm"}); r.Error == nil || r.Error.Code != -32602 {
		t.Errorf("a resume with sandbox vm: %+v", r)
	}
	var resumed runResult
	if err := json.Unmarshal(c.ok("run.resume", map[string]any{"id": started.ID, "sandbox": "container"}), &resumed); err != nil {
		t.Fatal(err)
	}
	if s := resumed.State; !s.Sandbox || !s.Unattended || !s.AutoApprove {
		t.Fatalf("resumed %+v", s)
	}
	if done := waitRun(t, c, started.ID, "completed"); !done.Sandbox {
		t.Fatalf("completed %+v", done)
	}
	var listed []sessionInfo
	if err := json.Unmarshal(c.ok("session.list", struct{}{}), &listed); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, s := range listed {
		if s.ID == started.ID {
			found = true
			if s.Run == nil || !s.Run.Sandbox {
				t.Errorf("the listing's run: %+v", s.Run)
			}
		}
	}
	if !found {
		t.Errorf("%s is not listed: %+v", started.ID, listed)
	}
}
