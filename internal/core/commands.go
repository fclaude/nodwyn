package core

import (
	"errors"

	"github.com/fclaude/nodwyn/internal/config"
	"github.com/fclaude/nodwyn/internal/guard"
	"github.com/fclaude/nodwyn/internal/log"
)

// CommandsChoice is who answers a task's commands, one of approval.commands'
// values, given to the task itself (ADR 35, amended 2026-10-30), and who
// chose it, for the log. Settings' approval.commands is what a task starts
// with when it was given none.
type CommandsChoice struct {
	Mode  string
	Actor string
}

// Commands is who answers the task's commands now: its own choice, else
// config.toml's approval.commands; and who chose it, empty for the setting.
func (s *Session) Commands() (string, string) {
	s.mu.Lock()
	mode, actor := s.commands, s.commandsActor
	s.mu.Unlock()
	if mode == "" {
		return s.approval().Commands, ""
	}
	return mode, actor
}

// SetCommands makes mode who answers the task's commands, as actor, from its
// next confirm, and a question about a command waiting for the person is
// answered again by it unless it is to ask. The change is recorded before it
// applies, and resuming the task keeps it.
func (s *Session) SetCommands(mode, actor string) error {
	if err := config.CheckCommands(mode); err != nil {
		return errors.New("commands " + err.Error())
	}
	s.commandsMu.Lock()
	defer s.commandsMu.Unlock()
	s.mu.Lock()
	closed, same := s.closed || s.closing, s.commands == mode
	s.mu.Unlock()
	if closed {
		return errors.New("core: session is closed")
	}
	if same {
		return nil
	}
	if _, err := s.Record(log.KindSessionCommands, log.SessionCommands{Actor: actor, Commands: mode}); err != nil {
		return err
	}
	s.mu.Lock()
	s.commands, s.commandsActor = mode, actor
	s.mu.Unlock()
	if mode == config.ApproveAsk {
		return nil
	}
	// What waits for the person about a command is answered again by the
	// new choice (approvalConfirmer), so picking the judge alone or no
	// asking also answers the question on the screen.
	s.promptMu.Lock()
	for id, pending := range s.prompts {
		if pending.checkpoint == nil && pending.choice == nil && pending.reply != nil && commandPrompt(pending.prompt) && (pending.ctx == nil || pending.ctx.Err() == nil) {
			delete(s.prompts, id)
			pending.reply <- guard.Approval{PromptID: id, Scope: rescoped}
		}
	}
	s.promptMu.Unlock()
	return nil
}

// lastCommands is the latest session.commands record, if the log has one
// that holds a value approval.commands still takes.
func lastCommands(events []log.Event) (rec log.SessionCommands, found bool) {
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].Kind != log.KindSessionCommands {
			continue
		}
		if err := events[i].Decode(&rec); err != nil || config.CheckCommands(rec.Commands) != nil {
			return log.SessionCommands{}, false
		}
		return rec, true
	}
	return log.SessionCommands{}, false
}
