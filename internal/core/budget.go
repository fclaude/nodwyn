package core

import (
	"context"
	"crypto/rand"
	"strings"

	"github.com/fclaude/nibb/internal/budget"
	"github.com/fclaude/nibb/internal/event"
	"github.com/fclaude/nibb/internal/guard"
	"github.com/fclaude/nibb/internal/log"
	"github.com/fclaude/nibb/internal/scheme"
)

type checkpointReply struct {
	choice budget.Choice
	actor  string
}

type pendingCheckpoint struct {
	info  budget.Checkpoint
	reply chan checkpointReply
}

// confirmBudget uses the session's existing prompt IDs, reply queue and
// cancellation path. Budget choices never create standing tool approvals.
func (s *Session) confirmBudget(ctx context.Context, p budget.Checkpoint) (choice budget.Choice, err error) {
	var actor, id string
	defer func() {
		record := log.TurnCheckpoint{Checkpoint: p, Choice: choice, Actor: actor, PromptID: id}
		if err != nil {
			record.Error = err.Error()
		}
		if _, recordErr := s.Record(log.KindTurnCheckpoint, record); recordErr != nil {
			err = recordErr
		}
	}()
	if s.opts.NoConfirm {
		return budget.Choice{}, nil
	}
	s.promptMu.Lock()
	id = "p" + rand.Text()
	ch := make(chan checkpointReply, 1)
	s.prompts[id] = pendingConfirmation{ctx: ctx, checkpoint: &pendingCheckpoint{info: p, reply: ch}}
	s.promptMu.Unlock()
	defer func() { s.promptMu.Lock(); delete(s.prompts, id); s.promptMu.Unlock() }()
	s.Emit(event.Event{Kind: event.PromptCheckpoint, Payload: event.Checkpoint{ID: id, Checkpoint: p}})
	select {
	case reply := <-ch:
		choice, actor = reply.choice, reply.actor
		return choice, nil
	case <-ctx.Done():
		return budget.Choice{}, context.Cause(ctx)
	}
}

// turnGate leaves authorization with guard, then accounts for actual host
// actions. A Scheme wrapper cannot hide several host actions behind one tool.
type turnGate struct{ scheme.Gate }

func (g turnGate) Before(ctx context.Context, p *scheme.Prim, args []scheme.Value) error {
	if err := g.Gate.Before(ctx, p, args); err != nil {
		return err
	}
	t := budget.From(ctx)
	if t == nil || !p.Meta.Host {
		return nil
	}
	if !p.Meta.Irreversible {
		return t.Check(ctx)
	}
	path, _ := guard.WritePath(p, args)
	command := strings.Join(guard.CommandArgs(p, args), " ")
	return t.Action(ctx, path, command)
}
