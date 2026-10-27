package rpc

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fclaude/nodwyn/internal/budget"
	"github.com/fclaude/nodwyn/internal/config"
	"github.com/fclaude/nodwyn/internal/event"
	"github.com/fclaude/nodwyn/internal/frontend/run"
	"github.com/fclaude/nodwyn/internal/llm"
	"github.com/fclaude/nodwyn/test/fakes/must"
	fake "github.com/fclaude/nodwyn/test/fakes/ollama"
)

func beginRun(t *testing.T, c *client, paths config.Paths) runResult {
	t.Helper()
	var result runResult
	// These tests pin the run's conversation turn by turn; the default loop's
	// explorer and evidence reminders (docs/scheme.md §9) are off here and
	// tested in internal/core.
	repo := t.TempDir()
	os.MkdirAll(filepath.Join(repo, ".nodwyn"), 0o755)
	os.WriteFile(filepath.Join(repo, ".nodwyn", "init.scm"), []byte("(define plan-explore-rounds 0)\n(define finish-reminders 0)\n"), 0o644)
	trustRunFixture(t, paths, repo)
	if err := json.Unmarshal(c.ok("run.start", runParams{Repo: repo, Task: "test task", Wall: "1m", MaxTurns: 3}), &result); err != nil {
		t.Fatal(err)
	}
	return result
}
func runInfo(t *testing.T, c *client, id string) run.State {
	t.Helper()
	var state run.State
	if err := json.Unmarshal(c.ok("run.info", idParams{id}), &state); err != nil {
		t.Fatal(err)
	}
	return state
}
func waitRun(t *testing.T, c *client, id, status string) run.State {
	t.Helper()
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		state := runInfo(t, c, id)
		if state.Status == status {
			return state
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("run did not reach %s: %+v", status, runInfo(t, c, id))
	return run.State{}
}

// A run's own limits are optional and its task's are the task's (ADR 49,
// amended 2026-10-27): run.start without a deadline or a limit on turns
// starts one with neither, its limits are the task's, which no change
// reaches while the run goes on, and it ends when the model says so.
func TestARunStartsWithoutLimitsOfItsOwn(t *testing.T) {
	srv := fake.New(fake.Turn{Content: "1. first step"}, fake.Turn{Content: "done\n" + run.CompleteLine})
	defer srv.Close()
	o := options(t)
	o.Provider = llm.NewOllama(srv.URL)
	path, _ := start(t, o)
	c := connect(t, path)
	c.hello()
	repo := t.TempDir()
	must.MkdirAll(t, filepath.Join(repo, ".nodwyn"), 0o755)
	must.WriteFile(t, filepath.Join(repo, ".nodwyn", "init.scm"), []byte("(define plan-explore-rounds 0)\n(define finish-reminders 0)\n"), 0o644)
	trustRunFixture(t, o.Paths, repo)
	if r := c.call("run.start", map[string]any{"repo": repo, "task": "t", "wall": "soon", "auto_approve": false}); r.Error == nil {
		t.Fatal("a wall that is not a duration started a run")
	}
	var started runResult
	if err := json.Unmarshal(c.ok("run.start", map[string]any{"repo": repo, "task": "test task", "auto_approve": false, "limits": map[string]any{"cost": 1, "tool_calls": 9}}), &started); err != nil {
		t.Fatal(err)
	}
	if started.State.MaxTurns != 0 || !started.State.Deadline.IsZero() {
		t.Fatalf("a run started without limits of its own: %+v", started.State)
	}
	var info struct {
		Limits budget.Task `json:"limits"`
	}
	into(t, c.ok("session.info", idParams{started.ID}), &info)
	if info.Limits != (budget.Task{Cost: 1, ToolCalls: 9}) {
		t.Fatalf("the run's task's limits: %+v", info.Limits)
	}
	if r := c.call("session.limits.set", map[string]any{"id": started.ID, "limits": map[string]any{}}); r.Error == nil || !strings.Contains(r.Error.Message, "active run") {
		t.Fatalf("a run's limits changed while it went on: %+v", r.Error)
	}
	if done := waitRun(t, c, started.ID, "completed"); done.Turns != 1 {
		t.Fatalf("%+v", done)
	}
}
func TestRunDetachCheckpointAndResume(t *testing.T) {
	// The resumed turn carries the run's plan: the planner is not asked again.
	srv := fake.New(fake.Turn{Content: "1. first step"}, fake.Turn{Content: "step done"}, fake.Turn{Content: "done\n" + run.CompleteLine})
	defer srv.Close()
	o := options(t)
	o.Provider = llm.NewOllama(srv.URL)
	path, stop := start(t, o)
	c := connect(t, path)
	c.hello()
	started := beginRun(t, c, o.Paths)
	c.conn.Close()
	c = connect(t, path)
	c.hello()
	sub := subscribed(t, c, started.ID, 0, 100)
	if !sub.Busy {
		t.Fatal("detached run was not active")
	}
	var confirm event.Confirm
	for _, p := range sub.Pending {
		if p.Kind == event.PromptConfirm {
			data, _ := json.Marshal(p.Payload)
			json.Unmarshal(data, &confirm)
		}
	}
	if confirm.ID == "" {
		confirm = prompt[event.Confirm](t, c, event.PromptConfirm)
	}
	if confirm.Prompt.Prim != "run checkpoint" {
		t.Fatalf("%+v", confirm)
	}
	// Between-turn supervision must still reserve ownership of the session.
	for method, params := range map[string]any{
		"session.send": sendParams{started.ID, "intrude"}, "session.eval": evalParams{started.ID, "(+ 1 2)"},
		"session.friction": idParams{started.ID}, "session.policy.read": policyReadParams{started.ID, "bindings"},
	} {
		if r := c.call(method, params); r.Error == nil {
			t.Fatalf("%s entered active run", method)
		}
	}
	c.ok("session.abort", idParams{started.ID})
	paused := waitRun(t, c, started.ID, "stopped")
	if paused.Turns != 1 {
		t.Fatalf("%+v", paused)
	}
	stop()
	path, _ = start(t, o)
	c = connect(t, path)
	c.hello()
	before := runInfo(t, c, started.ID)
	var resumed runResult
	// The reply is the run as resumed, before it goes on: its limits are the
	// ones from before, and it is already running.
	json.Unmarshal(c.ok("run.resume", idParams{started.ID}), &resumed)
	if resumed.State.Turns != 1 || resumed.State.MaxTurns != 3 || !resumed.State.Deadline.Equal(before.Deadline) {
		t.Fatalf("limits reset: before=%+v after=%+v", before, resumed.State)
	}
	if resumed.State.Status != "running" {
		t.Fatalf("resume reply not running: %+v", resumed.State)
	}
	final := waitRun(t, c, started.ID, "completed")
	if final.Turns != 2 {
		t.Fatalf("%+v", final)
	}
	if r := c.call("run.resume", idParams{started.ID}); r.Error == nil {
		t.Fatal("resumed completed run")
	}
}
func TestRunDaemonShutdownAtCheckpoint(t *testing.T) {
	srv := fake.New(fake.Turn{Content: "1. step"}, fake.Turn{Content: "step done"})
	defer srv.Close()
	o := options(t)
	o.Provider = llm.NewOllama(srv.URL)
	path, stop := start(t, o)
	c := connect(t, path)
	c.hello()
	started := beginRun(t, c, o.Paths)
	sub := subscribed(t, c, started.ID, 0, 100)
	if len(sub.Pending) == 0 {
		_ = prompt[event.Confirm](t, c, event.PromptConfirm)
	}
	stop()
	history, err := readRunHistory(o.Paths.SessionsDir, started.ID)
	if err != nil {
		t.Fatal(err)
	}
	state, err := run.Restore(history)
	if err != nil || state.Status != "stopped" {
		t.Fatalf("shutdown lost stop: %+v %v", state, err)
	}
}

// run.start with unattended runs without asking anyone, and a resume is
// unattended only when it says so again.
func TestUnattendedRunOverRPC(t *testing.T) {
	step := fake.ToolCall{ID: "s", Name: "plan-step", Args: map[string]any{"step": 1, "state": "done", "note": "ok"}}
	srv := fake.New(fake.Turn{Content: "1. first step"}, fake.Turn{ToolCalls: []fake.ToolCall{step}}, fake.Turn{Content: "step done"},
		fake.Turn{Content: "1. second step"}, fake.Turn{Content: "nothing marked"})
	defer srv.Close()
	o := options(t)
	o.Provider = llm.NewOllama(srv.URL)
	path, stop := start(t, o)
	defer stop()
	c := connect(t, path)
	c.hello()
	repo := t.TempDir()
	os.MkdirAll(filepath.Join(repo, ".nodwyn"), 0o755)
	os.WriteFile(filepath.Join(repo, ".nodwyn", "init.scm"), []byte("(define plan-explore-rounds 0)\n(define finish-reminders 0)\n"), 0o644)
	trustRunFixture(t, o.Paths, repo)
	var started runResult
	if err := json.Unmarshal(c.ok("run.start", runParams{Repo: repo, Task: "test task", Wall: "1m", MaxTurns: 10, Unattended: true}), &started); err != nil {
		t.Fatal(err)
	}
	if !started.State.Unattended {
		t.Fatalf("started %+v", started.State)
	}
	stopped := waitRun(t, c, started.ID, "stopped")
	if stopped.Turns != 2 || !stopped.Unattended {
		t.Fatalf("stopped %+v", stopped)
	}
	var resumed runResult
	if err := json.Unmarshal(c.ok("run.resume", map[string]any{"id": started.ID}), &resumed); err != nil {
		t.Fatal(err)
	}
	if resumed.State.Unattended || resumed.State.AutoApprove {
		t.Fatalf("a plain resume came back unattended: %+v", resumed.State)
	}
	// The plain resume may use a turn before the stop lands (the fake has no
	// more replies, so it fails); there are turns enough left either way.
	c.ok("run.stop", idParams{started.ID})
	for runInfo(t, c, started.ID).Status == "running" {
		time.Sleep(10 * time.Millisecond)
	}
	if err := json.Unmarshal(c.ok("run.resume", map[string]any{"id": started.ID, "unattended": true}), &resumed); err != nil {
		t.Fatal(err)
	}
	if !resumed.State.Unattended {
		t.Fatalf("resume asked for unattended: %+v", resumed.State)
	}
}

// A judged run needs a judge, and the judge and auto_approve exclude each
// other.
func TestJudgedRunRefusals(t *testing.T) {
	o := options(t)
	path, stop := start(t, o)
	defer stop()
	c := connect(t, path)
	c.hello()
	repo := t.TempDir()
	r := c.call("run.start", runParams{Repo: repo, Task: "t", Wall: "1m", MaxTurns: 2, Judge: true, AutoApprove: true})
	if r.Error == nil || r.Error.Code != -32602 {
		t.Fatalf("judge with auto_approve: %+v", r)
	}
	r = c.call("run.start", runParams{Repo: repo, Task: "t", Wall: "1m", MaxTurns: 2, Judge: true})
	if r.Error == nil || !strings.Contains(r.Error.Message, "needs a judge") {
		t.Fatalf("judge without one: %+v", r)
	}
}

func trustRunFixture(t *testing.T, paths config.Paths, repo string) {
	t.Helper()
	files, err := config.ReadProjectFiles(repo)
	if err != nil {
		t.Fatal(err)
	}
	if err := config.TrustProject(context.Background(), paths, repo, files.Revision()); err != nil {
		t.Fatal(err)
	}
}
