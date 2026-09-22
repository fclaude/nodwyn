package core

import (
	"slices"

	"github.com/fclaude/nibb/internal/event"
	"github.com/fclaude/nibb/internal/redact"
)

// PendingPrompts returns a redacted display snapshot for a reconnecting
// frontend. IDs are still checked by Answer/SecretReply at reply time: a
// snapshot is never authority. Secret values and reply channels are excluded.
func (s *Session) PendingPrompts() ([]event.Event, error) {
	pending := []event.Event{}
	s.promptMu.Lock()
	ids := make([]string, 0, len(s.prompts))
	for id := range s.prompts {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	for _, id := range ids {
		p := s.prompts[id]
		if p.checkpoint != nil {
			pending = append(pending, event.Event{Kind: event.PromptCheckpoint, Payload: event.Checkpoint{ID: id, Checkpoint: p.checkpoint.info}})
		} else {
			pending = append(pending, event.Event{Kind: event.PromptConfirm, Payload: event.Confirm{ID: id, Prompt: p.prompt}})
		}
	}
	s.promptMu.Unlock()
	s.secretsMu.Lock()
	ids = ids[:0]
	for id := range s.secrets {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	for _, id := range ids {
		pending = append(pending, event.Event{Kind: event.PromptSecret, Payload: event.SecretPrompt{ID: id, Class: s.secrets[id].class}})
	}
	s.secretsMu.Unlock()
	for i := range pending {
		clean, _, err := redact.Payload(pending[i].Payload, nil)
		if err != nil {
			return nil, err
		}
		pending[i].Payload = clean
	}
	return pending, nil
}
