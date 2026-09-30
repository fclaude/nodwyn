package core

import (
	"bytes"
	"strings"
	"testing"

	"github.com/fclaude/nodwyn/internal/config"
	"github.com/fclaude/nodwyn/internal/llm"
	fake "github.com/fclaude/nodwyn/test/fakes/ollama"
)

// unattendedRig is a --yolo session: nobody approves its calls.
func unattendedRig(t *testing.T, turns ...fake.Turn) *rig {
	t.Helper()
	repo := t.TempDir()
	srv := fake.New(turns...)
	t.Cleanup(srv.Close)
	logb := &bytes.Buffer{}
	s, err := New(Options{Config: config.Default(), Repo: repo, Provider: llm.NewOllama(srv.URL), AutoApprove: true, LogWriter: logb})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return &rig{t: t, s: s, srv: srv, repo: repo, logb: logb}
}

func shell(id, script string) fake.Turn {
	return fake.Turn{ToolCalls: []fake.ToolCall{{ID: id, Name: "shell-exec", Args: map[string]any{"cmd": "sh", "args": []string{"-c", script}}}}}
}

func writeFile(id string) fake.Turn {
	return fake.Turn{ToolCalls: []fake.ToolCall{{ID: id, Name: "fs-write", Args: map[string]any{"path": "fix.txt", "content": "x\n"}}}}
}

func lastMessage(r *rig, i int) string {
	msgs := requestMessages(r.t, r.srv.Requests[i])
	return msgs[len(msgs)-1].Content
}

func TestUnattendedSessionsGetTheUnattendedPrompt(t *testing.T) {
	u := unattendedRig(t)
	if sys := u.s.loop.WindowContext().System; !strings.Contains(sys, "You work unattended") || !strings.Contains(sys, "Set up a way to check your work") {
		t.Fatalf("unattended system prompt:\n%s", sys)
	}
	i := newRig(t, true)
	if sys := i.s.loop.WindowContext().System; strings.Contains(sys, "You work unattended") {
		t.Fatal("an interactive session got the unattended prompt")
	}
}

// Unattended, a turn ends on evidence: a change, then a test run after it.
func TestUnattendedTurnEndsOnEvidence(t *testing.T) {
	r := unattendedRig(t,
		fake.Turn{Content: "done"},         // nothing changed: reminded
		writeFile("w"),                     // a change
		fake.Turn{Content: "done"},         // untested: reminded
		shell("t", "true # go test ./..."), // a test run after the change
		fake.Turn{Content: "done"},         // ends
	)
	if e := r.turn("fix it"); e != "" {
		t.Fatal(e)
	}
	if len(r.srv.Requests) != 5 {
		t.Fatalf("%d requests, want 5", len(r.srv.Requests))
	}
	if !strings.Contains(lastMessage(r, 1), "No file has changed yet") {
		t.Fatalf("no-change reminder: %q", lastMessage(r, 1))
	}
	if !strings.Contains(lastMessage(r, 3), "after your last test run") {
		t.Fatalf("untested reminder: %q", lastMessage(r, 3))
	}
}

// Test commands that keep failing to run get one way out: report the change
// as unverified instead of spending the budget.
func TestUnrunnableTestsGetAWayOut(t *testing.T) {
	turns := []fake.Turn{writeFile("w")}
	for i := 0; i < 6; i++ {
		// Different each time: the same call four times in a row is a
		// progress stop, a different guard.
		turns = append(turns, shell("t"+string(rune('a'+i)), "exit 4 # pytest -q -k case"+string(rune('a'+i))))
	}
	turns = append(turns, fake.Turn{Content: "The tests cannot run here; the change is unverified."})
	r := unattendedRig(t, turns...)
	if e := r.turn("fix it"); e != "" {
		t.Fatal(e)
	}
	if len(r.srv.Requests) != 8 || !strings.Contains(lastMessage(r, 7), "did not run successfully") {
		t.Fatalf("%d requests; last message %q", len(r.srv.Requests), lastMessage(r, 7))
	}
}

// Before a plan is drafted, a read-only explorer looks at the repository and
// the planner is told what it found.
func TestPlannerExploresFirst(t *testing.T) {
	r := newRig(t, true,
		fake.Turn{Content: "main.go declares package main; tests run with go test ./..."},
		fake.Turn{Content: "1. Edit main.go."},
		fake.Turn{Content: "done"}, fake.Turn{Content: "done"}, fake.Turn{Content: "done"},
	)
	if err := r.s.SendPlanned("change main.go"); err != nil {
		t.Fatal(err)
	}
	r.drain("n")
	explorer := requestMessages(t, r.srv.Requests[0])
	if !strings.HasPrefix(explorer[0].Content, "You explore a repository") || !strings.Contains(explorer[1].Content, "change main.go") {
		t.Fatalf("the explorer's context: %+v", explorer)
	}
	if planner := lastMessage(r, 1); !strings.Contains(planner, "What a look at the repository found:\nmain.go declares package main") {
		t.Fatalf("the planner was not told what exploring found:\n%s", planner)
	}
}
