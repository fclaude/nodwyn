package main

import (
	"bytes"
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fclaude/nodwyn/internal/config"
	"github.com/fclaude/nodwyn/internal/core"
	"github.com/fclaude/nodwyn/internal/llm"
	fake "github.com/fclaude/nodwyn/test/fakes/ollama"
)

func TestGitLandValidation(t *testing.T) {
	for _, name := range []string{"unchanged-tree", "commit-id-only-rebase", "changed-tree-rebase", "unvalidated-tree", "failed-validation", "edit-after-validation"} {
		t.Run(name, func(t *testing.T) {
			repo := fixture(t, map[string]string{
				"README.md": "fixture\n",
				"Makefile":  "validate:\n\t@echo 'validate: PASS (0 warnings)'\n",
			})
			copyInit(t, repo)
			gitIn(t, repo, "config", "user.name", "Test")
			gitIn(t, repo, "config", "user.email", "test@example.invalid")
			gitIn(t, repo, "add", "-A")
			gitIn(t, repo, "commit", "-qm", "The fixture offers validation")
			remote := filepath.Join(t.TempDir(), "remote.git")
			gitIn(t, repo, "init", "-q", "--bare", "-b", "main", remote)
			gitIn(t, repo, "remote", "add", "origin", remote)
			gitIn(t, repo, "push", "-qu", "origin", "main")
			if name == "failed-validation" {
				writeAll(t, repo, map[string]string{"Makefile": "validate:\n\t@echo 'validate: FAIL (0 warnings)'\n\t@false\n"})
				gitIn(t, repo, "commit", "-qam", "Validation fails")
				gitIn(t, repo, "push", "-q")
			}
			turns := []fake.Turn{{ToolCalls: []fake.ToolCall{{ID: "w", Name: "fs-write", Args: map[string]any{"path": "new.txt", "content": "completed work\n"}}}}}
			if name != "unvalidated-tree" {
				turns = append(turns, fake.Turn{ToolCalls: []fake.ToolCall{{ID: "v", Name: "validate", Args: map[string]any{}}}})
			}
			if name == "edit-after-validation" {
				turns = append(turns, fake.Turn{ToolCalls: []fake.ToolCall{{ID: "w2", Name: "fs-write", Args: map[string]any{"path": "new.txt", "content": "changed again\n"}}}})
			}
			turns = append(turns, fake.Turn{ToolCalls: []fake.ToolCall{{ID: "land", Name: "git-land", Args: map[string]any{"message": "The work is done"}}}}, fake.Turn{Content: "Landed."}, fake.Turn{Content: "Validation needed."}, fake.Turn{Content: "Still needed."})
			srv := fake.NewT(t, turns...)
			defer srv.Close()
			cfg := config.Default()
			cfg.Control.URL = srv.URL
			cfg.Control.Model = "test-model"
			logb := &bytes.Buffer{}
			s, err := core.New(core.Options{Config: cfg, Repo: repo, Worktree: true, InitFile: filepath.Join(repo, ".nodwyn", "init.scm"), Version: "test", Provider: llm.NewOllama(srv.URL), LogWriter: logb})
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			if name == "changed-tree-rebase" {
				writeAll(t, repo, map[string]string{"README.md": "new main content\n"})
				gitIn(t, repo, "commit", "-qam", "Main moves")
				gitIn(t, repo, "push", "-q")
			}
			if name == "commit-id-only-rebase" {
				gitIn(t, repo, "commit", "--allow-empty", "-qm", "Main advances without content changes")
				gitIn(t, repo, "push", "-q")
			}
			turn(t, s, "Finish and publish the work.")
			if !strings.Contains(logb.String(), "pushed 1 commit(s) to origin main") {
				t.Fatalf("git-land did not publish:\n%s", logb.String())
			}
			v, err := s.Eval(context.Background(), `(repository)`)
			if err != nil {
				t.Fatal(err)
			}
			out, err := exec.Command("git", "-C", v.S, "show", "HEAD:new.txt").Output()
			wantContents := "completed work\n"
			if name == "edit-after-validation" {
				wantContents = "changed again\n"
			}
			if err != nil || string(out) != wantContents {
				t.Fatalf("landed tree: %q %v", out, err)
			}
			if name == "changed-tree-rebase" {
				out, err := exec.Command("git", "-C", v.S, "show", "HEAD:README.md").Output()
				if err != nil || string(out) != "new main content\n" {
					t.Fatalf("rebase did not include new main content: %q %v", out, err)
				}
			}
			wantReminder := name != "unchanged-tree" && name != "commit-id-only-rebase"
			gotReminder := false
			for _, request := range srv.Requests {
				gotReminder = gotReminder || strings.Contains(string(request.Raw), "validate has not run since your last change")
			}
			if gotReminder != wantReminder {
				t.Fatalf("validation reminder %v, want %v after %s", gotReminder, wantReminder, name)
			}
		})
	}
}

// A completed attempt is not a certificate: a zero exit, a PASS summary and
// unchanged contents must agree, whether the check is a tool or a shell call.
func TestValidationCertifiesOnlyPassingUnchangedContents(t *testing.T) {
	for _, mode := range []string{"tool", "shell", "background"} {
		for _, outcome := range []string{"pass", "fail", "missing-summary", "changed-during-check"} {
			t.Run(mode+"/"+outcome, func(t *testing.T) {
				recipe := "\t@echo 'validate: PASS (0 warnings)'\n"
				switch outcome {
				case "fail":
					recipe += "\t@exit 1\n"
				case "missing-summary":
					recipe = "\t@echo 'checks did not run'\n"
				case "changed-during-check":
					recipe = "\t@echo changed > notes.txt\n" + recipe
				}
				repo := fixture(t, map[string]string{"Makefile": "validate:\n" + recipe, "notes.txt": "before\n"})
				copyInit(t, repo)
				calls := []fake.Turn{{ToolCalls: []fake.ToolCall{{ID: "write", Name: "fs-write", Args: map[string]any{"path": "notes.txt", "content": "edited\n"}}}}}
				switch mode {
				case "tool":
					calls = append(calls, fake.Turn{ToolCalls: []fake.ToolCall{{ID: "v", Name: "validate", Args: map[string]any{}}}})
				case "shell":
					calls = append(calls, shellCall("v", "make validate"))
				case "background":
					calls = append(calls, fake.Turn{ToolCalls: []fake.ToolCall{{ID: "v", Name: "shell-start", Args: map[string]any{"cmd": "make", "args": []any{"validate"}}}}}, fake.Turn{ToolCalls: []fake.ToolCall{{ID: "read", Name: "shell-read", Args: map[string]any{"id": "p1", "wait": 10}}}})
				}
				for i := 0; i < 6; i++ {
					calls = append(calls, fake.Turn{Content: "Done."})
				}
				srv := fake.NewT(t, calls...)
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
				turn(t, s, "Edit the note and check it.")
				state, err := s.Eval(context.Background(), `(and validated-state (equal? validated-state (validation-state)))`)
				if err != nil {
					t.Fatal(err)
				}
				if got, want := state.IsTrue(), outcome == "pass"; got != want {
					t.Fatalf("certified=%v want %v", got, want)
				}
			})
		}
	}
}
