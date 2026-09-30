package core

import (
	"context"
	"crypto/rand"
	"errors"

	"github.com/fclaude/nodwyn/internal/event"
	"github.com/fclaude/nodwyn/internal/scheme"
)

// ReadFriction starts a user-origin inspection through the ordinary interpreter
// and guard. The asynchronous result lets a frontend answer any custom checker
// prompt while the read is pending. The fixed path is not caller-controlled.
func (s *Session) ReadFriction(actor string) (string, error) {
	id := "i" + rand.Text()
	err := s.startTurn(func(ctx context.Context) error {
		value, err := s.evalRecorded(ctx, `(fs-read "docs/friction.md")`, actor)
		result := event.Inspection{ID: id, Name: "friction"}
		if err == nil && value.Kind != scheme.KString {
			err = errors.New("friction read returned no text")
		}
		if err != nil {
			result.Err = err.Error()
		} else {
			result.Text = value.S
		}
		// Emit applies the normal redaction boundary before any transport sees text.
		s.Emit(event.Event{Kind: event.InspectionResult, Payload: result})
		return err
	})
	if err != nil {
		return "", err
	}
	return id, nil
}
