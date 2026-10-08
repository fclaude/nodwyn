package core

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fclaude/nodwyn/internal/binding"
	"github.com/fclaude/nodwyn/internal/config"
	"github.com/fclaude/nodwyn/internal/keychain"
	"github.com/fclaude/nodwyn/internal/llm"
	"github.com/fclaude/nodwyn/internal/log"
	fake "github.com/fclaude/nodwyn/test/fakes/ollama"
)

// twoJudges is a session whose config.toml has two judges, [judge] on
// "first" and "second" beside it, each with its own key and binding, opened
// with judge (nil for [judge]'s). Its log is the rig's buffer, or with
// onDisk the sessions directory, where Resume reads it.
func twoJudges(t *testing.T, first, second *fakeJudge, judge *JudgeChoice, onDisk bool) (*rig, Options) {
	t.Helper()
	srv := fake.NewT(t)
	t.Cleanup(srv.Close)
	cfg := config.Default()
	cfg.Servers = map[string]config.Server{
		"first":  {Provider: "jev", URL: first.URL, Credential: "first-key"},
		"second": {Provider: "jev", URL: second.URL, Credential: "second-key"},
	}
	cfg.Judge.Server = "first"
	kc, err := keychain.Create(filepath.Join(t.TempDir(), "kc"), keychain.FastKDF, []byte("p"))
	if err != nil {
		t.Fatal(err)
	}
	for _, class := range []string{"first-key", "second-key"} {
		if _, err := kc.Add(class, []byte("sk-"+class+"-canary")); err != nil {
			t.Fatal(err)
		}
	}
	bearer := []string{"header:Authorization:Bearer"}
	o := Options{Config: cfg, Repo: t.TempDir(), Provider: llm.NewOllama(srv.URL), NoConfirm: true, Keychain: kc, Judge: judge,
		Paths: config.PathsUnder(filepath.Join(t.TempDir(), "config"), filepath.Join(t.TempDir(), "data")),
		Bindings: binding.Table{Bind: []binding.Binding{
			{Class: "first-key", Dest: "127.0.0.1", Positions: bearer, Methods: []string{"POST"}},
			{Class: "second-key", Dest: "127.0.0.1", Positions: bearer, Methods: []string{"POST"}},
		}}}
	// The table above is the bindings, not a file under Paths.
	o.Paths.Bindings = ""
	logb := &bytes.Buffer{}
	if !onDisk {
		o.LogWriter = logb
	}
	s, err := newInitSession(o)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return &rig{t: t, s: s, srv: srv, repo: config.CanonicalPath(o.Repo), logb: logb}, o
}

// A task opened with a judge of its own asks that one, not [judge]'s, with
// that judge's key, and the log says so (ADR 47).
func TestTaskAsksTheJudgeItWasOpenedWith(t *testing.T) {
	t.Parallel()
	first, second := newFakeJudge(t), newFakeJudge(t, noul(0.7))
	r, _ := twoJudges(t, first, second, &JudgeChoice{Server: "second", Actor: "tester"}, false)
	if got := r.s.Judge(); got.Server != "second" || got.Model != "jev-latest" || !got.Available || got.Host != "127.0.0.1" {
		t.Fatalf("judge: %+v", got)
	}
	if got := r.eval(`(judge-yes-no "a text" "is it fine?")`); got != "0.7" {
		t.Fatalf("answer %q", got)
	}
	if len(first.asked()) != 0 || len(second.asked()) != 1 || second.auths[0] != "Bearer sk-second-key-canary" {
		t.Fatalf("first asked %v, second %v %v", first.asked(), second.asked(), second.auths)
	}
	var rec log.SessionJudge
	for _, ev := range r.logged() {
		if ev.Kind == log.KindSessionJudge {
			if err := ev.Decode(&rec); err != nil {
				t.Fatal(err)
			}
		}
	}
	if rec.Server != "second" || rec.Actor != "tester" || rec.Provider != "jev" || rec.Host != "127.0.0.1" || rec.Model != "jev-latest" {
		t.Fatalf("session.judge: %+v", rec)
	}
}

// A task moves to another judge between turns: the next question goes
// there, and the guard checks it against that judge's host and key class,
// so it never judges one destination while bytes go to another. Choosing
// none stops the questions, and the plan gate sees there is no judge.
func TestTaskSwitchesItsJudgeBetweenTurns(t *testing.T) {
	t.Parallel()
	first, second := newFakeJudge(t, noul(0.1)), newFakeJudge(t, noul(0.9))
	r, _ := twoJudges(t, first, second, nil, false)
	if got := r.eval(`(judge-yes-no "a text" "q")`); got != "0.1" {
		t.Fatalf("[judge]'s answer %q", got)
	}
	state, err := r.s.SetJudge("second", "tester")
	if err != nil || state.Server != "second" || !state.Available {
		t.Fatalf("switch: %+v %v", state, err)
	}
	if got := r.eval(`(judge-yes-no "a text" "q")`); got != "0.9" {
		t.Fatalf("second's answer %q", got)
	}
	p, _ := r.s.interp.Lookup("judge-yes-no")
	if p.Meta.EgressClass != "second-key" || p.Meta.EgressHost != "127.0.0.1" {
		t.Fatalf("the guard checks %s at %s", p.Meta.EgressClass, p.Meta.EgressHost)
	}
	if _, err := r.s.SetJudge("", "tester"); err != nil {
		t.Fatal(err)
	}
	if got := r.eval(`(judge-available?)`); got != "#f" {
		t.Fatalf("judge-available? with none: %s", got)
	}
	if _, err := r.s.Eval(context.Background(), `(judge-yes-no "a text" "q")`); err == nil || !strings.Contains(err.Error(), "this task has no judge") {
		t.Fatalf("a question with no judge: %v", err)
	}
	if len(first.asked()) != 1 || len(second.asked()) != 1 {
		t.Fatalf("first asked %v, second %v", first.asked(), second.asked())
	}
	if _, err := r.s.SetJudge("nowhere", "tester"); err == nil || !strings.Contains(err.Error(), "not configured") {
		t.Fatalf("an unknown server: %v", err)
	}
}

// A resumed task asks the judge it chose, and one config.toml no longer has
// leaves it with none rather than [judge]'s, which nobody chose for it.
func TestResumedTaskKeepsItsJudge(t *testing.T) {
	t.Parallel()
	first, second := newFakeJudge(t), newFakeJudge(t)
	r, o := twoJudges(t, first, second, nil, true)
	if _, err := r.s.SetJudge("second", "tester"); err != nil {
		t.Fatal(err)
	}
	id := r.s.ID()
	r.s.Close()
	o.Repo = ""
	s, err := resumeInitSession(o, id)
	if err != nil {
		t.Fatal(err)
	}
	if got := s.Judge(); got.Server != "second" {
		t.Fatalf("resumed with %+v", got)
	}
	s.Close()
	delete(o.Config.Servers, "second")
	s, err = resumeInitSession(o, id)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if got := s.Judge(); got.Server != "" {
		t.Fatalf("a judge config.toml lost became %+v", got)
	}
	found := false
	for _, ev := range s.EventsSoFar() {
		var n log.Note
		if ev.Kind == log.KindNote && ev.Decode(&n) == nil && strings.Contains(n.Text, "The judge this task chose is not available (second") {
			found = true
		}
	}
	if !found {
		t.Fatal("no note says the judge was lost")
	}
}
