package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/fclaude/nodwyn/internal/config"
	"github.com/fclaude/nodwyn/internal/core"
	"github.com/fclaude/nodwyn/internal/llm"
	fake "github.com/fclaude/nodwyn/test/fakes/ollama"
)

// grep-repo, from this repository's dev/repo.scm, reads a pattern as the
// models write it, with alternation, and says when nothing matched and
// where it did not look. It read basic expressions, so every search for
// "a|b" came back as "exit 1" and was taken for an answer (ADR 14, amended
// 2026-10-05).
func TestGrepRepoReadsAlternationsAndSaysWhenNothingMatched(t *testing.T) {
	repo := fixture(t, map[string]string{
		"README.md": "fixture\n",
		"a.go":      "package a // alpha\n",
		"b.go":      "package b // beta\n",
	})
	copyInit(t, repo)
	srv := fake.New()
	defer srv.Close()
	cfg := config.Default()
	cfg.Control.URL = srv.URL
	cfg.Control.Model = "test-model"
	s, err := core.New(core.Options{Config: cfg, Repo: repo, Version: "test", Provider: llm.NewOllama(srv.URL), AutoApprove: true, LogWriter: &bytes.Buffer{}})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	grep := func(pattern, glob string) string {
		t.Helper()
		v, err := s.Eval(ctx, `(grep-repo "`+pattern+`" "`+glob+`")`)
		if err != nil {
			t.Fatalf("grep-repo %q: %v", pattern, err)
		}
		return v.String()
	}
	if got := grep(`alpha|beta`, "*.go"); !strings.Contains(got, "a.go:1:package a // alpha") || !strings.Contains(got, "b.go:1:package b // beta") {
		t.Fatalf("an alternation found: %s", got)
	}
	if got := grep(`\\bgamma\\b`, "*.go"); !strings.Contains(got, `no match for /\\bgamma\\b/ in *.go: tracked files only`) {
		t.Fatalf("nothing matched, and it said: %s", got)
	}
}
