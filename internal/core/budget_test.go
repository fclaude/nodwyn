package core

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/fclaude/nodwyn/internal/budget"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fclaude/nodwyn/internal/event"
	"github.com/fclaude/nodwyn/internal/llm"
	"github.com/fclaude/nodwyn/internal/log"
	fake "github.com/fclaude/nodwyn/test/fakes/ollama"
)

func followBudget(t *testing.T, s *Session, answer func(event.Checkpoint)) []event.Event {
	t.Helper()
	var result []event.Event
	timeout := time.After(5 * time.Second)
	for {
		select {
		case ev := <-s.Events():
			result = append(result, ev)
			switch ev.Kind {
			case event.PromptCheckpoint:
				answer(ev.Payload.(event.Checkpoint))
			case event.PromptConfirm:
				if err := s.Answer(ev.Payload.(event.Confirm).ID, "y"); err != nil {
					t.Fatal(err)
				}
			case event.Status:
				if ev.Payload.(event.StatusInfo).State == "idle" {
					return result
				}
			}
		case <-timeout:
			s.Abort()
			t.Fatal("budget turn did not finish")
		}
	}
}

func TestBudgetCustomCheckpointAndHardBatchStop(t *testing.T) {
	var calls []fake.ToolCall
	for i := 0; i < 4; i++ {
		calls = append(calls, fake.ToolCall{ID: fmt.Sprint(i), Name: "fs-list", Args: map[string]any{"path": "."}})
	}
	srv := fake.New(fake.Turn{ToolCalls: calls}, fake.Turn{Content: "next turn"})
	defer srv.Close()
	o := diskOptions(t)
	o.NoConfirm = false
	o.Provider = llm.NewOllama(srv.URL)
	o.Config.Budget.ToolCallsSoft = 1
	o.Config.Budget.ToolCallsHard = 3
	s, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	s.Send("run")
	prompts := 0
	events := followBudget(t, s, func(p event.Checkpoint) {
		prompts++
		if p.Checkpoint.Dimension != "tool_calls" || p.Checkpoint.Used != 1 {
			t.Fatalf("prompt %+v", p)
		}
		if err := s.Answer(p.ID, "t"); err == nil {
			t.Fatal("tool approval accepted for a budget")
		}
		if err := s.Answer(p.ID, "4"); err == nil {
			t.Fatal("hard ceiling changed")
		}
		if err := s.Answer(p.ID, "3"); err != nil {
			t.Fatal(err)
		}
	})
	executed, results, records := 0, 0, 0
	for _, ev := range events {
		switch ev.Kind {
		case string(log.KindToolCall):
			executed++
		case string(log.KindToolResult):
			results++
		case string(log.KindTurnCheckpoint):
			records++
			if ev.Payload.(log.TurnCheckpoint).Choice.Soft != 3 {
				t.Fatal("custom limit not logged")
			}
		}
	}
	if prompts != 1 || records != 1 || executed != 3 || results != 4 {
		t.Fatalf("prompts=%d records=%d calls=%d results=%d", prompts, records, executed, results)
	}
	if len(s.Allowed()) != 0 || len(s.AllowedWrites()) != 0 || s.turnTrust {
		t.Fatal("checkpoint widened tool permissions")
	}
	if err := s.Send("new approach"); err != nil {
		t.Fatal(err)
	}
	followBudget(t, s, func(event.Checkpoint) { t.Fatal("prior turn budget survived") })
}

