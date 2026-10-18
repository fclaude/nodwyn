package tui

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/fclaude/nodwyn/internal/core"
)

// gitInit makes dir a repository whose main branch has one commit, which is
// what a worktree is made from.
func gitInit(t *testing.T, dir string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"commit", "-q", "--allow-empty", "-m", "first"}} {
		cmd := exec.Command("git", append([]string{"-c", "commit.gpgsign=false"}, args...)...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
}

// resolved is dir as core records it, its links resolved (/var is
// /private/var on macOS).
func resolved(t *testing.T, dir string) string {
	t.Helper()
	real, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	return real
}

func testBackend(t *testing.T, o core.Options) backend {
	t.Helper()
	b, _ := testRemote(t, o)
	return b
}

// press sends one key and runs what it asks, as Bubble Tea would.
func press(m *model, key tea.KeyPressMsg) {
	if _, cmd := m.Update(key); cmd != nil {
		m.Update(cmd())
	}
}

// In a repository's top folder the chooser offers a worktree first, as the
// console does, and Enter opens the session there: on a branch of its own,
// shown in the title and the session list (ADR 34).
func TestChooserOpensAWorktreeThroughTheDaemon(t *testing.T) {
	o := testOptions(t)
	gitInit(t, o.Repo)
	b := testBackend(t, o)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	m := newModel(ctx, b, o.Repo, "", false)
	m.Update(m.Init()())
	_, rows, _ := strings.Cut(m.View().Content, "\n")
	first, _, _ := strings.Cut(rows, "\n")
	if !strings.Contains(first, "›") || !strings.Contains(first, "New task in a new worktree of") {
		t.Fatalf("the worktree row is not first and selected: %q", first)
	}
	press(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if !m.active {
		t.Fatalf("not opened: %s", m.status)
	}
	if !strings.HasPrefix(m.branch, "nodwyn/") || filepath.Dir(m.repo) != filepath.Join(resolved(t, o.Repo), ".nodwyn", "worktrees") {
		t.Fatalf("not in a worktree: repo %s, branch %q", m.repo, m.branch)
	}
	if title, _, _ := strings.Cut(m.View().Content, "\n"); !strings.Contains(title, m.branch) {
		t.Fatalf("the title does not show the branch: %q", title)
	}
	if !strings.Contains(m.viewport.View(), "Working in a git worktree of its own, on "+m.branch+", from main") {
		t.Fatalf("the conversation does not say where it works:\n%s", m.viewport.View())
	}
	items, err := b.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range items {
		if s.ID == m.id && (s.Worktree == nil || s.Worktree.Branch != m.branch || s.Worktree.Repository != resolved(t, o.Repo)) {
			t.Fatalf("the list does not have the worktree: %+v", s.Worktree)
		}
	}
}

// The second row opens in the folder itself, and a folder that is not a
// repository's top offers only that.
func TestChooserStillOpensInTheFolder(t *testing.T) {
	o := testOptions(t)
	gitInit(t, o.Repo)
	m := newModel(context.Background(), testBackend(t, o), o.Repo, "", false)
	m.Update(m.Init()())
	press(m, tea.KeyPressMsg{Code: tea.KeyDown})
	press(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if !m.active || m.repo != resolved(t, o.Repo) || m.branch != "" {
		t.Fatalf("opened %s on %q: %s", m.repo, m.branch, m.status)
	}

	plain := testOptions(t)
	m = newModel(context.Background(), testBackend(t, plain), plain.Repo, "", false)
	m.Update(m.Init()())
	if rows := m.newRows(); len(rows) != 1 || strings.Contains(m.View().Content, "worktree") {
		t.Fatalf("a folder without git offers a worktree: %v", rows)
	}
}

// --worktree opens a new session in a worktree without the chooser.
func TestWorktreeFlagSkipsTheChooser(t *testing.T) {
	o := testOptions(t)
	gitInit(t, o.Repo)
	m := newModel(context.Background(), testBackend(t, o), o.Repo, "", false)
	m.worktree = true
	m.Update(m.Init()())
	if !m.active || !strings.HasPrefix(m.branch, "nodwyn/") {
		t.Fatalf("not opened in a worktree: %q %s", m.branch, m.status)
	}
}
