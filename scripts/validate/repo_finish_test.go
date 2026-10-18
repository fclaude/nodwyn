package main

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fclaude/nodwyn/internal/config"
	"github.com/fclaude/nodwyn/internal/core"
	"github.com/fclaude/nodwyn/internal/llm"
	fake "github.com/fclaude/nodwyn/test/fakes/ollama"
)

// This repository's .nodwyn/init.scm tells a turn that changed Go or Scheme
// code what its change lacks of what AGENTS.md asks a change to carry, once a
// session, in both profiles (ADR 32, amended 2026-10-18). finishIntro is how
// the message begins, and finishLines its line for each thing missing.
const finishIntro = "This turn changed Go or Scheme code, and AGENTS.md says a change carries everything that belongs with it."

var finishLines = map[string]string{
	"test":      "- no test: no test file changed",
	"docs":      "- no document: nothing under docs/ changed",
	"adr":       "- no decision record: nothing under docs/adr changed",
	"catalogue": "- an old source catalogue: Go or Scheme files or their line counts changed",
}

// finishProfile is a session of one profile as auto chooses it: the full
// loop in a chat a person approves, the lean loop in a run nobody does
// (commit-bench runs it so, --yolo --unattended). The lean loop does not
// offer the validate tool, so its model runs make validate through the
// shell, which the evidence loop counts as validate's run; a chat's model
// calls the tool.
type finishProfile struct {
	name     string
	prompt   string // in every request's system prompt
	validate fake.Turn
}

var finishProfiles = []finishProfile{
	{config.ProfileFull, "You are nodwyn, a coding agent", fake.Turn{ToolCalls: []fake.ToolCall{{ID: "v", Name: "validate", Args: map[string]any{}}}}},
	{config.ProfileLean, "nobody approves or answers your calls", shellCall("v", "make validate")},
}

