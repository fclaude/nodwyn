package rpc

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/fclaude/nodwyn/internal/event"
	"github.com/fclaude/nodwyn/internal/frontend/run"
	"github.com/fclaude/nodwyn/internal/llm"
	fake "github.com/fclaude/nodwyn/test/fakes/ollama"
)

func beginRun(t *testing.T, c *client) runResult {
	t.Helper()
	var result runResult
	if err := json.Unmarshal(c.ok("run.start", runParams{Repo: t.TempDir(), Task: "test task", Wall: "1m", MaxTurns: 3}), &result); err != nil {
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
func TestRunDetachCheckpointAndResume(t *testing.T) {
	srv := fake.New(fake.Turn{Content: "1. first step"}, fake.Turn{Content: "step done"}, fake.Turn{Content: "1. final step"}, fake.Turn{Content: "done\n" + run.CompleteLine})
	defer srv.Close()
	o := options(t)
	o.Provider = llm.NewOllama(srv.URL)
	path, stop := start(t, o)
	c := connect(t, path)
	c.hello()
	started := beginRun(t, c)
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
	json.Unmarshal(c.ok("run.resume", idParams{started.ID}), &resumed)
	if resumed.State.Turns != 1 || resumed.State.MaxTurns != 3 || !resumed.State.Deadline.Equal(before.Deadline) {
		t.Fatalf("limits reset: before=%+v after=%+v", before, resumed.State)
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
	started := beginRun(t, c)
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