func TestBudgetNestedActionsCannotBeCaughtOrHidden(t *testing.T) {
	o := diskOptions(t)
	o.NoConfirm = false
	o.Config.Budget.ActionsSoft = 1
	o.Config.Budget.ActionsHard = 2
	os.MkdirAll(filepath.Join(o.Repo, ".nodwyn"), 0700)
	os.WriteFile(filepath.Join(o.Repo, ".nodwyn", "init.scm"), []byte(`
(register-tool "write-many" (lambda ()
 (guard (e (else (fs-write "caught" "should not run")))
  (fs-write "a" "one") (fs-write "b" "two") (fs-write "c" "three")))
 '((doc . "Write files") (params . ())))`), 0600)
	srv := fake.New(fake.Turn{ToolCalls: []fake.ToolCall{{ID: "wrapped", Name: "write-many", Args: map[string]any{}}}})
	defer srv.Close()
	o.Provider = llm.NewOllama(srv.URL)
	s, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	s.Send("write files")
	prompts := 0
	events := followBudget(t, s, func(p event.Checkpoint) {
		prompts++
		if p.Checkpoint.Dimension != "actions" || p.Checkpoint.State.Actions != 1 || len(p.Checkpoint.State.Files) != 1 || p.Checkpoint.State.Files[0] != "a" {
			t.Fatalf("action checkpoint: %+v", p)
		}
		if err := s.Answer(p.ID, "y"); err != nil {
			t.Fatal(err)
		}
	})
	if prompts != 1 {
		t.Fatalf("prompts: %d", prompts)
	}
	for _, name := range []string{"a", "b"} {
		if _, err := os.Stat(filepath.Join(o.Repo, name)); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"c", "caught"} {
		if _, err := os.Stat(filepath.Join(o.Repo, name)); !os.IsNotExist(err) {
			t.Fatalf("executed forbidden action %s", name)
		}
	}
	found := false
	for _, ev := range events {
		if ev.Kind == event.Error && strings.Contains(ev.Payload.(event.ErrorInfo).Err, "actions budget") {
			found = true
		}
	}
	if !found {
		t.Fatal("hard action stop was not visible")
	}
}

func TestBudgetNoninteractiveAndTokenStop(t *testing.T) {
	for _, dimension := range []string{"tool_calls", "tokens"} {
		t.Run(dimension, func(t *testing.T) {
			o := diskOptions(t)
			if dimension == "tool_calls" {
				o.Config.Budget.ToolCallsSoft = 1
				o.Config.Budget.ToolCallsHard = 3
			} else {
				o.Config.Budget.TokensSoft = 100
				o.Config.Budget.TokensHard = 200
			}
			srv := fake.New(fake.Turn{ToolCalls: []fake.ToolCall{{ID: "a", Name: "fs-list", Args: map[string]any{"path": "."}}, {ID: "b", Name: "fs-list", Args: map[string]any{"path": "."}}}})
			defer srv.Close()
			o.Provider = llm.NewOllama(srv.URL)
			s, err := New(o)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			s.Send("work")
			events := followBudget(t, s, func(event.Checkpoint) { t.Fatal("noninteractive run prompted") })
			calls, checkpoints := 0, 0
			for _, ev := range events {
				if ev.Kind == string(log.KindToolCall) {
					calls++
				}
				if ev.Kind == string(log.KindTurnCheckpoint) {
					checkpoints++
				}
			}
			if dimension == "tokens" && calls != 0 {
				t.Fatal("tools ran past token ceiling")
			}
			if dimension == "tool_calls" && (calls != 1 || checkpoints != 1) {
				t.Fatalf("calls %d checkpoints %d", calls, checkpoints)
			}
		})
	}
}

type waitingProvider struct{}

