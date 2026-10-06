package guard_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/fclaude/nodwyn/internal/guard"
	"github.com/fclaude/nodwyn/internal/prim"
	"github.com/fclaude/nodwyn/internal/scheme"
	"github.com/fclaude/nodwyn/test/fakes/home"
)

func TestMain(m *testing.M) {
	done := home.Isolate()
	code := m.Run()
	done()
	os.Exit(code)
}

// The real checker pipeline must keep argument-level authority out of a
// standing prefix, including when a shell wraps that same command.
func TestStandingApprovalsRespectArgumentBoundaries(t *testing.T) {
	repo, bin, outside := t.TempDir(), t.TempDir(), t.TempDir()
	for _, name := range []string{"git", "go", "make", "gofmt", "staticcheck", "rg", "bash", "tail", "grep"} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\nexit 99\n"), 0755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	if err := os.Symlink(outside, filepath.Join(repo, "escape")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(repo, "escape with spaces")); err != nil {
		t.Fatal(err)
	}
	allow := guard.NewCommandAllowIn(repo, "git", "go", "git grep", "git log", "git diff", "git status", "git ls-files", "go test", "go build", "go vet", "make", "gofmt -l", "staticcheck", "rg")
	unsafe := [][]string{
		{"git", "grep", "--open-files-in-pager=program", "-e", "x"},
		{"git", "grep", "-Oprogram", "x"},
		{"git", "log", "-1", "--output=" + filepath.Join(outside, "log")},
		{"git", "diff", "--output=inside.patch"},
		{"git", "diff", "--no-index", filepath.Join(outside, "secret"), "/dev/null"},
		{"git", "diff", "--ext-diff"}, {"git", "diff", "--textconv"},
		{"go", "test", "-exec", filepath.Join(outside, "program"), "./..."},
		{"go", "test", "-exec=program", "./..."},
		{"go", "build", "-o", filepath.Join(outside, "binary"), "./..."},
		{"go", "build", "-toolexec=program", "./..."},
		{"go", "build", "-overlay=overlay.json", "./..."},
		{"go", "build", "-ldflags=-extld=program", "./..."},
		{"go", "test", "-coverprofile=coverage.out", "./..."},
		{"go", "test", "-args", "-exec=program"},
		{"make", "test", "SHELL=" + filepath.Join(outside, "program")},
		{"make", "test", "CC=program"}, {"make", "-f", "other.mk", "test"},
		{"make", "--file=other.mk", "test"}, {"make", "-C", "sub", "test"},
		{"make", "--directory=sub", "test"}, {"make", "--eval=recipe", "test"},
		{"make", "test", "another-target"},
		{"git", "-c", "core.pager=program", "log"}, {"go", "-C", "sub", "test"},
		{"git", "diff", "--", filepath.Join(repo, "inside")},
		{"git", "diff", "--", "../outside"}, {"git", "diff", "--", "a/../../outside"},
		{"git", "diff", "--", "escape/secret"},
		{"git", "diff", "--", "escape/../outside"},
		{"git", "log", "--format=" + filepath.Join(outside, "value")},
		{"git", "log", "--format=../outside"},
		{"git", "log", "--format=escape with spaces/file"},
		{"git", "log", "--unknown-option=value"},
		{"gofmt", "-l", "-w", "file.go"},
		{"rg", "-z", "pattern"},
		{filepath.Join(bin, "go"), "test", "./..."},
	}
	ctx := scheme.WithOrigin(context.Background(), scheme.OriginModel)
	for _, argv := range unsafe {
		for _, shell := range []bool{false, true} {
			call := argv
			if shell {
				quoted := make([]string, len(argv))
				for i, arg := range argv {
					quoted[i] = strconv.Quote(arg)
				}
				call = []string{"bash", "-c", strings.Join(quoted, " ")}
			}
			t.Run(strings.Join(call, " "), func(t *testing.T) {
				if got := allow.Match(call); got != "" {
					t.Errorf("unsafe arguments inherited standing approval %q", got)
				}
				var prompts []guard.Prompt
				g := guard.New(guard.ConfirmFunc(func(_ context.Context, p guard.Prompt) (bool, error) {
					prompts = append(prompts, p)
					return false, nil
				}), nil, prim.Checkers(prim.DefaultOptions(repo), allow, nil)...)
				err := g.Before(ctx, shellP(), shellArgs(call[0], call[1:]...))
				var denied *scheme.Denied
				if !errors.As(err, &denied) || len(prompts) != 1 || !prompts[0].Security || prompts[0].Ordinary() || prompts[0].ApprovalOptions().Session {
					t.Fatalf("unsafe arguments did not need a person: error=%v prompts=%+v", err, prompts)
				}
			})
		}
	}
	for _, argv := range [][]string{
		{filepath.Join(bin, "bash"), "-c", "git status"},
		{"bash", "-c", "git status | grep " + strconv.Quote(filepath.Join(outside, "file"))},
		{"bash", "-c", `git status | grep "escape with spaces/file"`},
	} {
		t.Run("shell boundary "+strings.Join(argv, " "), func(t *testing.T) {
			if got := allow.Match(argv); got != "" {
				t.Errorf("shell boundary inherited approval %q", got)
			}
			d := allow.Check(ctx, shellP(), shellArgs(argv[0], argv[1:]...))
			if d.Verdict != guard.Confirm || !d.Security {
				t.Fatalf("shell boundary did not ask a person: %+v", d)
			}
		})
	}
	for _, argv := range [][]string{
		{"go", "test", "-race", "-run", "TestA|TestB", "-count=1", "./..."},
		{"go", "build", "./..."}, {"go", "vet", "./..."},
		{"git", "log", "--oneline", "-10"}, {"git", "diff", "--stat", "HEAD", "--", "internal/"},
		{"git", "grep", "-n", "-I", "-P", "-e", "alpha|beta", "--", "*.go"},
		{"git", "status", "--porcelain=v1"}, {"git", "ls-files", "--", "docs/*.md"},
		{"make", "test"}, {"make", "-j2", "test"}, {"gofmt", "-l", "."}, {"staticcheck", "./..."},
		{"bash", "-c", "go test ./... 2>&1 | tail -n 20"},
	} {
		t.Run("ordinary "+strings.Join(argv, " "), func(t *testing.T) {
			g := guard.New(nil, nil, prim.Checkers(prim.DefaultOptions(repo), allow, nil)...)
			if err := g.Before(ctx, shellP(), shellArgs(argv[0], argv[1:]...)); err != nil {
				t.Fatalf("ordinary approved command asked: %v", err)
			}
		})
	}
}

func TestArgvTriggerSeesPathsInsideFlagValues(t *testing.T) {
	repo, outside := t.TempDir(), t.TempDir()
	trigger := guard.DefaultArgvTrigger(repo)
	for _, value := range []string{filepath.Join(outside, "file"), "../outside", "a/../../outside", "escape/../outside"} {
		for _, argv := range [][]string{{"git", "log", "--output=" + value}, {"bash", "-c", "git log --output=" + value}} {
			d := trigger.Check(scheme.WithOrigin(context.Background(), scheme.OriginModel), shellP(), shellArgs(argv[0], argv[1:]...))
			if d.Verdict != guard.Confirm || !d.Security || !strings.Contains(d.Reason, "path") {
				t.Errorf("%q: flag value did not cause a security path confirm: %+v", argv, d)
			}
		}
	}
}
