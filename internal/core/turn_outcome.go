package core

import (
	"context"
	"errors"

	"github.com/fclaude/nodwyn/internal/budget"
	"github.com/fclaude/nodwyn/internal/log"
)

func turnOutcome(ctx context.Context, err error) log.TurnEnd {
	if ctx.Err() != nil {
		err = context.Cause(ctx)
	}
	result := log.TurnEnd{Text: "turn ended", Outcome: "completed"}
	if err == nil {
		return result
	}
	result.Error = err.Error()
	var stop *budget.Stop
	switch {
	case errors.As(err, &stop):
		result.Outcome = "stopped"
		if stop.Hard {
			result.Outcome = "hard_budget"
		}
	case errors.Is(err, context.Canceled):
		result.Outcome = "canceled"
	default:
		result.Outcome = "error"
	}
	return result
}
