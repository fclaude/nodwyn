package core

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fclaude/nodwyn/internal/config"
	"github.com/fclaude/nodwyn/internal/event"
	"github.com/fclaude/nodwyn/internal/log"
)

// asked drains a session's events, answering every confirm with answer,
// and says which primitives it was asked about.
type asked struct {
	mu    sync.Mutex
	prims []string
	seen  chan string
}

func answering(t *testing.T, s *Session, answer string) *asked {
	t.Helper()
	a := &asked{seen: make(chan string, 16)}
	go func() {
		for ev := range s.Events() {
			if ev.Kind != event.PromptConfirm {
				continue
			}
			c := ev.Payload.(event.Confirm)
			a.mu.Lock()
			a.prims = append(a.prims, c.Prompt.Prim)
			a.mu.Unlock()
			a.seen <- c.ID
			if answer != "" {
				_ = s.Answer(c.ID, answer)
			}
		}
	}()
	return a
}

func (a *asked) list() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.prims...)
}

func approvalChanges(t *testing.T, s *Session) []log.ApprovalChange {
	t.Helper()
	var out []log.ApprovalChange
	for _, ev := range s.EventsSoFar() {
		var p log.ApprovalChange
		if ev.Kind == log.KindApprovalChange && ev.Decode(&p) == nil {
			out = append(out, p)
		}
	}
	return out
}

// "Don't ask" answers the ordinary writes and commands for the person who
// turned it on, and installs nothing; a protected path still asks, and
// turned off, everything asks again. Each switch is in the log.
func TestApproveAllAnswersOrdinaryWritesAndCommands(t *testing.T) {
	r := newAskingRig(t)
	a := answering(t, r.s, "n")
	ctx := context.Background()
	if err := r.s.SetApproveAll(true, "unix:uid=1"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.s.Eval(ctx, `(fs-write "a.txt" "a")`); err != nil {
		t.Fatal(err)
	}
	if _, err := r.s.Eval(ctx, `(shell-exec "true" '())`); err != nil {
		t.Fatal(err)
	}
	if got := a.list(); len(got) != 0 {
		t.Fatalf("asked about %v", got)
	}
	if b, err := os.ReadFile(filepath.Join(r.repo, "a.txt")); err != nil || string(b) != "a" {
		t.Fatalf("a.txt: %q %v", b, err)
	}
	if len(r.s.Allowed()) != 0 || len(r.s.AllowedWrites()) != 0 {
		t.Fatalf("installed %v %v", r.s.Allowed(), r.s.AllowedWrites())
	}
	answered := 0
	for _, ev := range r.s.EventsSoFar() {
		var d log.GuardDecision
		if ev.Kind == log.KindGuardDecision && ev.Decode(&d) == nil && d.Scope == "all" {
			if d.Actor != "unix:uid=1" || d.Answer != "yes" {
				t.Fatalf("decision %+v", d)
			}
			answered++
		}
	}
	if answered != 2 {
		t.Fatalf("%d decisions answered by don't ask, want 2", answered)
	}
	// A protected path is a security confirm: still asked, here declined.
	if _, err := r.s.Eval(ctx, `(fs-write ".nodwyn/init.scm" "(define x 1)")`); err == nil {
		t.Fatal("a protected write went through unasked")
	}
	if err := r.s.SetApproveAll(false, "unix:uid=1"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.s.Eval(ctx, `(fs-write "b.txt" "b")`); err == nil {
		t.Fatal("a write went through after don't ask was turned off")
	}
	if got := a.list(); len(got) != 2 || got[0] != "fs-write" || got[1] != "fs-write" {
		t.Fatalf("asked about %v", got)
	}
	changes := approvalChanges(t, r.s)
	if len(changes) != 2 || changes[0].Kind != "all" || changes[0].Off || !changes[1].Off || changes[1].Actor != "unix:uid=1" {
		t.Fatalf("changes %+v", changes)
	}
	if on, _ := r.s.ApproveAll(); on {
		t.Fatal("still on")
	}
}

// Turned on while an ordinary write waits for an answer, it answers it.
func TestApproveAllAnswersWhatIsWaiting(t *testing.T) {
	r := newAskingRig(t)
	a := answering(t, r.s, "")
	done := make(chan error, 1)
	go func() {
		_, err := r.s.Eval(context.Background(), `(fs-write "a.txt" "a")`)
		done <- err
	}()
	select {
	case <-a.seen:
	case <-time.After(5 * time.Second):
		t.Fatal("not asked")
	}
	if err := r.s.SetApproveAll(true, "unix:uid=1"); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the waiting write was not answered")
	}
	if len(r.s.AllowedWrites()) != 0 {
		t.Fatalf("installed %v", r.s.AllowedWrites())
	}
}

// No Scheme reaches it, whatever its origin: a repository's init.scm runs
// as user-origin Scheme, as the person's own evaluations do. No primitive
// names it, and what Scheme can define changes nothing.
func TestApproveAllIsNotScheme(t *testing.T) {
	repo := t.TempDir()
	os.MkdirAll(filepath.Join(repo, ".nodwyn"), 0o755)
	os.WriteFile(filepath.Join(repo, ".nodwyn", "init.scm"), []byte("(define approve-all #t)\n(define dont-ask #t)\n"), 0o644)
	cfg := diskOptions(t).Config
	cfg.Approval.Writes = config.ApproveAsk // a write is the confirm asked
	s, err := New(Options{Repo: repo, Paths: diskOptions(t).Paths, Config: cfg})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	answering(t, s, "n")
	for _, p := range s.interp.Prims() {
		if strings.Contains(p.Name, "approve") || strings.Contains(p.Name, "ask") {
			t.Errorf("primitive %q", p.Name)
		}
	}
	s.Eval(context.Background(), "(set! approve-all #t)")
	if on, _ := s.ApproveAll(); on {
		t.Fatal("Scheme turned it on")
	}
	if _, err := s.Eval(context.Background(), `(fs-write "a.txt" "a")`); err == nil {
		t.Fatal("a write went through unasked")
	}
}

// A session whose confirms nobody is asked has nothing to stop asking.
func TestApproveAllNeedsAPersonAsked(t *testing.T) {
	for _, o := range []Options{{NoConfirm: true}, {AutoApprove: true}, {Unattended: true}} {
		o.Repo, o.Paths, o.Config = t.TempDir(), diskOptions(t).Paths, diskOptions(t).Config
		s, err := New(o)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.SetApproveAll(true, "unix:uid=1"); err == nil {
			t.Errorf("%+v accepted it", o)
		}
		if err := s.SetApproveAll(true, ""); err == nil {
			t.Error("no actor accepted")
		}
		s.Close()
	}
}

// It is never saved: a resumed session starts asking again.
func TestApproveAllEndsWithTheSession(t *testing.T) {
	o := diskOptions(t)
	o.NoConfirm = false
	s, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for range s.Events() {
		}
	}()
	id := s.ID()
	if err := s.SetApproveAll(true, "unix:uid=1"); err != nil {
		t.Fatal(err)
	}
	s.Close()
	resumed, err := Resume(o, id)
	if err != nil {
		t.Fatal(err)
	}
	defer resumed.Close()
	go func() {
		for range resumed.Events() {
		}
	}()
	if on, _ := resumed.ApproveAll(); on {
		t.Fatal("don't ask survived a resume")
	}
}
