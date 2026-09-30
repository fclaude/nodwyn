package rpc

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/fclaude/nodwyn/internal/event"
)

func TestWatchReportsAttentionWithoutTranscriptOrPromptContent(t *testing.T) {
	path, _ := start(t, options(t))
	watcher := connect(t, path)
	watcher.hello()
	var initial []sessionState
	if err := json.Unmarshal(watcher.ok("session.watch", map[string]any{}), &initial); err != nil || len(initial) != 0 {
		t.Fatal(initial, err)
	}
	worker := connect(t, path)
	worker.hello()
	id := opened(t, worker, t.TempDir())
	next := func(want func(sessionState) bool) sessionState {
		t.Helper()
		for range 20 {
			events := watcher.until("session.state")
			for _, ev := range events {
				if ev.Kind != "session.state" {
					t.Fatal("watch leaked another event", ev.Kind)
				}
				raw, err := json.Marshal(ev.Payload)
				if err != nil {
					t.Fatal(err)
				}
				if strings.Contains(string(raw), "private fixture") || strings.Contains(string(raw), "prompt_id") {
					t.Fatal("watch contained prompt content")
				}
				var state sessionState
				if err := json.Unmarshal(raw, &state); err != nil {
					t.Fatal(err)
				}
				if state.ID == id && want(state) {
					return state
				}
			}
		}
		t.Fatal("missing state")
		return sessionState{}
	}
	opened := next(func(s sessionState) bool { return s.Open && !s.Busy })
	subscribed(t, worker, id, 0, 100)
	eval(worker, id, `(fs-write "notes.txt" "private fixture")`)
	confirm := prompt[event.Confirm](t, worker, event.PromptConfirm)
	attention := next(func(s sessionState) bool { return s.NeedsAnswer })
	if !attention.Open || !attention.Busy || attention.Revision <= opened.Revision {
		t.Fatal(attention)
	}
	// A later watcher gets the same pending state without replaying tool work.
	other := connect(t, path)
	other.hello()
	var snapshot []sessionState
	if err := json.Unmarshal(other.ok("session.watch", map[string]any{}), &snapshot); err != nil || len(snapshot) != 1 || !snapshot[0].NeedsAnswer {
		t.Fatal(snapshot, err)
	}
	worker.ok("session.answer", map[string]any{"id": id, "prompt_id": confirm.ID, "reply": "n"})
	finishEval(t, worker)
	idle := next(func(s sessionState) bool { return s.Open && !s.Busy && !s.NeedsAnswer })
	if idle.Revision <= attention.Revision {
		t.Fatal("non-monotonic state")
	}
	worker.ok("session.close", map[string]any{"id": id})
	closed := next(func(s sessionState) bool { return !s.Open })
	if closed.NeedsAnswer || closed.Busy || closed.Revision <= idle.Revision {
		t.Fatal(closed)
	}
	if err := json.Unmarshal(watcher.ok("session.watch", map[string]any{}), &snapshot); err != nil || len(snapshot) != 0 {
		t.Fatal("closed state in open snapshot", snapshot, err)
	}
}
