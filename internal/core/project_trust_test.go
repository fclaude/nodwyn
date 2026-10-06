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

func TestProjectTrustExpiresWhenAuthorityFilesChange(t *testing.T) {
	repo, dir := t.TempDir(), t.TempDir()
	paths := config.PathsUnder(filepath.Join(dir, "config"), filepath.Join(dir, "data"))
	if err := os.MkdirAll(filepath.Join(repo, ".nodwyn"), 0755); err != nil {
		t.Fatal(err)
	}
	init := filepath.Join(repo, ".nodwyn", "init.scm")
	allow := filepath.Join(repo, ".nodwyn", "allow.toml")
	write := func(path, text string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write(init, `(define trusted-marker "one")`)
	write(allow, `commands = ["git status"]`)
	trust := func() {
		t.Helper()
		f, err := config.ReadProjectFiles(repo)
		if err != nil {
			t.Fatal(err)
		}
		if err := config.TrustProject(context.Background(), paths, repo, f.Revision()); err != nil {
			t.Fatal(err)
		}
	}
	srv := fake.NewT(t)
	defer srv.Close()
	open := func() *Session {
		t.Helper()
		s, err := New(Options{Config: config.Default(), Paths: paths, Repo: repo, Provider: llm.NewOllama(srv.URL), NoConfirm: true, LogWriter: &bytes.Buffer{}})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { s.Close() })
		return s
	}
	trust()
	s := open()
	if v, err := s.Eval(context.Background(), "trusted-marker"); err != nil || v.S != "one" {
		t.Fatalf("trusted init: %v %v", v, err)
	}
	write(init, `(define trusted-marker "two")`)
	if _, err := s.Eval(context.Background(), "(reload)"); err == nil {
		t.Fatal("changed init reloaded without review")
	}
	if _, err := open().Eval(context.Background(), "trusted-marker"); err == nil {
		t.Fatal("changed init loaded on restart")
	}
	trust()
	if v, err := open().Eval(context.Background(), "trusted-marker"); err != nil || v.S != "two" {
		t.Fatalf("reviewed change: %v %v", v, err)
	}
	write(allow, `commands = ["git status", "sh"]`)
	if got := open().Allowed(); len(got) != 0 {
		t.Fatalf("changed approvals took effect: %v", got)
	}
	trust()
	if err := config.TrustProject(context.Background(), paths, repo, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := open().Eval(context.Background(), "trusted-marker"); err == nil {
		t.Fatal("revoked init loaded")
	}
}
