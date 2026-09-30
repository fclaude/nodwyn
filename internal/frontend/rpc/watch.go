package rpc

import (
	"slices"
	"sync"

	"github.com/fclaude/nodwyn/internal/core"
)

// sessionState is deliberately content-free: watching all sessions does not
// subscribe a client to their transcripts or prompt bodies.
type sessionState struct {
	ID          string `json:"id"`
	Revision    uint64 `json:"revision"`
	Open        bool   `json:"open"`
	Busy        bool   `json:"busy"`
	NeedsAnswer bool   `json:"needs_answer"`
}

// stateWatch has its own lock so a session can drain while lifecycle operations
// wait for Close. Each session's drain goroutine is its sole live state publisher.
type stateWatch struct {
	mu       sync.Mutex
	revision uint64
	states   map[string]sessionState
	clients  map[*peer]bool
}

func (w *stateWatch) update(session *core.Session, open bool, running ...bool) {
	state := sessionState{ID: session.ID(), Open: open}
	if open {
		state.Busy = session.Busy() || (len(running) > 0 && running[0])
		pending, err := session.PendingPrompts()
		state.NeedsAnswer = err != nil || len(pending) > 0
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.states == nil {
		w.states = make(map[string]sessionState)
	}
	previous, exists := w.states[state.ID]
	state.Revision = previous.Revision
	if exists && state == previous {
		return
	}
	w.revision++
	state.Revision = w.revision
	if open {
		w.states[state.ID] = state
	} else {
		delete(w.states, state.ID)
	}
	for client := range w.clients {
		if !client.enqueue(notification{"2.0", "session.event", sessionEvent{ID: state.ID, Kind: "session.state", Payload: state}}) {
			delete(w.clients, client)
		}
	}
}
func (w *stateWatch) subscribe(client *peer) []sessionState {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.clients == nil {
		w.clients = make(map[*peer]bool)
	}
	w.clients[client] = true
	result := make([]sessionState, 0, len(w.states))
	for _, state := range w.states {
		result = append(result, state)
	}
	slices.SortFunc(result, func(a, b sessionState) int {
		if a.ID < b.ID {
			return -1
		}
		if a.ID > b.ID {
			return 1
		}
		return 0
	})
	return result
}
func (w *stateWatch) unsubscribe(client *peer) {
	w.mu.Lock()
	defer w.mu.Unlock()
	delete(w.clients, client)
}
