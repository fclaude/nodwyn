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
	"github.com/fclaude/nodwyn/internal/event"
	"github.com/fclaude/nodwyn/internal/llm"
	fake "github.com/fclaude/nodwyn/test/fakes/ollama"
)

// This repository's .nodwyn/init.scm offers the validate tool and wraps the
// agent turn with the finish check (ADR 37). The test runs it in a session
// on a copy, with a Makefile whose validate target only prints a summary:
// a turn that changes a file and finishes without validate is told to run
// it and goes on; once validate has run on the repository as it is, the
// turn ends; a turn that changes nothing is not told anything.
func TestFinishCheckAsksForValidate(t *testing.T) {
	repo := fixture(t, map[string]string{
		"README.md": "fixture\n",
		"Makefile":  "validate:\n\t@echo 'validate: PASS (0 warnings)'\n",
	})
	copyInit(t, repo)
	srv := fake.New(
		fake.Turn{ToolCalls: []fake.ToolCall{{ID: "w", Name: "fs-write", Args: map[string]any{"path": "notes.txt", "content": "a change\n"}}}},
		fake.Turn{Content: "Done."},
		fake.Turn{ToolCalls: []fake.ToolCall{{ID: "v", Name: "validate", Args: map[string]any{}}}},
		fake.Turn{Content: "validate: PASS (0 warnings)"},
		fake.Turn{Content: "An answer that changes nothing."},
	)
	defer srv.Close()
	cfg := config.Default()
	cfg.Control.URL = srv.URL
	cfg.Control.Model = "test-model"
	// A chat: a person answers the confirms, which turn does.
	s, err := core.New(core.Options{Config: cfg, Repo: repo, InitFile: filepath.Join(repo, ".nodwyn", "init.scm"), Version: "test", Provider: llm.NewOllama(srv.URL), LogWriter: &bytes.Buffer{}})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	turn(t, s, "Write a note.")
	if len(srv.Requests) != 4 {
		t.Fatalf("the turn made %d model requests, want 4", len(srv.Requests))
	}
	reminder := "validate has not run since your last change"
	if !strings.Contains(string(srv.Requests[2].Raw), reminder) {
		t.Fatalf("the turn was not told to validate:\n%s", srv.Requests[2].Raw)
	}
	if !strings.Contains(string(srv.Requests[3].Raw), "validate: PASS (0 warnings)") {
		t.Fatalf("the validate result did not reach the model:\n%s", srv.Requests[3].Raw)
	}
	if strings.Contains(string(srv.Requests[3].Raw[bytes.LastIndex(srv.Requests[3].Raw, []byte("validate: PASS")):]), reminder) {
		t.Fatal("told again after validate ran")
	}

	turn(t, s, "A question.")
	if len(srv.Requests) != 5 {
		t.Fatalf("a turn that changed nothing made %d requests in all, want 5", len(srv.Requests))
	}

	// A reload runs init.scm again; the check must wrap the turn once, not
	// wrap itself.
	ctx := context.Background()
	if _, err := s.Eval(ctx, `(define validate-probe validate-base-agent-turn)`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Eval(ctx, `(reload)`); err != nil {
		t.Fatal(err)
	}
	if v, err := s.Eval(ctx, `(eq? validate-probe validate-base-agent-turn)`); err != nil || v.String() != "#t" {
		t.Fatalf("after a reload the check wraps %v, %v", v, err)
	}
}

// In a run, which answers its own confirms, the default loop's finish check
// asks for a test run after the last change: validate counts as one, so
// neither check asks again once it has run.
func TestValidateCountsAsARunsTestRun(t *testing.T) {
	repo := fixture(t, map[string]string{
		"README.md": "fixture\n",
		"Makefile":  "validate:\n\t@echo 'validate: PASS (0 warnings)'\n",
	})
	copyInit(t, repo)
	srv := fake.New(
		fake.Turn{ToolCalls: []fake.ToolCall{{ID: "w", Name: "fs-write", Args: map[string]any{"path": "notes.txt", "content": "a change\n"}}}},
		fake.Turn{ToolCalls: []fake.ToolCall{{ID: "v", Name: "validate", Args: map[string]any{}}}},
		fake.Turn{Content: "Done."},
		fake.Turn{Content: "notes.txt is the change."},
	)
	defer srv.Close()
	cfg := config.Default()
	cfg.Control.URL = srv.URL
	cfg.Control.Model = "test-model"
	s, err := core.New(core.Options{Config: cfg, Repo: repo, InitFile: filepath.Join(repo, ".nodwyn", "init.scm"), Version: "test", Provider: llm.NewOllama(srv.URL),
		AutoApprove: true, LogWriter: &bytes.Buffer{}})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	turn(t, s, "Write a note.")
	// The fourth request is the default loop asking about the new,
	// untracked notes.txt; nothing asks for tests or validate.
	if len(srv.Requests) != 4 {
		t.Fatalf("the run made %d model requests, want 4", len(srv.Requests))
	}
	for i, r := range srv.Requests {
		for _, asked := range []string{"after your last test run", "validate has not run since"} {
			if strings.Contains(string(r.Raw), asked) {
				t.Fatalf("request %d asks again (%q) after validate ran", i, asked)
			}
		}
	}
}

// copyInit puts this repository's .nodwyn/init.scm and the dev/ files it
// loads into repo.
func copyInit(t *testing.T, repo string) {
	t.Helper()
	files := map[string]string{}
	for _, p := range []string{".nodwyn/init.scm", "dev/go.scm", "dev/git.scm", "dev/repo.scm"} {
		raw, err := os.ReadFile(filepath.Join("..", "..", p))
		if err != nil {
			t.Fatal(err)
		}
		files[p] = string(raw)
	}
	writeAll(t, repo, files)
}

// turn sends text and waits for the session to go idle, answering any
// confirm with yes.
func turn(t *testing.T, s *core.Session, text string) {
	t.Helper()
	if err := s.Send(text); err != nil {
		t.Fatal(err)
	}
	idle(t, s)
}

// idle waits for the turn under way to end, answering any confirm with yes.
func idle(t *testing.T, s *core.Session) {
	t.Helper()
	timeout := time.After(60 * time.Second)
	for {
		select {
		case ev := <-s.Events():
			switch ev.Kind {
			case event.PromptConfirm:
				if err := s.Answer(ev.Payload.(event.Confirm).ID, "y"); err != nil {
					t.Fatal(err)
				}
			case event.Error:
				t.Fatalf("turn failed: %v", ev.Payload)
			case event.Status:
				if ev.Payload.(event.StatusInfo).State == "idle" {
					return
				}
			}
		case <-timeout:
			t.Fatal("no idle after 60s")
		}
	}
}

// A turn that only commits what validate already checked is not told to
// validate again: the change is measured against the commit the turn began
// on, and a commit moves nothing out of it. Measured against HEAD, every
// commit-only turn was told to (at least ten times in the local sessions of
// 2026-10-04 and 05).
func TestACommitAloneIsNoChangeToValidate(t *testing.T) {
	repo := fixture(t, map[string]string{
		"README.md": "fixture\n",
		"Makefile":  "validate:\n\t@echo 'validate: PASS (0 warnings)'\n",
	})
	copyInit(t, repo)
	srv := fake.New(
		fake.Turn{ToolCalls: []fake.ToolCall{{ID: "w", Name: "fs-write", Args: map[string]any{"path": "README.md", "content": "fixture, changed\n"}}}},
		fake.Turn{ToolCalls: []fake.ToolCall{{ID: "v", Name: "validate", Args: map[string]any{}}}},
		fake.Turn{Content: "validate: PASS (0 warnings)"},
		fake.Turn{ToolCalls: []fake.ToolCall{{ID: "c", Name: "shell-exec", Args: map[string]any{"cmd": "git", "args": []string{"-c", "user.name=test", "-c", "user.email=test@example.com", "commit", "-qam", "the change"}}}}},
		fake.Turn{Content: "Committed."},
	)
	defer srv.Close()
	cfg := config.Default()
	cfg.Control.URL = srv.URL
	cfg.Control.Model = "test-model"
	logb := &bytes.Buffer{}
	s, err := core.New(core.Options{Config: cfg, Repo: repo, InitFile: filepath.Join(repo, ".nodwyn", "init.scm"), Version: "test", Provider: llm.NewOllama(srv.URL), LogWriter: logb})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	turn(t, s, "Change the README.")
	turn(t, s, "Commit it.")
	if out, err := exec.Command("git", "-C", repo, "status", "--porcelain", "--untracked-files=no").Output(); err != nil || strings.TrimSpace(string(out)) != "" {
		t.Fatalf("the commit did not happen: %q %v\n%s", out, err, logb.String())
	}
	if len(srv.Requests) != 5 {
		t.Fatalf("%d model requests, want 5: a commit-only turn was asked something more", len(srv.Requests))
	}
	if strings.Contains(string(srv.Requests[4].Raw), "validate has not run since your last change") {
		t.Fatal("a commit-only turn was told to validate again")
	}
}

// offersTool reports whether the request offered the model a tool by name.
func offersTool(r fake.Request, name string) bool {
	for _, raw := range r.Tools {
		if strings.Contains(string(raw), `"name":"`+name+`"`) {
			return true
		}
	}
	return false
}

// The validate reminder names what the model can run: the validate tool in
// the full profile, which offers it, and make validate through shell-exec in
// the lean one, which offers lean-tools alone (shell-exec and fs-edit). It
// named the tool in both, and a lean model was told to call a tool it could
// not (ADR 37, amended 2026-10-18). The model answers without validating, so
// the turn is reminded twice, the reminder's limit.
func TestTheValidateReminderNamesWhatTheModelCanRun(t *testing.T) {
	for _, c := range []struct {
		p         finishProfile
		offered   bool
		says, not string
	}{
		{finishProfiles[0], true, "Run validate, read its summary", "Run make validate with shell-exec"},
		{finishProfiles[1], false, "Run make validate with shell-exec, read its summary", "Run validate,"},
	} {
		t.Run(c.p.name, func(t *testing.T) {
			s, srv := c.p.session(t, finishRepo(t, false),
				editCall("e", "calc/calc.go", "return 1 }", "return 2 }"),
				fake.Turn{Content: "Done."},
				fake.Turn{Content: "Done, without validate."},
				fake.Turn{Content: "Still without validate."},
			)
			c.p.turn(t, s, 1, 1)
			if len(srv.Requests) != 4 {
				t.Fatalf("%d requests, want 4", len(srv.Requests))
			}
			if got := offersTool(srv.Requests[2], "validate"); got != c.offered {
				t.Fatalf("the %s profile offers validate: %v, want %v", c.p.name, got, c.offered)
			}
			if n := requestsSaying(srv, c.says); n != 2 {
				t.Errorf("%d requests carry %q, want the two after each reminder:\n%s", n, c.says, srv.Requests[3].Raw)
			}
			if n := requestsSaying(srv, c.not); n != 0 {
				t.Errorf("%d requests carry %q", n, c.not)
			}
		})
	}
}

// A run of the repository's validate through shell-exec counts as the tool's,
// when it passes, in the reminder and in the reminder's own continuation:
// after it, with no edit since, the turn is not told that validate has not
// run. The continuation did not count it, so a lean session that ran make
// validate after the first reminder got the second anyway (commit-bench,
// 48f90ea8: "No code changes this turn; re-ran validate"), and a chat's turn
// did not count it at all. A passing check must count in either profile.
func TestAShellValidateRunCountsAsValidate(t *testing.T) {
	const reminder = "validate has not run since your last change"
	shellRepo := func(t *testing.T) string {
		t.Helper()
		repo := fixture(t, map[string]string{
			"Makefile":     "validate:\n\t@echo 'validate: PASS (0 warnings)'\n",
			"calc/calc.go": "package calc\n\nfunc One() int { return 1 }\n",
		})
		copyInit(t, repo)
		return repo
	}
	for _, p := range finishProfiles {
		t.Run(p.name+"/after the reminder", func(t *testing.T) {
			s, srv := p.session(t, shellRepo(t),
				editCall("e", "calc/calc.go", "return 1 }", "return 2 }"),
				fake.Turn{Content: "Done."},
				shellCall("v", "make validate"),
				fake.Turn{Content: "validate: PASS (0 warnings)."},
			)
			p.turn(t, s, 1, 1)
			if len(srv.Requests) != 4 || requestsSaying(srv, reminder) != 2 {
				t.Fatalf("%d requests, %d with the reminder; want 4, the last two with the first reminder alone", len(srv.Requests), requestsSaying(srv, reminder))
			}
			if n := strings.Count(string(srv.Requests[3].Raw), reminder); n != 1 {
				t.Fatalf("the reminder was said %d times, want once", n)
			}
		})
		t.Run(p.name+"/before it", func(t *testing.T) {
			s, srv := p.session(t, shellRepo(t),
				editCall("e", "calc/calc.go", "return 1 }", "return 2 }"),
				shellCall("v", "make validate"),
				fake.Turn{Content: "validate: PASS (0 warnings)."},
			)
			p.turn(t, s, 1, 1)
			if len(srv.Requests) != 3 || requestsSaying(srv, reminder) != 0 {
				t.Fatalf("%d requests, %d with the reminder; want 3 and none", len(srv.Requests), requestsSaying(srv, reminder))
			}
		})
	}
}
