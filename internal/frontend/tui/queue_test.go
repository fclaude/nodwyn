package tui

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/fclaude/nodwyn/internal/event"
	"github.com/fclaude/nodwyn/internal/log"
)

// sends is a backend that records what the composer sent; the other methods
// are the nil interface's, which a send must not reach.
type sends struct {
	backend
	calls []string
	err   error
}

func (s *sends) Send(_ context.Context, _, text string, eval bool) error {
	s.calls = append(s.calls, "send:"+text)
	return nil
}
func (s *sends) SendWhileBusy(_ context.Context, _, text, mode string) error {
	s.calls = append(s.calls, mode+":"+text)
	return s.err
}

// pressAndRun presses a key and, when it started an action, runs it and hands its
// result back, as the program does.
func pressAndRun(t *testing.T, m *model, code rune, mod tea.KeyMod) tea.Cmd {
	t.Helper()
	_, cmd := m.Update(tea.KeyPressMsg{Code: code, Mod: mod})
	if cmd != nil {
		m.Update(cmd())
	}
	return cmd
}

// While a turn runs, Ctrl+O sends the draft to wait for the turn's end and
// Ctrl+S sends it to stop the turn and run next. Either empties the composer
// and shows the message as queued until its user.msg arrives; Enter still
// keeps the draft, and an empty draft sends nothing.
func TestQueueAndSteerKeysWhileBusy(t *testing.T) {
	m := testModel()
	rec := &sends{}
	m.backend = rec
	m.busy = true

	m.composer.SetValue("next")
	if pressAndRun(t, m, 'o', tea.ModCtrl) == nil || m.composer.Value() != "" || !reflect.DeepEqual(m.queued, []string{"next"}) {
		t.Fatalf("queue key: calls %v, draft %q, queued %v", rec.calls, m.composer.Value(), m.queued)
	}
	m.composer.SetValue("now")
	if pressAndRun(t, m, 's', tea.ModCtrl) == nil || m.composer.Value() != "" || !reflect.DeepEqual(m.queued, []string{"next", "now"}) {
		t.Fatalf("steer key: calls %v, draft %q, queued %v", rec.calls, m.composer.Value(), m.queued)
	}
	if want := []string{"queue:next", "steer:now"}; !reflect.DeepEqual(rec.calls, want) {
		t.Fatalf("the backend was called %v, want %v", rec.calls, want)
	}
	view := m.View().Content
	if !m.busy || !strings.Contains(view, "queued> next") || !strings.Contains(view, "queued> now") {
		t.Fatalf("busy %v, the queued messages are not shown:\n%s", m.busy, view)
	}
	if !strings.Contains(view, "Ctrl+O queues") || !strings.Contains(view, "Ctrl+S steers") || !strings.Contains(view, "Ctrl+C aborts the turn") {
		t.Fatalf("the footer does not name the keys:\n%s", view)
	}

	// Enter keeps the draft, and an evaluation is not queued.
	m.composer.SetValue("keep")
	if pressAndRun(t, m, tea.KeyEnter, 0) != nil || m.composer.Value() != "keep" || len(rec.calls) != 2 {
		t.Fatalf("busy Enter: calls %v, draft %q", rec.calls, m.composer.Value())
	}
	m.composer.SetValue(":eval (+ 1 2)")
	if pressAndRun(t, m, 'o', tea.ModCtrl) != nil || m.composer.Value() != ":eval (+ 1 2)" || len(rec.calls) != 2 {
		t.Fatalf("a queued evaluation: calls %v, draft %q", rec.calls, m.composer.Value())
	}
	m.composer.Reset()
	for _, key := range []rune{'o', 's'} {
		if pressAndRun(t, m, key, tea.ModCtrl) != nil || len(rec.calls) != 2 {
			t.Fatalf("an empty draft was sent: %v", rec.calls)
		}
	}

	// The message that starts leaves the queue, and an idle task drops what
	// it still held, as a Stop does.
	m.consume(wire(string(log.KindUserMsg), log.UserMsg{Text: "now"}, 1))
	if !reflect.DeepEqual(m.queued, []string{"next"}) {
		t.Fatalf("queued after the steering message started: %v", m.queued)
	}
	m.consume(wire(event.Status, event.StatusInfo{State: "idle"}, 0))
	if len(m.queued) != 0 || m.busy {
		t.Fatalf("after idle: queued %v, busy %v", m.queued, m.busy)
	}

	// With no turn running the keys send as Enter does.
	m.composer.SetValue("plain")
	pressAndRun(t, m, 'o', tea.ModCtrl)
	if got := rec.calls[len(rec.calls)-1]; got != "send:plain" || len(m.queued) != 0 || !m.busy {
		t.Fatalf("idle queue key: calls %v, queued %v, busy %v", rec.calls, m.queued, m.busy)
	}
}

// A message the daemon refuses to hold is not shown as queued, and the draft
// comes back, with the reason in the status line; the task is still busy.
func TestRefusedQueueGivesTheDraftBack(t *testing.T) {
	m := testModel()
	rec := &sends{err: errors.New("the model is being switched; send when it is done")}
	m.backend = rec
	m.busy = true
	m.composer.SetValue("lost")
	pressAndRun(t, m, 'o', tea.ModCtrl)
	if m.composer.Value() != "lost" || len(m.queued) != 0 || !m.busy || !strings.Contains(m.status, "model is being switched") {
		t.Fatalf("draft %q, queued %v, busy %v, status %q", m.composer.Value(), m.queued, m.busy, m.status)
	}
}
