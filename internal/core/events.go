package core

import (
	"github.com/fclaude/nibb/internal/event"
	"github.com/fclaude/nibb/internal/log"
	"github.com/fclaude/nibb/internal/redact"
	"github.com/fclaude/nibb/internal/window"
)

// Record appends to the log, keeps the event for the window, and forwards
// it to the front-end. It is the loop.Host implementation.
func (s *Session) Record(kind log.Kind, payload any) (log.Event, error) {
	clean, redactions, err := redact.Payload(payload, nil)
	if err != nil {
		return log.Event{}, err
	}
	payload = clean
	ev, err := s.logw.Append(kind, payload)
	if err != nil {
		return ev, err
	}
	s.notePlan(kind, payload)
	s.remember(ev)
	s.Emit(event.FromLog(ev, payload))
	return ev, s.recordRedactions(ev.Seq, redactions)
}

func (s *Session) recordRedactions(source uint64, redactions []redact.Event) error {
	if len(redactions) == 0 {
		return nil
	}
	_, err := s.Record(log.KindRedact, struct {
		Source uint64         `json:"source_seq"`
		Events []redact.Event `json:"events"`
	}{source, redactions})
	return err
}

// EventsSoFar returns a snapshot of the log so far (loop.Host).
func (s *Session) EventsSoFar() []log.Event {
	s.evmu.Lock()
	defer s.evmu.Unlock()
	return append([]log.Event(nil), s.events...)
}

// FinalReply returns the model's last reply of the latest turn that the
// conversation still shows (window.LastReply): a folded sub-agent's reply is
// not the turn's answer.
func (s *Session) FinalReply() (log.LLMResp, bool) { return window.LastReply(s.EventsSoFar()) }

// Emit delivers an event to the front-end (loop.Host). It blocks if the
// front-end is not draining; that is backpressure, not a bug.
func (s *Session) Emit(ev event.Event) {
	s.mu.Lock()
	closed := s.closed
	s.mu.Unlock()
	if closed {
		return
	}
	clean, _, err := redact.Payload(ev.Payload, nil)
	if err != nil {
		ev = event.Event{Kind: event.Error, Payload: event.ErrorInfo{Err: "event redaction failed"}}
	} else {
		ev.Payload = clean
	}
	s.out <- ev
}

// outputWriter turns Scheme display/write output into Output events.
type outputWriter struct{ s *Session }

func (w outputWriter) Write(p []byte) (int, error) {
	w.s.outputMu.Lock()
	_, _ = w.s.output.Write(p)
	w.s.outputMu.Unlock()
	return len(p), nil
}

// flushOutput joins Scheme display fragments before scanning, just as model
// fragments are joined before display. It runs at evaluation/turn boundaries.
func (s *Session) flushOutput() {
	s.outputMu.Lock()
	text := s.output.String()
	s.output.Reset()
	s.outputMu.Unlock()
	if text != "" {
		s.Emit(event.Event{Kind: event.Output, Payload: event.Text{Text: text}})
	}
}

// remember is the only mutation point for the context's recorded history.
func (s *Session) remember(ev log.Event) {
	s.evmu.Lock()
	s.events = append(s.events, ev)
	s.evmu.Unlock()
}

// restoreHistory redacts legacy logs again before they enter model context.
// Original records remain intact on disk; they are not appended a second time.
func (s *Session) restoreHistory(events []log.Event) error {
	for _, ev := range events {
		if len(ev.Payload) > 0 {
			payload, _, err := redact.JSON(ev.Payload, nil)
			if err != nil {
				return err
			}
			ev.Payload = payload
		}
		s.remember(ev)
	}
	return nil
}
