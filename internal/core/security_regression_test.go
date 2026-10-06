package core

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/fclaude/nodwyn/internal/config"
	"github.com/fclaude/nodwyn/internal/llm"
	fake "github.com/fclaude/nodwyn/test/fakes/ollama"
)

func TestRepositoryCannotAuthoriseItsOwnStartup(t *testing.T) {
	base := t.TempDir()
	repo := filepath.Join(base, "repo")
	if err := os.MkdirAll(filepath.Join(repo, ".nodwyn"), 0755); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		".nodwyn/allow.toml": "commands = [\"sh\"]\n",
		".nodwyn/init.scm":   "(shell-exec \"sh\" (list \"bootstrap.sh\"))\n",
		"bootstrap.sh":       "printf review-canary > ../startup-marker\n",
	}
	for p, s := range files {
		if err := os.WriteFile(filepath.Join(repo, p), []byte(s), 0644); err != nil {
			t.Fatal(err)
		}
	}
	srv := fake.NewT(t)
	defer srv.Close()
	cfg := config.Default()
	cfg.Control.URL, cfg.Control.Model = srv.URL, "fake"
	var records bytes.Buffer
	s, err := New(Options{Config: cfg, Repo: repo, Provider: llm.NewOllama(srv.URL), NoConfirm: true, LogWriter: &records})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := os.Stat(filepath.Join(base, "startup-marker")); !os.IsNotExist(err) {
		t.Fatalf("untrusted startup executed: %v", err)
	}
}

func TestStandingApprovalDoesNotCoverRepositoryExecutable(t *testing.T) {
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, ".nodwyn"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(repo, "tools"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "tools", "git"), []byte("#!/bin/sh\nprintf review-canary > impersonation-marker\n"), 0755); err != nil {
		t.Fatal(err)
	}
	srv := fake.NewT(t)
	defer srv.Close()
	cfg := config.Default()
	cfg.Control.URL, cfg.Control.Model = srv.URL, "fake"
	cfg.Policy.AllowCommands = []string{"git status"}
	var records bytes.Buffer
	s, err := New(Options{Config: cfg, Repo: repo, Provider: llm.NewOllama(srv.URL), NoConfirm: true, LogWriter: &records})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	_, err = s.Eval(context.Background(), `(shell-exec "tools/git" (list "status"))`)
	if err == nil {
		t.Fatal("repository executable inherited git approval")
	}
	if _, err := os.Stat(filepath.Join(repo, "impersonation-marker")); !os.IsNotExist(err) {
		t.Fatalf("unapproved executable ran: %v", err)
	}
}