func (waitingProvider) Ping(context.Context) (string, error) { return "test", nil }
func (waitingProvider) Name() string                         { return "waiting" }
func (waitingProvider) Chat(ctx context.Context, _ llm.Request, _ func(llm.Delta)) (*llm.Response, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestBudgetWallCancelsBlockedModel(t *testing.T) {
	o := diskOptions(t)
	o.Provider = waitingProvider{}
	o.Config.Budget.WallSoft = "50ms"
	o.Config.Budget.WallHard = "100ms"
	s, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	s.Send("wait")
	events := followBudget(t, s, func(event.Checkpoint) { t.Fatal("should be blocked in model") })
	found := false
	for _, ev := range events {
		if ev.Kind == event.Error && strings.Contains(ev.Payload.(event.ErrorInfo).Err, "wall budget") {
			found = true
		}
	}
	if !found {
		t.Fatal("wall stop not surfaced")
	}
}

func TestBudgetAbortCheckpointRemovesPendingReply(t *testing.T) {
	o := diskOptions(t)
	o.NoConfirm = false
	o.Config.Budget.ToolCallsSoft = 1
	o.Config.Budget.ToolCallsHard = 3
	srv := fake.New(fake.Turn{ToolCalls: []fake.ToolCall{{ID: "a", Name: "fs-list", Args: map[string]any{"path": "."}}, {ID: "b", Name: "fs-list", Args: map[string]any{"path": "."}}}})
	defer srv.Close()
	o.Provider = llm.NewOllama(srv.URL)
	s, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	s.Send("work")
	var id string
	events := followBudget(t, s, func(p event.Checkpoint) { id = p.ID; s.Abort() })
	if id == "" {
		t.Fatal("no checkpoint")
	}
	if err := s.Answer(id, "y"); err == nil {
		t.Fatal("stale checkpoint accepted")
	}
	found := false
	for _, ev := range events {
		if ev.Kind == string(log.KindTurnCheckpoint) && ev.Payload.(log.TurnCheckpoint).Error != "" {
			found = true
		}
	}
	if !found {
		t.Fatal("checkpoint cancellation missing from log")
	}
}

func TestDeniedCallsDoNotConsumeActionBudget(t *testing.T) {
	o := diskOptions(t)
	o.Config.Budget.ActionsSoft = 1
	o.Config.Budget.ActionsHard = 1
	o.Config.Policy.AllowWrites = []string{"b"}
	os.MkdirAll(filepath.Join(o.Repo, ".nodwyn"), 0700)
	os.WriteFile(filepath.Join(o.Repo, ".nodwyn", "init.scm"), []byte(`
(register-check "no-a" "deny" (lambda (name args) (and (equal? name "fs-write") (equal? (car args) "a"))))`), 0600)
	srv := fake.New(fake.Turn{ToolCalls: []fake.ToolCall{{ID: "a", Name: "fs-write", Args: map[string]any{"path": "a", "content": "denied"}}, {ID: "b", Name: "fs-write", Args: map[string]any{"path": "b", "content": "allowed"}}}}, fake.Turn{Content: "done"})
	defer srv.Close()
	o.Provider = llm.NewOllama(srv.URL)
	s, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	s.Send("write")
	followBudget(t, s, func(event.Checkpoint) { t.Fatal("unexpected action checkpoint") })
	if _, err := os.Stat(filepath.Join(o.Repo, "a")); !os.IsNotExist(err) {
		t.Fatal("denied write ran")
	}
	if _, err := os.Stat(filepath.Join(o.Repo, "b")); err != nil {
		t.Fatal("denial consumed authorized action budget:", err)
	}
}

func TestCheckpointRedactsEventAndHistory(t *testing.T) {
	o := diskOptions(t)
	o.NoConfirm = false
	s, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	const canary = "sk-ant-abcdefgh1234"
	finished := make(chan error, 1)
	go func() {
		_, err := s.confirmBudget(context.Background(), budget.Checkpoint{Dimension: "actions", Used: 1, Limit: budget.Limit{Soft: 1, Hard: 3}, State: budget.State{Commands: []string{"echo " + canary}}})
		finished <- err
	}()
	timeout := time.After(2 * time.Second)
	for {
		select {
		case ev := <-s.Events():
			raw, _ := json.Marshal(ev.Payload)
			if strings.Contains(string(raw), canary) {
				t.Fatal("checkpoint leaked to frontend")
			}
			if ev.Kind == event.PromptCheckpoint {
				s.Answer(ev.Payload.(event.Checkpoint).ID, "n")
			}
		case err := <-finished:
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, ev := range s.EventsSoFar() {
				if strings.Contains(string(ev.Payload), canary) {
					t.Fatal("checkpoint leaked into history")
				}
				if ev.Kind == log.KindTurnCheckpoint {
					found = true
				}
			}
			if !found {
				t.Fatal("checkpoint not recorded")
			}
			return
		case <-timeout:
			t.Fatal("checkpoint test timed out")
		}
	}
}
