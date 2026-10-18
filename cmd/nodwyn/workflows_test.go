package main

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fclaude/nodwyn/internal/config"
	"github.com/fclaude/nodwyn/internal/core"
	"github.com/fclaude/nodwyn/internal/frontend/rpc"
	"github.com/fclaude/nodwyn/internal/log"
	"github.com/fclaude/nodwyn/internal/schedule"
	"github.com/fclaude/nodwyn/internal/watch"
	"github.com/fclaude/nodwyn/internal/workflow"
)

// nodwyn workflows and nodwyn outbox read what the console shows, in the same
// words, and decide the same way: a held answer is discarded, a workflow paused
// and resumed, and a reference that means nothing is refused. The daemon is in
// this process; the workflow and its held answer are written where it reads
// them, so no forge is needed.
func TestWorkflowsAndTheOutboxFromTheCommandLine(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "nodwyn-wf-cli-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	socket := filepath.Join(dir, "sock")
	listener, err := rpc.Listen(socket)
	if err != nil {
		t.Fatal(err)
	}
	paths := config.PathsUnder(filepath.Join(dir, "config"), filepath.Join(dir, "data"))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- rpc.Serve(ctx, listener, core.Options{Config: config.Default(), Paths: paths}) }()
	defer func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()
	repo := filepath.Join(dir, "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	invoke := func(args ...string) (string, error) {
		t.Helper()
		callCtx, stop := context.WithTimeout(context.Background(), 20*time.Second)
		defer stop()
		cmd := exec.CommandContext(callCtx, os.Args[0], append([]string{"-test.run=^TestCLIHelper$", "--"}, args...)...)
		cmd.Env = append(os.Environ(), "NODWYN_TEST_CLI=1", quickExit, "XDG_CONFIG_HOME="+filepath.Join(dir, "cli-config"), "XDG_DATA_HOME="+filepath.Join(dir, "cli-data"))
		var stdout, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		err := cmd.Run()
		if err != nil {
			return stdout.String() + stderr.String(), err
		}
		return stdout.String(), nil
	}
	// A scheduled task is a timetable: listed, paused and resumed like a
	// workflow that watches a source.
	cfg := config.Default()
	session, err := core.New(core.Options{Config: cfg, Paths: paths, Repo: repo})
	if err != nil {
		t.Fatal(err)
	}
	taskID := session.ID()
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	rule := schedule.Rule{At: []string{"15:00"}, Days: []string{"tue", "thu"}, From: "2099-10-16T00:00"}
	if err := (core.Store{Dir: paths.SessionsDir}).SetSchedule(taskID, log.SessionSchedule{Definition: schedule.Definition{Message: "Draft the notes.", Rule: &rule}}); err != nil {
		t.Fatal(err)
	}
	tableRef := workflowRef(taskID)
	if out, err := invoke("workflows", "--socket", socket); err != nil || !strings.Contains(out, "["+tableRef+"]  a timetable") || !strings.Contains(out, "Tuesdays and Thursdays at 15:00") || !strings.Contains(out, "Next: Tue 20 Oct 2099 15:00") {
		t.Fatalf("%q %v", out, err)
	}
	if out, err := invoke("workflows", "--socket", socket, "pause", tableRef); err != nil || !strings.Contains(out, "paused.") {
		t.Fatalf("%q %v", out, err)
	}
	if out, err := invoke("workflows", "--socket", socket); err != nil || !strings.Contains(out, "  Paused") {
		t.Fatalf("%q %v", out, err)
	}
	if out, err := invoke("workflows", "--socket", socket, "resume", tableRef); err != nil || !strings.Contains(out, "resumed.") {
		t.Fatalf("%q %v", out, err)
	}
	if err := (core.Store{Dir: paths.SessionsDir}).SetSchedule(taskID, log.SessionSchedule{Removed: true}); err != nil {
		t.Fatal(err)
	}
	// Nothing else yet.
	if out, err := invoke("workflows", "--socket", socket); err != nil || !strings.Contains(out, "No workflows") {
		t.Fatalf("%q %v", out, err)
	}
	if out, err := invoke("outbox", "--socket", socket); err != nil || !strings.Contains(out, "Nothing is waiting") {
		t.Fatalf("%q %v", out, err)
	}
	// A workflow that watches a command, and an answer it holds.
	store := workflow.Store{Dir: filepath.Join(paths.DataDir, "workflows")}
	id := workflow.NewID()
	def := workflow.Definition{Name: "Check staging", Every: 1440, Message: "Look at it.", Routing: workflow.OneTask, Repo: repo, Source: watch.Source{Kind: watch.Command, Command: []string{"/usr/bin/true"}, Dir: repo}}
	if _, err := store.Append(id, workflow.KindDefine, workflow.Define{Definition: def, Actor: "test"}); err != nil {
		t.Fatal(err)
	}
	held := workflow.Outbox{ID: workflow.NewID(), Kind: "review", Item: "142", Task: "t-1", Summary: "Pull request #142 opened: fix: login loop", Body: "Needs a test for the empty case.", At: time.Now()}
	if _, err := store.Append(id, workflow.KindOutbox, held); err != nil {
		t.Fatal(err)
	}
	ref := workflowRef(id)
	out, err := invoke("workflows", "--socket", socket)
	if err != nil || !strings.Contains(out, "Check staging  ["+ref+"]") || !strings.Contains(out, "When /usr/bin/true prints a new or changed item") || !strings.Contains(out, "1 answer waiting to be posted") {
		t.Fatalf("%q %v", out, err)
	}
	if out, err = invoke("workflows", "--socket", socket, "pause", ref); err != nil || !strings.Contains(out, "Check staging: Paused") {
		t.Fatalf("%q %v", out, err)
	}
	if out, err = invoke("workflows", "--socket", socket, "resume", ref); err != nil || strings.Contains(out, "Paused") {
		t.Fatalf("%q %v", out, err)
	}
	if out, err = invoke("workflows", "--socket", socket, "pause", "zzzzzzzz"); err == nil || !strings.Contains(out, "no workflow ends with") {
		t.Fatalf("a reference that means nothing was taken: %q %v", out, err)
	}
	heldRef := workflowRef(held.ID)
	if out, err = invoke("outbox", "--socket", socket); err != nil || !strings.Contains(out, "["+heldRef+"] Review for #142 · Check staging") || !strings.Contains(out, "> Needs a test for the empty case.") {
		t.Fatalf("%q %v", out, err)
	}
	if out, err = invoke("outbox", "--socket", socket, "discard", heldRef); err != nil || !strings.Contains(out, "Discarded. Nothing was posted.") {
		t.Fatalf("%q %v", out, err)
	}
	state, err := store.Read(id)
	if err != nil || len(state.Outbox) != 0 || len(state.Delivered) != 1 || state.Delivered[0].Fate != "discarded" || state.Delivered[0].By == "" {
		t.Fatalf("%+v %v", state, err)
	}
	if out, err = invoke("outbox", "--socket", socket, "discard", heldRef); err == nil || !strings.Contains(out, "no held answer ends with") {
		t.Fatalf("a decided answer was decided again: %q %v", out, err)
	}
	if out, err = invoke("outbox", "--socket", socket, "frobnicate"); err == nil || !strings.Contains(out, "use nodwyn outbox") {
		t.Fatalf("%q %v", out, err)
	}
	// Without a daemon it says so.
	if out, err = invoke("workflows", "--socket", filepath.Join(dir, "none")); err == nil || !strings.Contains(out, "needs a running nodwyn daemon") {
		t.Fatalf("%q %v", out, err)
	}
}

// workflowRef is the reference the list shows for an id.
func workflowRef(id string) string { return id[len(id)-8:] }
