package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/fclaude/nodwyn/internal/config"
	fake "github.com/fclaude/nodwyn/test/fakes/ollama"
)

// pump delivers the session's events to m until done says it has what it
// waits for.
func pump(t *testing.T, m *model, done func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !done() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out; status %q, transcript:\n%s", m.status, m.viewport.View())
		}
		m.Update(m.next()())
	}
}

// typed submits what a person typed and runs the request it makes.
func typed(m *model, text string) {
	m.composer.SetValue(text)
	if cmd := m.submit(); cmd != nil {
		m.Update(cmd())
	}
}

func transcriptHas(m *model, text string) func() bool {
	return func() bool {
		for _, e := range m.transcript {
			if strings.Contains(e.text, text) {
				return true
			}
		}
		return false
	}
}

// :model shows the model the conversation talks to and the choices; :model
// NAME moves it, and the next turn asks the chosen model (ADR 41).
func TestModelChoiceThroughBothBackends(t *testing.T) {
	for _, mode := range []string{"local", "rpc"} {
		t.Run(mode, func(t *testing.T) {
			home := fake.NewT(t)
			defer home.Close()
			away := fake.NewT(t, fake.Turn{Content: "away answer"})
			defer away.Close()
			o := testOptions(t)
			o.Config.Control.URL, o.Config.Control.Model, o.Config.Control.Server = home.URL, "control-model", "home"
			o.Config.Servers = map[string]config.Server{"home": {URL: home.URL}, "away": {URL: away.URL}}
			o.Config.Models = map[string]config.Model{"coder": {Server: "away", URL: away.URL, Model: "coder-model", NumCtx: 32768}}
			b := testBackend(t, mode, o)
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			snap, err := b.Attach(ctx, o.Repo, "")
			if err != nil {
				t.Fatal(err)
			}
			m := newModel(ctx, b, o.Repo, snap.ID, false)
			m.restore(snap)

			typed(m, ":model")
			if m.inspector == nil || !strings.Contains(m.inspector.content, "talks to control (control-model) on home") ||
				!strings.Contains(m.inspector.content, "› control · control-model") || !strings.Contains(m.inspector.content, "  coder · coder-model on ") {
				t.Fatalf(":model:\n%v", m.inspector)
			}
			m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})

			typed(m, ":model coder extreme")
			if !strings.Contains(m.status, "takes no effort setting") {
				t.Fatalf("an effort the model does not take: %q", m.status)
			}
			typed(m, ":model nobody")
			if !strings.Contains(m.status, `no model named "nobody"`) {
				t.Fatalf("an unknown name: %q", m.status)
			}

			typed(m, ":model coder")
			if m.status != "" {
				t.Fatalf("switch: %q", m.status)
			}
			pump(t, m, transcriptHas(m, "model → coder (coder-model) on away"))
			typed(m, "hello")
			pump(t, m, transcriptHas(m, "nodwyn> away answer"))
			pump(t, m, func() bool { return !m.busy })
			if len(home.Requests) != 0 || len(away.Requests) != 1 || away.Requests[0].Model != "coder-model" {
				t.Fatalf("home was asked %d times, away %d", len(home.Requests), len(away.Requests))
			}
			typed(m, ":model")
			if !strings.Contains(m.inspector.content, "talks to coder (coder-model) on away") {
				t.Fatalf(":model after the switch:\n%s", m.inspector.content)
			}
		})
	}
}

// A choice made while a turn runs waits for it, and says so.
func TestModelChoiceWhileBusyIsQueued(t *testing.T) {
	home := fake.NewT(t)
	defer home.Close()
	away := fake.NewT(t)
	defer away.Close()
	o := testOptions(t)
	o.Config.Control.URL, o.Config.Control.Model, o.Config.Control.Server = home.URL, "control-model", "home"
	o.Config.Servers = map[string]config.Server{"home": {URL: home.URL}, "away": {URL: away.URL}}
	o.Config.Models = map[string]config.Model{"coder": {Server: "away", URL: away.URL, Model: "coder-model", NumCtx: 32768}}
	b := testBackend(t, "local", o)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	snap, err := b.Attach(ctx, o.Repo, "")
	if err != nil {
		t.Fatal(err)
	}
	m := newModel(ctx, b, o.Repo, snap.ID, false)
	m.restore(snap)
	// An evaluation that never ends keeps the turn running.
	typed(m, `:eval (let loop () (loop))`)
	pump(t, m, transcriptHas(m, "scheme> (let loop () (loop))"))
	typed(m, ":model coder")
	if m.status != "" {
		t.Fatalf("a choice while busy was refused: %q", m.status)
	}
	pump(t, m, transcriptHas(m, "model choice queued for when this turn ends: coder (coder-model) on away"))
	press(m, tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	pump(t, m, transcriptHas(m, "model → coder (coder-model) on away"))
}