func (p finishProfile) session(t *testing.T, repo string, turns ...fake.Turn) (*core.Session, *fake.Server) {
	t.Helper()
	t.Setenv(config.ProfileEnv, p.name)
	srv := fake.NewT(t, turns...)
	t.Cleanup(srv.Close)
	cfg := config.Default()
	cfg.Control.URL = srv.URL
	cfg.Control.Model = "test-model"
	run := p.name == config.ProfileLean
	s, err := core.New(core.Options{Config: cfg, Repo: repo, InitFile: filepath.Join(repo, ".nodwyn", "init.scm"), Version: "test",
		Provider: llm.NewOllama(srv.URL), AutoApprove: run, Unattended: run, LogWriter: &bytes.Buffer{}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s, srv
}

// turn is the profile's turn: a chat's message, or a run's turn n of max.
func (p finishProfile) turn(t *testing.T, s *core.Session, n, max int) {
	t.Helper()
	const task = "Make One return a larger number."
	var err error
	if p.name == config.ProfileLean {
		err = s.SendRunTurn(task, "TASK COMPLETE", n, max)
	} else {
		err = s.Send(task)
	}
	if err != nil {
		t.Fatal(err)
	}
	idle(t, s)
}

func shellCall(id, line string) fake.Turn {
	return fake.Turn{ToolCalls: []fake.ToolCall{{ID: id, Name: "shell-exec", Args: map[string]any{"cmd": "bash", "args": []any{"-c", line}}}}}
}

func editCall(id, path, old, new string) fake.Turn {
	return fake.Turn{ToolCalls: []fake.ToolCall{{ID: id, Name: "fs-edit", Args: map[string]any{"path": path, "old": old, "new": new}}}}
}

// finishRepo is a repository with Go code, an ADR, a source catalogue and,
// when agents is true, an AGENTS.md; this repository's init.scm and the dev/
// files it loads are in it, untracked.
func finishRepo(t *testing.T, agents bool) string {
	t.Helper()
	files := map[string]string{
		"Makefile":                       "validate:\n\t@echo 'validate: PASS (0 warnings)'\n",
		"calc/calc.go":                   "package calc\n\nfunc One() int { return 1 }\n",
		"docs/adr/0001-one.md":           "# ADR 0001: One\n",
		"docs/book/source/catalogue.tex": "% the source catalogue\n",
	}
	if agents {
		files["AGENTS.md"] = "# Working here\n"
	}
	repo := fixture(t, files)
	copyInit(t, repo)
	return repo
}

// requestsSaying counts the requests whose text holds want.
func requestsSaying(srv *fake.Server, want string) int {
	n := 0
	for _, r := range srv.Requests {
		if strings.Contains(string(r.Raw), want) {
			n++
		}
	}
	return n
}

// A turn that changed code and ends with nothing else in the change is told,
// after validate has run, what AGENTS.md asks for that it lacks: a test, a
// document, a decision record and, since the code's line count changed, the
// regenerated catalogue. It goes on, and its next answer ends it.
func TestTheFinishCheckSaysWhatTheChangeLacks(t *testing.T) {
	for _, p := range finishProfiles {
		t.Run(p.name, func(t *testing.T) {
			s, srv := p.session(t, finishRepo(t, true),
				editCall("e", "calc/calc.go", "return 1 }\n", "return 2 }\n\nfunc Two() int { return 2 }\n"),
				p.validate,
				fake.Turn{Content: "Done: One returns 2. validate: PASS (0 warnings)"},
				fake.Turn{Content: "The fixture has no tests or documents to change."},
			)
			p.turn(t, s, 1, 1)
			if len(srv.Requests) != 4 {
				t.Fatalf("%d requests, want 4", len(srv.Requests))
			}
			if n := requestsSaying(srv, p.prompt); n != 4 {
				t.Fatalf("%d of 4 requests ran the %s profile", n, p.name)
			}
			if n := requestsSaying(srv, finishIntro); n != 1 {
				t.Fatalf("%d requests carry the finish check, want the last alone", n)
			}
			last := string(srv.Requests[3].Raw)
			for what, line := range finishLines {
				if !strings.Contains(last, line) {
					t.Errorf("the finish check does not say %s is missing:\n%s", what, last)
				}
			}
			if !strings.Contains(last, "or say in your answer why it needs none of it") {
				t.Error("the finish check offers no way out for a change that needs none of it")
			}
		})
	}
}

// Only what is missing is said: a change of code whose line count is the
// same leaves the catalogue as it was, and a document outside docs/adr is a
// document but no decision record.
func TestTheFinishCheckSaysOnlyWhatIsMissing(t *testing.T) {
	for _, p := range finishProfiles {
		t.Run(p.name, func(t *testing.T) {
			s, srv := p.session(t, finishRepo(t, true),
				editCall("e", "calc/calc.go", "return 1 }", "return 2 }"),
				shellCall("w", "mkdir -p docs && echo 'One returns 2.' > docs/manual.md"),
				p.validate,
				fake.Turn{Content: "Done."},
				fake.Turn{Content: "A one-line change needs no ADR; I add a test."},
			)
			p.turn(t, s, 1, 1)
			if len(srv.Requests) != 5 {
				t.Fatalf("%d requests, want 5", len(srv.Requests))
			}
			last := string(srv.Requests[4].Raw)
			for what, line := range finishLines {
				if want := what == "test" || what == "adr"; strings.Contains(last, line) != want {
					t.Errorf("the finish check says %s is missing: %v, want %v\n%s", what, !want, want, last)
				}
			}
		})
	}
}

// A change that carries a test, an ADR amendment and the regenerated
// catalogue is told nothing, and neither is a change in a revision with no
// AGENTS.md, which the check cites: the turn ends on the model's answer.
// The session's repo-finish-told says the check was there to keep quiet.
func TestTheFinishCheckSaysNothingWhenNothingIsMissing(t *testing.T) {
	quiet := func(t *testing.T, s *core.Session, srv *fake.Server, requests int) {
		t.Helper()
		if len(srv.Requests) != requests || requestsSaying(srv, finishIntro) != 0 {
			t.Fatalf("%d requests, %d with the finish check; want %d and none", len(srv.Requests), requestsSaying(srv, finishIntro), requests)
		}
		if v, err := s.Eval(context.Background(), "repo-finish-told"); err != nil || v.String() != "#f" {
			t.Fatalf("repo-finish-told is %v (%v): the session ran without the check", v, err)
		}
	}
	for _, p := range finishProfiles {
		t.Run(p.name+"/whole", func(t *testing.T) {
			s, srv := p.session(t, finishRepo(t, true),
				editCall("e", "calc/calc.go", "return 1 }\n", "return 2 }\n\nfunc Two() int { return 2 }\n"),
				shellCall("w", "echo 'package calc' > calc/calc_test.go && echo '## Amendment' >> docs/adr/0001-one.md && echo '% regenerated' >> docs/book/source/catalogue.tex"),
				p.validate,
				fake.Turn{Content: "Done."},
			)
			p.turn(t, s, 1, 1)
			quiet(t, s, srv, 4)
		})
		t.Run(p.name+"/no AGENTS.md", func(t *testing.T) {
			s, srv := p.session(t, finishRepo(t, false),
				editCall("e", "calc/calc.go", "return 1 }\n", "return 2 }\n\nfunc Two() int { return 2 }\n"),
				p.validate,
				fake.Turn{Content: "Done."},
			)
			p.turn(t, s, 1, 1)
			quiet(t, s, srv, 3)
		})
	}
}

// The check is said once a session: a second turn whose change lacks the
// same is not told again.
func TestTheFinishCheckIsSaidOncePerRun(t *testing.T) {
	for _, p := range finishProfiles {
		t.Run(p.name, func(t *testing.T) {
			s, srv := p.session(t, finishRepo(t, true),
				editCall("e1", "calc/calc.go", "return 1 }", "return 2 }"),
				p.validate,
				fake.Turn{Content: "Done."},
				fake.Turn{Content: "It needs none of them."},
				editCall("e2", "calc/calc.go", "return 2 }", "return 3 }\n\nfunc Two() int { return 2 }"),
				p.validate,
				fake.Turn{Content: "Done again."},
			)
			p.turn(t, s, 1, 2)
			p.turn(t, s, 2, 2)
			if len(srv.Requests) != 7 {
				t.Fatalf("%d requests, want 7", len(srv.Requests))
			}
			if n := strings.Count(string(srv.Requests[6].Raw), finishIntro); n != 1 {
				t.Fatalf("the finish check was said %d times in the run, want once", n)
			}
		})
	}
}
