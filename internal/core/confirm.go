package core

import (
	"context"
	"fmt"
	"strings"

	"github.com/fclaude/nibb/internal/event"
	"github.com/fclaude/nibb/internal/guard"
	"github.com/fclaude/nibb/internal/log"
)

type pendingConfirmation struct {
	prompt guard.Prompt
	reply  chan bool
}

// Answer replies to a prompt event. "y"/"yes" confirm once. "a"/"always"
// confirms and adds a standing approval for the rest of the session: the
// command prefix for a command line, the file's directory for a write.
// "t"/"turn" (writes only) confirms and approves every further write until
// the turn ends. Anything else declines.
func (s *Session) Answer(id, reply string) error {
	s.promptMu.Lock()
	pending, ok := s.prompts[id]
	if ok {
		delete(s.prompts, id)
	}
	s.promptMu.Unlock()
	if !ok {
		return fmt.Errorf("core: no pending prompt %q", id)
	}
	p, ch := pending.prompt, pending.reply
	r := strings.ToLower(strings.TrimSpace(reply))
	switch {
	case (r == "a" || r == "always") && len(p.Argv) > 0:
		prefix := guard.Prefix(p.Argv)
		s.allow.Add(prefix)
		_, _ = s.Record(log.KindNote, log.Note{Text: "standing command approval for this session: " + prefix})
		ch <- true
	case (r == "a" || r == "always") && p.Path != "":
		prefix := guard.DirPrefix(p.Path)
		if guard.IsProtected(p.Path) {
			_, _ = s.Record(log.KindNote, log.Note{Text: "no standing approval for protected path " + p.Path + "; approved once"})
		} else {
			s.writes.Add(prefix)
			_, _ = s.Record(log.KindNote, log.Note{Text: "standing write approval for this session: " + prefix})
		}
		ch <- true
	case (r == "t" || r == "turn") && p.Path != "":
		s.mu.Lock()
		s.turnTrust = true
		s.mu.Unlock()
		_, _ = s.Record(log.KindNote, log.Note{Text: "writes approved for the rest of this turn"})
		ch <- true
	default:
		ch <- r == "y" || r == "yes"
	}
	return nil
}

// Allowed lists the standing command approvals in effect.
func (s *Session) Allowed() []string { return s.allow.List() }

// AllowedWrites lists the standing write approvals in effect.
func (s *Session) AllowedWrites() []string { return s.writes.List() }

// confirm is the guard's Confirmer: emit a prompt, wait for Answer or
// cancellation.
func (s *Session) confirm(ctx context.Context, p guard.Prompt) (bool, error) {
	if p.Path != "" && !guard.IsProtected(p.Path) {
		s.mu.Lock()
		trust := s.turnTrust
		s.mu.Unlock()
		if trust {
			_, _ = s.Record(log.KindNote, log.Note{Text: "write approved by turn trust: " + p.Prim + " " + p.Path + "\n" + p.Detail})
			return true, nil
		}
	}
	s.promptMu.Lock()
	s.promptN++
	id := fmt.Sprintf("p%d", s.promptN)
	ch := make(chan bool, 1)
	s.prompts[id] = pendingConfirmation{prompt: p, reply: ch}
	s.promptMu.Unlock()
	s.Emit(event.Event{Kind: event.PromptConfirm, Payload: event.Confirm{ID: id, Prompt: p}})
	select {
	case ok := <-ch:
		return ok, nil
	case <-ctx.Done():
		s.promptMu.Lock()
		delete(s.prompts, id)
		s.promptMu.Unlock()
		return false, ctx.Err()
	}
}
