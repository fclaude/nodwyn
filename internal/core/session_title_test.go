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

// titleRig is newRig for a session that offers the model name-task, as the
// daemon's sessions do (Options.Titles), or not.
func titleRig(t *testing.T, titles bool, turns ...fake.Turn) *rig {
	t.Helper()
	repo := t.TempDir()
	must.WriteFile(t, filepath.Join(repo, "main.go"), []byte("package main\n"), 0o644)
	srv := fake.NewT(t, turns...)
	t.Cleanup(srv.Close)
	cfg := config.Default()
	cfg.Control.URL = srv.URL
	cfg.Control.Model = "test-model"
	logb := &bytes.Buffer{}
	s, err := New(Options{Config: cfg, Repo: repo, Version: "test", Provider: llm.NewOllama(srv.URL), NoConfirm: true, LogWriter: logb, Titles: titles})
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

// offers says whether a request offers the tool name.
func offers(req fake.Request, name string) bool {
	for _, raw := range req.Tools {
		if strings.Contains(string(raw), `"name":"`+name+`"`) {
			return true
		}
	}
	return false
}

const longFirst = "hey, so the login page keeps sending me back to / after I sign in, can you look at it and fix whatever is wrong please"

// naming is a reply that calls name-task with title next to the first step.
func naming(title string) fake.Turn {
	return fake.Turn{ToolCalls: []fake.ToolCall{
		{ID: "n", Name: "name-task", Args: map[string]any{"title": title}},
		{ID: "l", Name: "fs-list", Args: map[string]any{"path": "."}},
	}}
}

// The model names a task in its first reply, beside its first step: the
// task is listed by that name rather than by the message cut down, and the
// name cost no request of its own.
func TestModelNamesTaskInItsFirstReply(t *testing.T) {
	r := titleRig(t, true, naming("Fix login redirect"), fake.Turn{Content: "Looked at it."})
	r.send(longFirst)
	titles, info := r.titles()
	if len(titles) != 1 || titles[0].Title != "Fix login redirect" || titles[0].Actor != "model" {
		t.Fatalf("session.title records = %+v, want one {model, Fix login redirect}", titles)
	}
	if got := info.Title(); got != "Fix login redirect" {
		t.Fatalf("Title() = %q, want the model's title", got)
	}
	if len(r.srv.Requests) != 2 {
		t.Fatalf("%d requests, want the turn's two: naming asks nothing of its own", len(r.srv.Requests))
	}
	first := r.srv.Requests[0]
	if !offers(first, "name-task") {
		t.Error("the first request does not offer name-task")
	}
	var told bool
	for _, m := range messages(t, first) {
		told = told || (m.Role == "system" && strings.Contains(m.Content, "names the task: call name-task"))
	}
	if !told {
		t.Error("the system prompt does not ask for the name")
	}
	if !strings.Contains(string(r.srv.Requests[1].Raw), "Named.") {
		t.Error("the model was not told the task is named")
	}
}

// A session the daemon did not open is offered no such tool and its prompt
// does not mention it, so a model scripted request by request and a
// project's tool list are as they were.
func TestNameTaskIsOfferedOnlyWhereTitlesAre(t *testing.T) {
	r := titleRig(t, false, fake.Turn{Content: "Looked."})
	r.send(longFirst)
	first := r.srv.Requests[0]
	if offers(first, "name-task") {
		t.Error("name-task is offered without Options.Titles")
	}
	for _, m := range messages(t, first) {
		if strings.Contains(m.Content, "name-task") {
			t.Errorf("the prompt names a tool that is not offered: %.80s", m.Content)
		}
	}
}

// The task is named once. A name a person or a workflow gave, and one a
// person took back with an empty name, are not replaced by a later call,
// and the model is told so.
func TestNameTaskKeepsExistingName(t *testing.T) {
	for name, named := range map[string][]log.SessionTitle{
		"a person's":   {{Actor: "local", Title: "My name"}},
		"a workflow's": {{Actor: "workflow", Title: "My name"}},
		"taken back":   {{Actor: "local", Title: "My name"}, {Actor: "local", Title: ""}},
		"its own":      {{Actor: "model", Title: "My name"}},
	} {
		t.Run(name, func(t *testing.T) {
			r := titleRig(t, true, naming("Fix login redirect"), fake.Turn{Content: "Looked."})
			for _, n := range named {
				if _, err := r.s.Record(log.KindSessionTitle, n); err != nil {
					t.Fatal(err)
				}
			}
			r.send(longFirst)
			titles, info := r.titles()
			if len(titles) != len(named) {
				t.Fatalf("records = %+v, want only %+v", titles, named)
			}
			if want := named[len(named)-1].Title; want != "" && info.Title() != want {
				t.Fatalf("Title() = %q, want %q kept", info.Title(), want)
			}
			if !strings.Contains(string(r.srv.Requests[1].Raw), "has a name already") {
				t.Error("the model was not told the task has a name")
			}
		})
	}
}

// A model that never calls it, or calls it with nothing, leaves the task
// read by its first message as before, and the turn goes on.
func TestNameTaskFallsBackToTheFirstMessage(t *testing.T) {
	for name, first := range map[string]fake.Turn{
		"not called": {ToolCalls: []fake.ToolCall{{ID: "l", Name: "fs-list", Args: map[string]any{"path": "."}}}},
		"blank":      naming(" \n\t "),
	} {
		t.Run(name, func(t *testing.T) {
			r := titleRig(t, true, first, fake.Turn{Content: "Looked."})
			r.send("Fix the login redirect. It sends me back to / after I sign in")
			titles, info := r.titles()
			if len(titles) != 0 {
				t.Fatalf("records = %+v, want none", titles)
			}
			if got := info.Title(); got != "Fix the login redirect" {
				t.Fatalf("Title() = %q, want the first sentence", got)
			}
		})
	}
}

// A name is made a title as a person's is made one: its whitespace
// collapsed, without the quotes and the full stop a model puts around one,
// and no longer than a name may be.
func TestCleanTitle(t *testing.T) {
	for reply, want := range map[string]string{
		"  \"Fix\n login   redirect.\" ":  "Fix login redirect",
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
