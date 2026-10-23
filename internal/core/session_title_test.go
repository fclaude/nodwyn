package core

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/fclaude/nodwyn/internal/config"
	"github.com/fclaude/nodwyn/internal/llm"
	"github.com/fclaude/nodwyn/internal/log"
	"github.com/fclaude/nodwyn/test/fakes/must"
	fake "github.com/fclaude/nodwyn/test/fakes/ollama"
)

// titleRig is newRig for a session that asks the model for its title, as
// the daemon's sessions do (Options.Titles).
func titleRig(t *testing.T, turns ...fake.Turn) *rig {
	t.Helper()
	repo := t.TempDir()
	must.WriteFile(t, filepath.Join(repo, "main.go"), []byte("package main\n"), 0o644)
	srv := fake.NewT(t, turns...)
	t.Cleanup(srv.Close)
	cfg := config.Default()
	cfg.Control.URL = srv.URL
	cfg.Control.Model = "test-model"
	logb := &bytes.Buffer{}
	s, err := New(Options{Config: cfg, Repo: repo, Version: "test", Provider: llm.NewOllama(srv.URL), NoConfirm: true, LogWriter: logb, Titles: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return &rig{t: t, s: s, srv: srv, repo: config.CanonicalPath(repo), logb: logb}
}

// send sends text and waits for the turn to end.
func (r *rig) send(text string) {
	r.t.Helper()
	if err := r.s.Send(text); err != nil {
		r.t.Fatal(err)
	}
	r.drain("n")
}

// titles are the session.title records in the rig's log, and the session's
// listing as the log makes it.
func (r *rig) titles() ([]log.SessionTitle, SessionInfo) {
	r.t.Helper()
	evs, err := log.Read(bytes.NewReader(r.logb.Bytes()))
	if err != nil {
		r.t.Fatal(err)
	}
	var titles []log.SessionTitle
	var f infoFold
	for _, ev := range evs {
		if err := f.observe(ev.Session, ev); err != nil {
			r.t.Fatal(err)
		}
		if ev.Kind == log.KindSessionTitle {
			var p log.SessionTitle
			if err := ev.Decode(&p); err != nil {
				r.t.Fatal(err)
			}
			titles = append(titles, p)
		}
	}
	return titles, f.Info
}

// messages are a request's messages as role and content.
func messages(t *testing.T, req fake.Request) []struct{ Role, Content string } {
	t.Helper()
	var out []struct{ Role, Content string }
	for _, raw := range req.Messages {
		var m struct{ Role, Content string }
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatal(err)
		}
		out = append(out, m)
	}
	return out
}

const longFirst = "hey, so the login page keeps sending me back to / after I sign in, can you look at it and fix whatever is wrong please"

// The model names a task from its first message, in a request of its own
// that offers no tools and that the conversation never sees, and the task
// is listed by that name rather than by the message cut down.
func TestModelTitlesSessionFromFirstMessage(t *testing.T) {
	r := titleRig(t, fake.Turn{Content: "Fix login redirect"}, fake.Turn{Content: "Looked at it."})
	r.send(longFirst)
	titles, info := r.titles()
	if len(titles) != 1 || titles[0].Title != "Fix login redirect" || titles[0].Actor != "model" {
		t.Fatalf("session.title records = %+v, want one {model, Fix login redirect}", titles)
	}
	if got := info.Title(); got != "Fix login redirect" {
		t.Fatalf("Title() = %q, want the model's title", got)
	}
	if len(r.srv.Requests) != 2 {
		t.Fatalf("%d requests, want the title's and the turn's", len(r.srv.Requests))
	}
	asked := r.srv.Requests[0]
	if len(asked.Tools) != 0 {
		t.Errorf("the title request offered %d tools", len(asked.Tools))
	}
	msgs := messages(t, asked)
	if len(msgs) != 2 || msgs[0].Role != "system" || !strings.Contains(msgs[0].Content, "short title") || msgs[1].Content != longFirst {
		t.Errorf("the title request was %+v, want the title prompt and the first message alone", msgs)
	}
	for _, m := range messages(t, r.srv.Requests[1]) {
		if strings.Contains(m.Content, "short title") || m.Content == "Fix login redirect" {
			t.Errorf("the turn's request carries the title exchange: %+v", m)
		}
	}
}

// Only the first message is titled: a later one asks nothing.
func TestModelTitleAskedOnlyOnFirstMessage(t *testing.T) {
	r := titleRig(t, fake.Turn{Content: "Fix login redirect"}, fake.Turn{Content: "Looked."}, fake.Turn{Content: "Fixed."})
	r.send(longFirst)
	r.send("now also fix the logout page, which has the same problem")
	titles, info := r.titles()
	if len(titles) != 1 {
		t.Fatalf("%d session.title records, want 1: %+v", len(titles), titles)
	}
	if len(r.srv.Requests) != 3 {
		t.Fatalf("%d requests, want 3: one title and two turns", len(r.srv.Requests))
	}
	if got := info.Title(); got != "Fix login redirect" {
		t.Fatalf("Title() = %q", got)
	}
}

// A reply is made a title as a person's name is made one: its whitespace
// collapsed, without the quotes and the full stop a model puts around one,
// and no longer than a name may be.
func TestModelTitleCleansReply(t *testing.T) {
	r := titleRig(t, fake.Turn{Content: "  \"Fix\n login   redirect.\" "}, fake.Turn{Content: "Looked."})
	r.send(longFirst)
	if titles, _ := r.titles(); len(titles) != 1 || titles[0].Title != "Fix login redirect" {
		t.Errorf("records = %+v, want Fix login redirect", titles)
	}
	for reply, want := range map[string]string{
		"  Fix   login\tredirect  ":       "Fix login redirect",
		"\n\n**`Fix login redirect.`**\n": "Fix login redirect",
		"\u201cFix login redirect\u201d":  "Fix login redirect",
		" \n \t ":                         "",
	} {
		if got := cleanTitle(reply); got != want {
			t.Errorf("cleanTitle(%q) = %q, want %q", reply, got, want)
		}
	}
	long := cleanTitle(strings.Repeat("word ", 40))
	if n := utf8.RuneCountInString(long); n > maxTitle || n < maxTitle/2 || strings.HasSuffix(long, " ") {
		t.Errorf("a 200-character reply became %d characters: %q", n, long)
	}
}

// A task named before its first message, by a person or a workflow, keeps
// its name, and the model is not asked for one: the fake's script has no
// title reply, so a title request would fail the test.
func TestModelTitleKeepsExistingName(t *testing.T) {
	for _, named := range []log.SessionTitle{{Actor: "local", Title: "My name"}, {Actor: "workflow", Title: "#3 nightly"}} {
		t.Run(named.Actor, func(t *testing.T) {
			r := titleRig(t, fake.Turn{Content: "Looked."})
			if _, err := r.s.Record(log.KindSessionTitle, named); err != nil {
				t.Fatal(err)
			}
			r.send(longFirst)
			titles, info := r.titles()
			if len(titles) != 1 || info.Title() != named.Title {
				t.Fatalf("records = %+v, Title() = %q, want %q kept", titles, info.Title(), named.Title)
			}
		})
	}
}

// When the model gives no title, nothing is recorded, the turn goes on, and
// the task is read by its first message as before.
func TestModelTitleFailureFallsBack(t *testing.T) {
	for name, reply := range map[string]fake.Turn{
		"error": {Status: 400},
		"blank": {Content: " \n\t "},
	} {
		t.Run(name, func(t *testing.T) {
			r := titleRig(t, reply, fake.Turn{Content: "Looked."})
			r.send("Fix the login redirect. It sends me back to / after I sign in")
			titles, info := r.titles()
			if len(titles) != 0 {
				t.Fatalf("records = %+v, want none", titles)
			}
			if got := info.Title(); got != "Fix the login redirect" {
				t.Fatalf("Title() = %q, want the first sentence", got)
			}
			evs, err := log.Read(bytes.NewReader(r.logb.Bytes()))
			if err != nil {
				t.Fatal(err)
			}
			var said bool
			for _, ev := range evs {
				var resp log.LLMResp
				if ev.Kind == log.KindLLMResp && ev.Decode(&resp) == nil && resp.Content == "Looked." {
					said = true
				}
			}
			if !said {
				t.Fatal("the turn did not go on after the title failed")
			}
		})
	}
}
