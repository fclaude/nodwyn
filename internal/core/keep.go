package core

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/fclaude/nodwyn/internal/budget"
	"github.com/fclaude/nodwyn/internal/log"
	"github.com/fclaude/nodwyn/internal/loop"
	"github.com/fclaude/nodwyn/internal/prim"
)

// restoreKept puts the repository back to the state a test run last passed
// on, when a turn nobody watches was stopped by a hard limit or by the
// progress guard (ADR 18, amended 2026-10-06). A turn stopped that way is
// stopped where it happens to be, often one edit past the last state its own
// tests accepted, and there is nobody to say which state to go on from. The
// strategy keeps a state each time a test run passes after an edit
// (keep-passing, strategy.scm); this is the other half, which only the core
// can do, since a hard stop ends the strategy with it.
//
// It runs for a session that is --yolo, judged or unattended, the sessions
// whose turns end on evidence, and never for a chat or a person's own stop.
// The files it replaces are saved first, and what it did is written to the
// log and returned for the turn's end, so the person is told. It returns ""
// when it does not apply or nothing was kept.
func (s *Session) restoreKept(turnErr error) string {
	if turnErr == nil || s.keeper == nil || !(s.opts.AutoApprove || s.opts.JudgeApprove || s.opts.Unattended) {
		return ""
	}
	var stop *budget.Stop
	var stuck *loop.ProgressStop
	if !(errors.As(turnErr, &stop) && stop.Hard) && !errors.As(turnErr, &stuck) {
		return ""
	}
	// The turn's context is over: its stop ended it.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	res, err := s.keeper.Restore(ctx)
	var said string
	switch {
	case errors.Is(err, prim.ErrNothingKept):
		if why := s.keeper.Failed(); why != "" {
			said = "no passing state could be kept (" + why + "), so the files are as the turn left them"
		} else {
			return ""
		}
	case err != nil:
		said = "the last passing state could not be put back (" + err.Error() + "); the files are as the turn left them"
	case res.Put+res.Removed == 0 && len(res.Skipped) == 0:
		said = "the files are the state a test run last passed on"
	default:
		said = fmt.Sprintf("put the files back to the state a test run last passed on: %d written, %d removed", res.Put, res.Removed)
		if res.Put+res.Removed > 0 {
			said += "; what they held after it is saved in " + res.Discarded
		}
		if len(res.Skipped) > 0 {
			said += "; left alone: " + strings.Join(res.Skipped, ", ")
		}
	}
	_, _ = s.Record(log.KindNote, log.Note{Text: said})
	return said
}
